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

// omp writes one JSONL transcript per session to
// ~/.omp/agent/sessions/<project>/<time>_<id>.jsonl. A subagent writes to a
// directory named after the transcript of its parent, and its session record
// names the parent file in parentSession. A turn is a user message, then
// assistant messages with text, thinking and toolCall blocks that the tool
// results answer. omp repeats each toolCall in a custom tool_execution_start
// record, which this reader ignores.
type omp struct {
	dir  string
	skip map[string]bool // transcripts that belong to another project
}

func newOmp() *omp {
	dir := os.Getenv("PI_CODING_AGENT_SESSION_DIR")
	if dir == "" {
		base := os.Getenv("PI_CODING_AGENT_DIR")
		if base == "" {
			base = filepath.Join(home(), ".omp", "agent")
		}
		dir = filepath.Join(base, "sessions")
	}
	return &omp{dir: dir, skip: map[string]bool{}}
}

func (o *omp) close() {}

func (o *omp) scan(w *Watcher, cutoff time.Time) {
	mains, _ := filepath.Glob(filepath.Join(o.dir, "*", "*.jsonl"))
	subs, _ := filepath.Glob(filepath.Join(o.dir, "*", "*", "*.jsonl"))
	for _, f := range append(mains, subs...) {
		key := "omp:" + f
		if w.has(key) || o.skip[f] {
			continue
		}
		st, err := os.Stat(f)
		if err != nil || st.ModTime().Before(cutoff) {
			continue
		}
		cwd, parent := ompMeta(f)
		switch {
		case cwd == "":
			// The session record is not written yet.
			continue
		case !w.inRoot(cwd):
			o.skip[f] = true
			continue
		}
		h := &ompHandle{t: tail{path: f, start: w.opt.Tail}, jobs: map[string]string{}}
		s := newSession(key, "omp", h)
		s.Cwd = cwd
		if parent != "" {
			s.Parent = "omp:" + parent
			s.Name = strings.TrimSuffix(filepath.Base(f), ".jsonl")
		}
		w.add(s)
	}
}

// ompMeta reads the project directory and the parent session from the session
// record at the top of a transcript.
func ompMeta(path string) (cwd, parent string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	buf := make([]byte, 32<<10)
	n, _ := f.Read(buf)
	for _, line := range bytes.Split(buf[:n], []byte{'\n'}) {
		var rec struct {
			Type          string `json:"type"`
			Cwd           string `json:"cwd"`
			ParentSession string `json:"parentSession"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.Type != "session" {
			continue
		}
		return rec.Cwd, rec.ParentSession
	}
	return "", ""
}

type ompHandle struct {
	t    tail
	jobs map[string]string // background job id -> tool call of the command
}

type ompRecord struct {
	Type       string          `json:"type"`
	CustomType string          `json:"customType"`
	Timestamp  string          `json:"timestamp"`
	Cwd        string          `json:"cwd"`
	Title      string          `json:"title"`
	Content    string          `json:"content"`
	Details    json.RawMessage `json:"details"`
	Message    *ompMsg         `json:"message"`
}

type ompMsg struct {
	Role        string          `json:"role"`
	Content     json.RawMessage `json:"content"`
	Attribution string          `json:"attribution"`
	ToolCallID  string          `json:"toolCallId"`
	ToolName    string          `json:"toolName"`
	Details     json.RawMessage `json:"details"`
	IsError     bool            `json:"isError"`
	StopReason  string          `json:"stopReason"`
	Snapshot    *struct {
		Prompt int `json:"promptTokens"`
	} `json:"contextSnapshot"`
}

type ompBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (h *ompHandle) poll(s *session) error {
	return h.t.lines(func(b []byte) { h.record(s, b) })
}

func (h *ompHandle) record(s *session, b []byte) {
	var r ompRecord
	if json.Unmarshal(b, &r) != nil {
		return
	}
	t := parseTime(r.Timestamp, s.Updated)
	switch r.Type {
	case "session":
		if r.Cwd != "" {
			s.Cwd = r.Cwd
		}
	case "title", "title_change":
		if r.Title != "" && r.Title != s.Title {
			s.Title = r.Title
			s.dirty = true
		}
	case "custom_message":
		if r.CustomType == "async-result" {
			h.asyncResult(s, t, r.Content, decodeArgs(r.Details))
		}
	case "message":
		if r.Message != nil {
			h.message(s, t, r.Message)
		}
	}
}

func (h *ompHandle) message(s *session, t time.Time, m *ompMsg) {
	var blocks []ompBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return
	}
	switch m.Role {
	case "user":
		for _, b := range blocks {
			if b.Type == "text" {
				h.prompt(s, t, b.Text, m.Attribution)
			}
		}
	case "assistant":
		for _, b := range blocks {
			switch b.Type {
			case "text":
				s.say(t, b.Text)
			case "toolCall":
				h.tool(s, t, &b)
			default:
				// Thinking carries no activity of its own.
				s.touch(t)
			}
		}
		if m.Snapshot != nil && m.Snapshot.Prompt > 0 {
			s.Ctx = m.Snapshot.Prompt
		}
		if m.StopReason != "" && m.StopReason != "toolUse" && s.pending() < 0 {
			s.endTurn(t, "")
		}
	case "toolResult":
		h.result(s, t, m)
	}
}

// prompt records a prompt from the user. omp hands a subagent its brief as a
// message from the agent; that brief labels the subagent and is not an event.
func (h *ompHandle) prompt(s *session, t time.Time, text, attribution string) {
	if attribution == "agent" {
		if s.Prompt == "" {
			s.Prompt = oneLine(text, 120)
			s.touch(t)
		}
		return
	}
	s.prompt(t, text)
}

// ompToolName maps a tool name to the name that toolEvents knows.
func ompToolName(name string) string {
	switch name {
	case "todo":
		return "todo_write"
	case "ask":
		return "ask_user"
	}
	return name
}

func (h *ompHandle) tool(s *session, t time.Time, b *ompBlock) {
	in := decodeArgs(b.Arguments)
	evs := toolEvents(ompToolName(b.Name), in, s.Cwd, toolStyle{})
	intent := in.str("i")
	for i := range evs {
		evs[i].Tool = b.Name
		evs[i].CallID = b.ID
		switch evs[i].Kind {
		case KindTool:
			if intent != "" {
				evs[i].Text = intent
			}
		case KindTask:
			if evs[i].Text == "" {
				evs[i].Text = intent
			}
		}
	}
	s.add(t, evs...)
}

var planDoneRe = regexp.MustCompile(`Overall: (\d+)/(\d+) done`)

func (h *ompHandle) result(s *session, t time.Time, m *ompMsg) {
	idx := s.call(m.ToolCallID)
	if len(idx) == 0 {
		return
	}
	det := decodeArgs(m.Details)
	text := ompText(m.Content)
	for _, i := range idx {
		e := &s.Events[i]
		if e.Result == Pending {
			e.Result = OK
			if m.IsError {
				e.Result = Failed
			}
		}
		switch e.Kind {
		case KindRun:
			job := ""
			if a, ok := det["async"].(map[string]any); ok {
				job = args(a).str("jobId")
			}
			if job != "" {
				// The command runs on in the background. Its output comes
				// later, in the async-result notice.
				h.jobs[job] = m.ToolCallID
				e.Result = Pending
				continue
			}
			exit, ok := det.num("exitCode")
			if !ok {
				exit = -1
			}
			runResult(e, text, exit, m.IsError)
		case KindRead:
			if e.EndLine < 0 {
				if n, ok := det.num("totalLines"); ok && n > 0 {
					e.EndLine = n - 1
				}
			}
		case KindEdit:
			if line, ok := det.num("firstChangedLine"); ok && line > 0 {
				e.Line = line - 1
				e.EndLine = e.Line + max(0, e.Add-1)
			}
		case KindGrep:
			if n, ok := det.num("matchCount", "fileCount"); ok {
				e.Hits = n
			}
		case KindPlan:
			if sub := planDoneRe.FindStringSubmatch(text); sub != nil {
				e.Text = sub[1] + "/" + sub[2] + " done"
			}
		}
	}
	s.touch(t)
}

// asyncResult fills the output of a command that omp moved to the background
// when the notice of the finished job arrives.
func (h *ompHandle) asyncResult(s *session, t time.Time, content string, det args) {
	jobs, _ := det["jobs"].([]any)
	for _, j := range jobs {
		m, _ := j.(map[string]any)
		id := args(m).str("jobId")
		call := h.jobs[id]
		if call == "" {
			continue
		}
		delete(h.jobs, id)
		for _, i := range s.call(call) {
			if e := &s.Events[i]; e.Kind == KindRun {
				runResult(e, asyncOutput(content), -1, false)
			}
		}
	}
	s.touch(t)
}

// asyncOutput returns the output of a finished background job, without the
// notice that omp wraps around it.
func asyncOutput(content string) string {
	s := strings.TrimPrefix(strings.TrimSpace(content), "<system-notice>")
	if i := strings.IndexByte(s, '\n'); i >= 0 && strings.Contains(s[:i], "has completed") {
		s = s[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "</system-notice>"))
}

// ompText returns the text blocks of a tool result.
func ompText(raw json.RawMessage) string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, x := range blocks {
		if x.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(x.Text)
	}
	return b.String()
}
