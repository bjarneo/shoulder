package watch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// pi writes one JSONL session per session to
// $PI_CODING_AGENT_DIR/sessions/, default ~/.pi/agent/sessions/. Each
// project gets a directory named after its path with every separator
// replaced by "-", and a session in a subdirectory extends that name.
type pi struct{ dir string }

func newPi() *pi {
	dir := os.Getenv("PI_CODING_AGENT_DIR")
	if dir == "" {
		dir = filepath.Join(home(), ".pi", "agent")
	}
	return &pi{dir: filepath.Join(dir, "sessions")}
}

func (p *pi) close() {}

func (p *pi) scan(w *Watcher, cutoff time.Time) {
	prefix := "-" + strings.ReplaceAll(w.opt.Root, "/", "-") + "-"
	dirs, _ := os.ReadDir(p.dir)
	for _, d := range dirs {
		name := d.Name()
		if !d.IsDir() || (name != prefix && !strings.HasPrefix(name, prefix)) {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(p.dir, name, "*.jsonl"))
		for _, f := range files {
			key := "pi:" + f
			if w.has(key) {
				continue
			}
			st, err := os.Stat(f)
			if err != nil || st.ModTime().Before(cutoff) {
				continue
			}
			head := headLine(f, 1<<20)
			m := cwdRe.FindSubmatch(head)
			if m == nil {
				continue
			}
			var cwd string
			if json.Unmarshal(append(append([]byte{'"'}, m[1]...), '"'), &cwd) != nil || !w.inRoot(cwd) {
				continue
			}
			s := newSession(key, "pi", &piHandle{t: tail{path: f}})
			s.Cwd = cwd
			w.add(s)
		}
	}
}

type piHandle struct{ t tail }

type piLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   *piMsg `json:"message"`
}

type piMsg struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	StopReason string          `json:"stopReason"`
	ToolCallID string          `json:"toolCallId"`
	IsError    bool            `json:"isError"`
	Usage      *struct {
		Input      int `json:"input"`
		CacheRead  int `json:"cacheRead"`
		CacheWrite int `json:"cacheWrite"`
	} `json:"usage"`
}

type piBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (h *piHandle) poll(s *session) error {
	return h.t.lines(func(b []byte) { h.line(s, b) })
}

func (h *piHandle) line(s *session, b []byte) {
	var l piLine
	if json.Unmarshal(b, &l) != nil {
		return
	}
	if l.Message == nil {
		return
	}
	t := parseTime(l.Timestamp, s.Updated)
	m := l.Message
	switch m.Role {
	case "user":
		h.user(s, t, m)
	case "assistant":
		h.assistant(s, t, m)
	case "toolResult":
		h.result(s, t, m)
	}
}

func (h *piHandle) user(s *session, t time.Time, m *piMsg) {
	var blocks []piBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			s.prompt(t, b.Text)
		}
	}
}

func (h *piHandle) assistant(s *session, t time.Time, m *piMsg) {
	var blocks []piBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			s.say(t, b.Text)
		case "toolCall":
			evs := toolEvents(b.Name, decodeArgs(b.Arguments), s.Cwd, toolStyle{})
			for i := range evs {
				evs[i].CallID = b.ID
			}
			s.add(t, evs...)
		default:
			s.touch(t)
		}
	}
	if u := m.Usage; u != nil {
		if ctx := u.Input + u.CacheRead + u.CacheWrite; ctx > 0 {
			s.Ctx = ctx
			if ctx > s.CtxMax {
				s.CtxMax = 1_000_000
			}
		}
	}
	switch m.StopReason {
	case "aborted":
		s.endTurn(t, "interrupted")
	case "stop", "length":
		s.endTurn(t, "")
	}
}

func (h *piHandle) result(s *session, t time.Time, m *piMsg) {
	idx := s.call(m.ToolCallID)
	if len(idx) == 0 {
		return
	}
	text := blockText(m.Content)
	for _, i := range idx {
		e := &s.Events[i]
		switch e.Kind {
		case KindRun:
			runResult(e, text, -1, m.IsError)
		case KindGrep:
			e.Hits = len(splitLines(text))
			e.Result = OK
		default:
			e.Result = OK
		}
		if m.IsError {
			e.Result = Failed
		}
	}
	s.touch(t)
}
