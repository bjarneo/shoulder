package watch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Gemini CLI writes chats to ~/.gemini/tmp/<project>/chats/. The project
// directory is a short name with a .project_root file, or the SHA-256 of the
// project path in older versions. Current versions append JSONL records with
// $set, $patch and $rewindTo updates. Older versions rewrite one JSON file.
type gemini struct {
	dir    string
	files  logCache // chats of other projects
	walked time.Time
}

func newGemini() *gemini {
	base := filepath.Join(home(), ".gemini")
	if h := os.Getenv("GEMINI_CLI_HOME"); h != "" {
		base = filepath.Join(h, ".gemini")
	}
	return &gemini{dir: filepath.Join(base, "tmp"), files: logCache{}}
}

func (g *gemini) close() {}

func (g *gemini) scan(w *Watcher, cutoff time.Time) {
	sum := sha256.Sum256([]byte(w.opt.Root))
	hash := hex.EncodeToString(sum[:])
	walk := walkDue(&g.walked)
	dirs, _ := os.ReadDir(g.dir)
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		pdir := filepath.Join(g.dir, d.Name())
		chats := filepath.Join(pdir, "chats")
		root := ""
		if b, err := os.ReadFile(filepath.Join(pdir, ".project_root")); err == nil {
			root = strings.TrimSpace(string(b))
		}
		cwd := ""
		switch {
		case w.inRoot(root):
			cwd = root
		case d.Name() == hash:
			cwd = w.opt.Root
		default:
			// The directory belongs to another project, or to an unknown
			// one. Only a walk updates the count of its chats.
			if root != "" && walk {
				g.away(chats, cutoff)
			}
			continue
		}
		files, _ := filepath.Glob(filepath.Join(chats, "session-*.json*"))
		subs, _ := filepath.Glob(filepath.Join(chats, "*", "*.jsonl"))
		for _, f := range append(files, subs...) {
			key := "gemini:" + f
			if w.has(key) || !(strings.HasSuffix(f, ".json") || strings.HasSuffix(f, ".jsonl")) {
				continue
			}
			st, err := os.Stat(f)
			if err != nil || st.ModTime().Before(cutoff) || w.parkedLog(key, st.ModTime()) {
				continue
			}
			h := &geminiHandle{path: f, legacy: strings.HasSuffix(f, ".json"), t: tail{path: f, start: w.opt.Tail}, msgs: map[string]*gemMsg{}}
			s := newSession(key, "gemini", h)
			s.Cwd = cwd
			s.CtxMax = 1_048_576
			if filepath.Dir(f) != chats {
				s.Name = "subagent"
			}
			w.add(s, st.ModTime())
		}
	}
	w.away["gemini"] = g.files.count(cutoff, nil)
}

// away records the main chats with a write at or after cutoff in the chats
// directory of another project.
func (g *gemini) away(chats string, cutoff time.Time) {
	files, _ := filepath.Glob(filepath.Join(chats, "session-*.json*"))
	for _, f := range files {
		if !strings.HasSuffix(f, ".json") && !strings.HasSuffix(f, ".jsonl") {
			continue
		}
		if st, err := os.Stat(f); err == nil && !st.ModTime().Before(cutoff) {
			g.files[f] = logInfo{away: true, mod: st.ModTime()}
		}
	}
}

type geminiHandle struct {
	path   string
	legacy bool
	t      tail
	mtime  time.Time // the modification time of a legacy file at the last read
	size   int64     // the bytes of a legacy file at the last read
	msgs   map[string]*gemMsg
	order  []string
	last   time.Time // time of the last change
}

type gemMsg struct {
	ID             string          `json:"id"`
	Timestamp      string          `json:"timestamp"`
	Type           string          `json:"type"`
	Content        json.RawMessage `json:"content"`
	DisplayContent json.RawMessage `json:"displayContent"`
	ToolCalls      []gemTool       `json:"toolCalls"`
	Tokens         *struct {
		Input int `json:"input"`
	} `json:"tokens"`
}

type gemTool struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Args          map[string]any  `json:"args"`
	Result        json.RawMessage `json:"result"`
	Status        string          `json:"status"`
	Timestamp     string          `json:"timestamp"`
	ResultDisplay json.RawMessage `json:"resultDisplay"`
}

type gemPatch struct {
	ID        *string         `json:"id"`
	Content   json.RawMessage `json:"content"`
	ToolCalls []struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
	} `json:"toolCalls"`
	Updates   []gemPatch `json:"updates"`
	RemoveIDs []string   `json:"removeIds"`
	OrderIDs  []string   `json:"orderIds"`
}

func (h *geminiHandle) poll(s *session) error {
	changed := false
	if h.legacy {
		st, err := os.Stat(h.path)
		if err != nil {
			return err
		}
		s.seen(st.ModTime())
		// A rewrite can change the file twice in one tick of the file
		// clock. The size tells a half-written file from the full one.
		if st.ModTime().Equal(h.mtime) && st.Size() == h.size {
			h.maybeEnd(s)
			return nil
		}
		b, err := os.ReadFile(h.path)
		if err != nil {
			return err
		}
		h.mtime, h.size = st.ModTime(), int64(len(b))
		var conv struct {
			Kind     string    `json:"kind"`
			Messages []*gemMsg `json:"messages"`
		}
		if json.Unmarshal(b, &conv) != nil {
			return nil
		}
		h.setAll(conv.Messages)
		changed = true
	} else {
		err := h.t.lines(func(b []byte) {
			if h.record(s, b) {
				changed = true
			}
		})
		s.seen(h.t.mod)
		if err != nil {
			return err
		}
	}
	if changed {
		h.last = time.Now()
		h.rebuild(s)
	}
	h.maybeEnd(s)
	return nil
}

func (h *geminiHandle) setAll(msgs []*gemMsg) {
	h.msgs, h.order = map[string]*gemMsg{}, nil
	for _, m := range msgs {
		if m != nil && m.ID != "" {
			h.put(m)
		}
	}
}

func (h *geminiHandle) put(m *gemMsg) {
	if _, ok := h.msgs[m.ID]; !ok {
		h.order = append(h.order, m.ID)
	}
	h.msgs[m.ID] = m
}

// record applies one JSONL record and reports whether the messages changed.
func (h *geminiHandle) record(s *session, b []byte) bool {
	var probe map[string]json.RawMessage
	if json.Unmarshal(b, &probe) != nil {
		return false
	}
	switch {
	case probe["$rewindTo"] != nil:
		var id string
		_ = json.Unmarshal(probe["$rewindTo"], &id)
		for i, x := range h.order {
			if x == id {
				for _, r := range h.order[i:] {
					delete(h.msgs, r)
				}
				h.order = h.order[:i]
				return true
			}
		}
		h.setAll(nil)
		return true
	case probe["$patch"] != nil:
		var p gemPatch
		if json.Unmarshal(probe["$patch"], &p) != nil {
			return false
		}
		h.patch(&p)
		for i := range p.Updates {
			h.patch(&p.Updates[i])
		}
		for _, id := range p.RemoveIDs {
			h.remove(id)
		}
		if len(p.OrderIDs) > 0 {
			h.reorder(p.OrderIDs)
		}
		return true
	case probe["$set"] != nil:
		var set struct {
			Kind     string    `json:"kind"`
			Messages []*gemMsg `json:"messages"`
		}
		if json.Unmarshal(probe["$set"], &set) != nil {
			return false
		}
		if set.Kind == "subagent" {
			s.Name = "subagent"
		}
		if set.Messages != nil {
			h.setAll(set.Messages)
			return true
		}
		return false
	case probe["id"] != nil:
		var m gemMsg
		if json.Unmarshal(b, &m) != nil || m.ID == "" {
			return false
		}
		h.put(&m)
		return true
	case probe["sessionId"] != nil:
		var meta struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(b, &meta) == nil && meta.Kind == "subagent" {
			s.Name = "subagent"
		}
	}
	return false
}

func (h *geminiHandle) patch(p *gemPatch) {
	if p.ID == nil {
		return
	}
	m := h.msgs[*p.ID]
	if m == nil {
		return
	}
	if p.Content != nil {
		m.Content = p.Content
	}
	for _, tp := range p.ToolCalls {
		for i := range m.ToolCalls {
			if m.ToolCalls[i].ID == tp.ID {
				m.ToolCalls[i].Result = tp.Result
			}
		}
	}
}

func (h *geminiHandle) remove(id string) {
	if _, ok := h.msgs[id]; !ok {
		return
	}
	delete(h.msgs, id)
	for i, x := range h.order {
		if x == id {
			h.order = append(h.order[:i], h.order[i+1:]...)
			break
		}
	}
}

// reorder moves the listed messages to the end in the given order.
func (h *geminiHandle) reorder(ids []string) {
	in := map[string]bool{}
	var tailIDs []string
	for _, id := range ids {
		if _, ok := h.msgs[id]; ok && !in[id] {
			in[id] = true
			tailIDs = append(tailIDs, id)
		}
	}
	var out []string
	for _, id := range h.order {
		if !in[id] {
			out = append(out, id)
		}
	}
	h.order = append(out, tailIDs...)
}

const maxRebuild = 300

func (h *geminiHandle) rebuild(s *session) {
	s.reset()
	order := h.order
	if len(order) > maxRebuild {
		order = order[len(order)-maxRebuild:]
	}
	for _, id := range order {
		m := h.msgs[id]
		t := parseTime(m.Timestamp, s.Updated)
		switch m.Type {
		case "user":
			raw := m.DisplayContent
			if raw == nil {
				raw = m.Content
			}
			s.prompt(t, partsText(raw))
		case "gemini":
			s.say(t, partsText(m.Content))
			for _, tc := range m.ToolCalls {
				evs := toolEvents(tc.Name, args(tc.Args), s.Cwd, toolStyle{zeroBasedOffset: true})
				tt := parseTime(tc.Timestamp, t)
				for i := range evs {
					evs[i].CallID = tc.ID
					geminiResult(&evs[i], &tc)
				}
				s.add(tt, evs...)
			}
			if m.Tokens != nil && m.Tokens.Input > 0 {
				s.Ctx = m.Tokens.Input
			}
			s.touch(t)
		case "error":
			s.say(t, "error: "+partsText(m.Content))
		}
	}
}

// maybeEnd ends the turn when the file has been quiet for a moment and the
// last message is a reply with no open tool calls, an error, or a reply with
// only cancelled tool calls. Gemini CLI does not log the end of a turn.
func (h *geminiHandle) maybeEnd(s *session) {
	if s.TurnEnded || len(h.order) == 0 || time.Since(h.last) < 4*time.Second {
		return
	}
	m := h.msgs[h.order[len(h.order)-1]]
	t := parseTime(m.Timestamp, s.Updated)
	switch m.Type {
	case "error":
		// An error, such as a used-up quota, ends the turn. The text of
		// the turn end is the error.
		s.endTurn(t, "")
		return
	case "gemini":
	default:
		return
	}
	cancelled := 0
	for _, tc := range m.ToolCalls {
		switch tc.Status {
		case "success", "error":
		case "cancelled":
			cancelled++
		default:
			return
		}
	}
	switch {
	case cancelled > 0 && cancelled == len(m.ToolCalls):
		// Gemini CLI does not send cancelled calls back to the model, so
		// the turn ends.
		s.endTurn(t, "interrupted")
	case strings.TrimSpace(partsText(m.Content)) != "":
		s.endTurn(t, "")
	}
}

var gemOutputRe = regexp.MustCompile(`(?s)Output: (.*?)(?:\n(?:Error|Exit Code|Signal|Background PIDs|Process Group PGID): |$)`)

func geminiResult(e *Event, tc *gemTool) {
	switch tc.Status {
	case "success":
		e.Result = OK
	case "error", "cancelled":
		e.Result = Failed
	case "awaiting_approval":
		e.Approval = true
		return
	default:
		return
	}
	text := partsText(tc.Result)
	switch e.Kind {
	case KindRun:
		out := text
		if m := gemOutputRe.FindStringSubmatch(text); m != nil {
			out = strings.TrimSpace(m[1])
			if out == "(empty)" {
				out = ""
			}
		}
		code := -1
		if m := exitRe.FindStringSubmatch(text); m != nil {
			code = atoi(m[1])
		}
		runResult(e, out, code, tc.Status != "success")
	case KindEdit:
		var d struct {
			DiffStat *struct {
				Added   int `json:"model_added_lines"`
				Removed int `json:"model_removed_lines"`
			} `json:"diffStat"`
			IsNewFile bool `json:"isNewFile"`
		}
		if json.Unmarshal(tc.ResultDisplay, &d) == nil && d.DiffStat != nil {
			e.Add, e.Del = d.DiffStat.Added, d.DiffStat.Removed
			e.Created = e.Created || d.IsNewFile
		}
	case KindGrep:
		if m := foundRe.FindStringSubmatch(text); m != nil {
			e.Hits = atoi(m[1])
		}
	}
}

var foundRe = regexp.MustCompile(`(?i)found (\d+)`)

// partsText returns the text of a Gemini PartListUnion value: a string, a
// part, or a list of parts. A part has text or a function response.
func partsText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		list = []json.RawMessage{raw}
	}
	var b strings.Builder
	for _, p := range list {
		var part struct {
			Text     string `json:"text"`
			Thought  bool   `json:"thought"`
			Response *struct {
				Response map[string]any `json:"response"`
			} `json:"functionResponse"`
		}
		if json.Unmarshal(p, &part) != nil {
			var str string
			if json.Unmarshal(p, &str) == nil {
				b.WriteString(str)
			}
			continue
		}
		switch {
		case part.Thought:
		case part.Text != "":
			b.WriteString(part.Text)
		case part.Response != nil:
			b.WriteString(args(part.Response.Response).str("output", "error", "content"))
		}
	}
	return b.String()
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
