package watch

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Claude Code writes one JSONL transcript per session to
// ~/.claude/projects/<encoded cwd>/<session>.jsonl. Subagents write to
// <session>/subagents/agent-<id>.jsonl next to a .meta.json file.
type claude struct{ dir string }

func newClaude() *claude {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(home(), ".claude")
	}
	return &claude{dir: filepath.Join(dir, "projects")}
}

func (c *claude) close() {}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

func (c *claude) scan(w *Watcher, cutoff time.Time) {
	prefix := nonAlnum.ReplaceAllString(w.opt.Root, "-")
	dirs, _ := os.ReadDir(c.dir)
	for _, d := range dirs {
		name := d.Name()
		if !d.IsDir() || (!w.opt.All && name != prefix && !strings.HasPrefix(name, prefix+"-")) {
			continue
		}
		pdir := filepath.Join(c.dir, name)
		mains, _ := filepath.Glob(filepath.Join(pdir, "*.jsonl"))
		subs, _ := filepath.Glob(filepath.Join(pdir, "*", "subagents", "agent-*.jsonl"))
		for _, f := range append(mains, subs...) {
			key := "claude:" + f
			if w.has(key) {
				continue
			}
			st, err := os.Stat(f)
			if err != nil || st.ModTime().Before(cutoff) {
				continue
			}
			cwd := claudeCwd(f)
			if !w.inRoot(cwd) {
				continue
			}
			h := &claudeHandle{t: tail{path: f, start: w.opt.Tail}}
			s := newSession(key, "claude", h)
			s.Cwd = cwd
			if strings.Contains(f, string(filepath.Separator)+"subagents"+string(filepath.Separator)) {
				h.sub = true
				subagentMeta(s, f)
			}
			w.add(s)
		}
	}
}

var cwdRe = regexp.MustCompile(`"cwd":"((?:[^"\\]|\\.)*)"`)

// claudeCwd finds the working directory in the first lines of a transcript.
func claudeCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 256<<10)
	n, _ := f.Read(buf)
	m := cwdRe.FindSubmatch(buf[:n])
	if m == nil {
		return ""
	}
	var s string
	if json.Unmarshal(append(append([]byte{'"'}, m[1]...), '"'), &s) != nil {
		return ""
	}
	return s
}

func subagentMeta(s *session, f string) {
	sessDir := filepath.Dir(filepath.Dir(f))
	s.Parent = "claude:" + sessDir + ".jsonl"
	s.Name = "subagent"
	b, err := os.ReadFile(strings.TrimSuffix(f, ".jsonl") + ".meta.json")
	if err != nil {
		return
	}
	var m struct {
		AgentType   string `json:"agentType"`
		Description string `json:"description"`
	}
	if json.Unmarshal(b, &m) == nil {
		if m.AgentType != "" {
			s.Name = strings.ToLower(strings.TrimSuffix(m.AgentType, "-purpose"))
		}
		s.Title = m.Description
	}
}

type claudeHandle struct {
	t   tail
	sub bool
}

type claudeLine struct {
	Type          string          `json:"type"`
	Subtype       string          `json:"subtype"`
	Timestamp     string          `json:"timestamp"`
	Cwd           string          `json:"cwd"`
	IsSidechain   bool            `json:"isSidechain"`
	IsMeta        bool            `json:"isMeta"`
	AITitle       string          `json:"aiTitle"`
	Message       *claudeMsg      `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

type claudeMsg struct {
	StopReason string          `json:"stop_reason"`
	Content    json.RawMessage `json:"content"`
	Usage      *struct {
		Input       int `json:"input_tokens"`
		CacheCreate int `json:"cache_creation_input_tokens"`
		CacheRead   int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func (h *claudeHandle) poll(s *session) error {
	return h.t.lines(func(b []byte) { h.line(s, b) })
}

func (h *claudeHandle) line(s *session, b []byte) {
	var l claudeLine
	if json.Unmarshal(b, &l) != nil {
		return
	}
	if l.IsSidechain && !h.sub {
		return
	}
	t := parseTime(l.Timestamp, s.Updated)
	if l.Cwd != "" {
		s.Cwd = l.Cwd
	}
	switch l.Type {
	case "ai-title":
		if !h.sub && l.AITitle != "" {
			s.Title = l.AITitle
			s.dirty = true
		}
	case "system":
		if l.Subtype == "turn_duration" {
			s.endTurn(t, "")
		}
	case "assistant":
		if l.Message != nil {
			h.assistant(s, t, l.Message)
		}
	case "user":
		if l.Message != nil {
			h.user(s, t, &l)
		}
	}
}

func (h *claudeHandle) assistant(s *session, t time.Time, m *claudeMsg) {
	var blocks []claudeBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return
	}
	hasText := false
	for _, b := range blocks {
		switch b.Type {
		case "text":
			s.say(t, b.Text)
			hasText = true
		case "tool_use":
			evs := toolEvents(b.Name, decodeArgs(b.Input), s.Cwd, toolStyle{})
			for i := range evs {
				evs[i].CallID = b.ID
			}
			s.add(t, evs...)
		default:
			s.touch(t)
		}
	}
	if u := m.Usage; u != nil {
		if ctx := u.Input + u.CacheCreate + u.CacheRead; ctx > 0 {
			s.Ctx = ctx
			if ctx > s.CtxMax {
				s.CtxMax = 1_000_000
			}
		}
	}
	if m.StopReason == "end_turn" && hasText && s.pending() < 0 {
		s.endTurn(t, "")
	}
}

var commandRe = regexp.MustCompile(`<command-name>([^<]*)</command-name>[\s\S]*?(?:<command-args>([^<]*)</command-args>)?`)

func (h *claudeHandle) user(s *session, t time.Time, l *claudeLine) {
	m := l.Message
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		h.prompt(s, t, text, l.IsMeta)
		return
	}
	var blocks []claudeBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			h.prompt(s, t, b.Text, l.IsMeta)
		case "tool_result":
			h.result(s, t, &b, l.ToolUseResult)
		}
	}
}

func (h *claudeHandle) prompt(s *session, t time.Time, text string, meta bool) {
	text = strings.TrimSpace(text)
	switch {
	case meta || text == "":
		return
	case strings.HasPrefix(text, "[Request interrupted"):
		s.endTurn(t, "interrupted")
		return
	case strings.HasPrefix(text, "<"):
		m := commandRe.FindStringSubmatch(text)
		if m == nil {
			return
		}
		text = strings.TrimSpace(m[1] + " " + m[2])
	}
	if h.sub && s.Prompt == "" {
		// The first prompt of a subagent is its full brief. The title from
		// the meta file is a better label.
		s.Prompt = oneLine(text, 120)
		s.touch(t)
		return
	}
	s.prompt(t, text)
}

type claudeResult struct {
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	Interrupted bool   `json:"interrupted"`
	Type        string `json:"type"`
	File        *struct {
		StartLine int `json:"startLine"`
		NumLines  int `json:"numLines"`
	} `json:"file"`
	StructuredPatch []hunk `json:"structuredPatch"`
	NumFiles        *int   `json:"numFiles"`
	NumMatches      *int   `json:"numMatches"`
	Filenames       []any  `json:"filenames"`
}

type hunk struct {
	OldStart int      `json:"oldStart"`
	NewStart int      `json:"newStart"`
	Lines    []string `json:"lines"`
}

func (h *claudeHandle) result(s *session, t time.Time, b *claudeBlock, raw json.RawMessage) {
	idx := s.call(b.ToolUseID)
	if len(idx) == 0 {
		return
	}
	var r claudeResult
	if len(raw) > 0 && raw[0] == '{' {
		_ = json.Unmarshal(raw, &r)
	}
	text := blockText(b.Content)
	for _, i := range idx {
		e := &s.Events[i]
		switch e.Kind {
		case KindRun:
			out := text
			if r.Stdout != "" || r.Stderr != "" {
				out = strings.TrimRight(r.Stdout+"\n"+r.Stderr, "\n")
			}
			runResult(e, out, -1, b.IsError || r.Interrupted)
		case KindRead:
			if r.File != nil && r.File.NumLines > 0 {
				e.Line = max(0, r.File.StartLine-1)
				e.EndLine = e.Line + r.File.NumLines - 1
			}
		case KindEdit:
			if r.Type == "create" {
				e.Created = true
			}
			applyHunks(e, r.StructuredPatch)
		case KindGrep:
			switch {
			case r.NumMatches != nil:
				e.Hits = *r.NumMatches
			case r.NumFiles != nil:
				e.Hits = *r.NumFiles
			default:
				e.Hits = len(splitLines(text))
			}
		}
		if e.Result == Pending {
			e.Result = OK
			if b.IsError {
				e.Result = Failed
			}
		}
	}
	s.touch(t)
}

// applyHunks sets the edit position and line counts from a structured patch.
func applyHunks(e *Event, hunks []hunk) {
	if len(hunks) == 0 {
		return
	}
	first, last, add, del := -1, -1, 0, 0
	for _, h := range hunks {
		n := h.NewStart - 1
		for _, l := range h.Lines {
			switch {
			case strings.HasPrefix(l, "+"):
				add++
				if first < 0 {
					first = n
				}
				last = n
				n++
			case strings.HasPrefix(l, "-"):
				del++
				if first < 0 {
					first = n
				}
			default:
				n++
			}
		}
	}
	e.Add, e.Del = add, del
	if first >= 0 {
		e.Line, e.EndLine = max(0, first), max(0, max(first, last))
	}
}

// blockText returns the text of a tool result, which is a string or a list
// of content blocks.
func blockText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func parseTime(s string, fallback time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return fallback
}
