package watch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Codex writes one JSONL rollout per thread to
// ~/.codex/sessions/YYYY/MM/DD/rollout-<time>-<id>.jsonl.
type codex struct{ dir string }

func newCodex() *codex {
	dir := os.Getenv("CODEX_HOME")
	if dir == "" {
		dir = filepath.Join(home(), ".codex")
	}
	return &codex{dir: filepath.Join(dir, "sessions")}
}

func (c *codex) close() {}

func (c *codex) scan(w *Watcher, cutoff time.Time) {
	now := time.Now()
	for d := cutoff.AddDate(0, 0, -1); !d.After(now.AddDate(0, 0, 1)); d = d.AddDate(0, 0, 1) {
		files, _ := filepath.Glob(filepath.Join(c.dir, d.Format("2006/01/02"), "rollout-*.jsonl"))
		for _, f := range files {
			key := "codex:" + f
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
			s := newSession(key, "codex", &codexHandle{t: tail{path: f}, procs: map[string]string{}, polls: map[string]string{}})
			s.Cwd = cwd
			s.CtxMax = 272_000
			w.add(s)
		}
	}
}

type codexHandle struct {
	t          tail
	lastPrompt string
	lastSay    string
	procs      map[string]string // exec session id → call id of the command
	polls      map[string]string // call id of write_stdin → exec session id
}

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexPayload struct {
	Type string `json:"type"`

	Cwd           string `json:"cwd"`
	AgentNickname string `json:"agent_nickname"`
	AgentRole     string `json:"agent_role"`

	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Input     string          `json:"input"`
	CallID    string          `json:"call_id"`
	Output    json.RawMessage `json:"output"`
	Action    *struct {
		Command          []string `json:"command"`
		WorkingDirectory string   `json:"working_directory"`
		Query            string   `json:"query"`
	} `json:"action"`

	Message          string `json:"message"`
	LastAgentMessage string `json:"last_agent_message"`
	Info             *struct {
		Last struct {
			Input int `json:"input_tokens"`
		} `json:"last_token_usage"`
		Window int `json:"model_context_window"`
	} `json:"info"`
	ExitCode         *int   `json:"exit_code"`
	AggregatedOutput string `json:"aggregated_output"`
	Stdout           string `json:"stdout"`
	Stderr           string `json:"stderr"`
	Success          *bool  `json:"success"`
}

func (h *codexHandle) poll(s *session) error {
	return h.t.lines(func(b []byte) { h.line(s, b) })
}

func (h *codexHandle) line(s *session, b []byte) {
	var l codexLine
	if json.Unmarshal(b, &l) != nil {
		return
	}
	var p codexPayload
	if json.Unmarshal(l.Payload, &p) != nil {
		return
	}
	t := parseTime(l.Timestamp, s.Updated)
	switch l.Type {
	case "session_meta":
		if p.Cwd != "" {
			s.Cwd = p.Cwd
		}
		if p.AgentRole != "" {
			s.Name = strings.ToLower(p.AgentRole)
		} else if p.AgentNickname != "" {
			s.Name = strings.ToLower(p.AgentNickname)
		}
		s.touch(t)
	case "turn_context":
		if p.Cwd != "" {
			s.Cwd = p.Cwd
		}
	case "response_item":
		h.item(s, t, &p)
	case "event_msg":
		h.event(s, t, &p)
	}
}

func (h *codexHandle) item(s *session, t time.Time, p *codexPayload) {
	switch p.Type {
	case "message":
		text := blockText(p.Content)
		switch p.Role {
		case "assistant":
			h.say(s, t, text)
		case "user":
			if !strings.HasPrefix(strings.TrimSpace(text), "<") && !strings.HasPrefix(text, "# AGENTS.md") {
				h.prompt(s, t, text)
			}
		}
	case "function_call":
		if p.Name == "write_stdin" {
			if sid, ok := decodeArgs([]byte(p.Arguments)).num("session_id"); ok {
				h.polls[p.CallID] = itoa(sid)
			}
			s.touch(t)
			return
		}
		h.call(s, t, p.CallID, toolEvents(p.Name, decodeArgs([]byte(p.Arguments)), s.Cwd, toolStyle{}))
	case "custom_tool_call":
		h.call(s, t, p.CallID, toolEvents(p.Name, args{"input": p.Input}, s.Cwd, toolStyle{}))
	case "local_shell_call":
		if p.Action == nil {
			return
		}
		cwd := s.Cwd
		if p.Action.WorkingDirectory != "" {
			cwd = p.Action.WorkingDirectory
		}
		h.call(s, t, p.CallID, shellEvents(Event{Tool: "local_shell", Line: -1, EndLine: -1, Hits: -1}, argvScript(p.Action.Command), cwd))
	case "web_search_call":
		q := ""
		if p.Action != nil {
			q = p.Action.Query
		}
		s.add(t, Event{Kind: KindWeb, Tool: "web_search", Text: q, Result: OK, Line: -1, EndLine: -1})
	case "function_call_output", "custom_tool_call_output":
		h.output(s, t, p.CallID, blockText(p.Output))
	default:
		s.touch(t)
	}
}

func (h *codexHandle) call(s *session, t time.Time, id string, evs []Event) {
	for i := range evs {
		evs[i].CallID = id
	}
	s.add(t, evs...)
}

var (
	runningRe = regexp.MustCompile(`Process running with session ID (\d+)`)
)

func (h *codexHandle) output(s *session, t time.Time, id, out string) {
	// The legacy shell tool wraps its output in JSON.
	exit := -1
	if strings.HasPrefix(out, "{") {
		var j struct {
			Output   string `json:"output"`
			Metadata struct {
				ExitCode *int `json:"exit_code"`
			} `json:"metadata"`
		}
		if json.Unmarshal([]byte(out), &j) == nil {
			out = j.Output
			if j.Metadata.ExitCode != nil {
				exit = *j.Metadata.ExitCode
			}
		}
	}
	if sid, ok := h.polls[id]; ok {
		// Output of write_stdin for a command that runs in the background.
		delete(h.polls, id)
		if ix := s.call(h.procs[sid]); len(ix) > 0 {
			// One event of a command keeps its text and its output.
			e := &s.Events[ix[0]]
			for _, i := range ix {
				if s.Events[i].Cmd != "" {
					e = &s.Events[i]
				}
			}
			e.Output = append(e.Output, outputLines(afterOutput(out))...)
			if exitRe.MatchString(out) {
				runResult(e, strings.Join(e.Output, "\n")+"\n"+out, -1, false)
				delete(h.procs, sid)
			}
		}
		s.touch(t)
		return
	}
	idx := s.call(id)
	for _, i := range idx {
		e := &s.Events[i]
		if e.Kind == KindRun || e.Kind == KindRead || e.Kind == KindGrep {
			if m := runningRe.FindStringSubmatch(out); m != nil && !exitRe.MatchString(out) {
				h.procs[m[1]] = id
				e.Output = outputLines(afterOutput(out))
				continue
			}
		}
		switch e.Kind {
		case KindRun:
			runResult(e, out, exit, false)
		case KindGrep:
			e.Hits = len(outputLines(afterOutput(out)))
			e.Result = OK
		default:
			e.Result = OK
			if exit > 0 || strings.HasPrefix(out, "apply_patch verification failed") || strings.HasPrefix(out, "Error") {
				e.Result = Failed
			}
		}
	}
	s.touch(t)
}

func afterOutput(out string) string {
	if i := strings.Index(out, "\nOutput:\n"); i >= 0 {
		return out[i+9:]
	}
	return out
}

func (h *codexHandle) event(s *session, t time.Time, p *codexPayload) {
	switch p.Type {
	case "user_message":
		h.prompt(s, t, p.Message)
	case "agent_message":
		h.say(s, t, p.Message)
	case "task_started", "turn_started":
		s.TurnEnded = false
		s.touch(t)
	case "task_complete", "turn_complete":
		s.endTurn(t, firstLine(p.LastAgentMessage))
	case "turn_aborted":
		s.endTurn(t, "interrupted")
	case "token_count":
		if p.Info != nil {
			s.Ctx = p.Info.Last.Input
			if p.Info.Window > 0 {
				s.CtxMax = p.Info.Window
			}
			s.dirty = true
		}
	case "exec_command_end":
		for _, i := range s.call(p.CallID) {
			if e := &s.Events[i]; e.Result == Pending {
				exit := -1
				if p.ExitCode != nil {
					exit = *p.ExitCode
				}
				out := p.AggregatedOutput
				if out == "" {
					out = strings.TrimRight(p.Stdout+"\n"+p.Stderr, "\n")
				}
				if e.Kind == KindRun {
					runResult(e, out, exit, false)
				} else {
					e.Result = OK
				}
			}
		}
		s.touch(t)
	case "patch_apply_end":
		for _, i := range s.call(p.CallID) {
			if e := &s.Events[i]; e.Result == Pending {
				e.Result = OK
				if p.Success != nil && !*p.Success {
					e.Result = Failed
				}
			}
		}
		s.touch(t)
	default:
		s.touch(t)
	}
}

// Codex can log the same prompt and reply as a response item and as an
// event. The handle keeps the last text of each to skip the copy.
func (h *codexHandle) prompt(s *session, t time.Time, text string) {
	text = strings.TrimSpace(text)
	if text == "" || text == h.lastPrompt {
		return
	}
	h.lastPrompt, h.lastSay = text, ""
	s.prompt(t, text)
}

func (h *codexHandle) say(s *session, t time.Time, text string) {
	text = strings.TrimSpace(text)
	if text == "" || text == h.lastSay {
		return
	}
	h.lastSay = text
	s.say(t, text)
}
