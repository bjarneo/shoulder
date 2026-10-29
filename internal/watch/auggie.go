package watch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Auggie, the Augment CLI, saves each session to ~/.augment/sessions/<id>.json.
// It rewrites the whole file after each exchange. An exchange is one model
// response with its tool calls. The results of the calls come in the request
// nodes of the next exchange. Tool paths are relative to the workspace root,
// which an IDE state node in the requests gives. A subagent runs inside the
// sub-agent tool call and has no session file of its own.
type auggie struct {
	dir    string
	files  logCache // sessions of other projects
	walked time.Time
}

func newAuggie() *auggie {
	return &auggie{dir: filepath.Join(home(), ".augment", "sessions"), files: logCache{}}
}

func (a *auggie) close() {}

func (a *auggie) scan(w *Watcher, cutoff time.Time) {
	walk := walkDue(&a.walked)
	files, _ := filepath.Glob(filepath.Join(a.dir, "*.json"))
	for _, f := range files {
		key := "auggie:" + f
		if w.has(key) {
			continue
		}
		info, away := a.files[f]
		if away && !walk {
			// The session belongs to another project. Only a walk updates
			// its time.
			continue
		}
		st, err := os.Stat(f)
		if err != nil || st.ModTime().Before(cutoff) {
			continue
		}
		if away {
			info.mod = st.ModTime()
			a.files[f] = info
			continue
		}
		if w.parkedLog(key, st.ModTime()) {
			continue
		}
		sess, err := readAugSession(f)
		if err != nil {
			continue
		}
		root := sess.root()
		switch {
		case root == "":
			// No IDE state node yet. A later rewrite of the file has one.
			continue
		case !w.inRoot(root):
			a.files[f] = logInfo{cwd: root, away: true, mod: st.ModTime()}
			continue
		}
		s := newSession(key, "auggie", &auggieHandle{path: f})
		s.Cwd = root
		w.add(s, st.ModTime())
	}
	w.away["auggie"] = a.files.count(cutoff, nil)
}

type augSession struct {
	Title       string    `json:"title"`
	CustomTitle string    `json:"customTitle"`
	ChatHistory []augItem `json:"chatHistory"`
}

type augItem struct {
	Completed  bool   `json:"completed"`
	FinishedAt string `json:"finishedAt"`
	Exchange   struct {
		RequestMessage string    `json:"request_message"`
		ResponseText   string    `json:"response_text"`
		RequestNodes   []augNode `json:"request_nodes"`
		ResponseNodes  []augNode `json:"response_nodes"`
	} `json:"exchange"`
}

type augNode struct {
	TimestampMs int64 `json:"timestamp_ms"`
	IdeState    *struct {
		Folders []struct {
			Root string `json:"repository_root"`
		} `json:"workspace_folders"`
		Terminal *struct {
			Cwd string `json:"current_working_directory"`
		} `json:"current_terminal"`
	} `json:"ide_state_node"`
	ToolUse *struct {
		ID        string `json:"tool_use_id"`
		Name      string `json:"tool_name"`
		Input     string `json:"input_json"`
		StartedMs int64  `json:"started_at_ms"`
	} `json:"tool_use"`
	ToolResult *augResult `json:"tool_result_node"`
	Tokens     *struct {
		Input       int `json:"input_tokens"`
		CacheRead   int `json:"cache_read_input_tokens"`
		CacheCreate int `json:"cache_creation_input_tokens"`
	} `json:"token_usage"`
}

type augResult struct {
	ID      string `json:"tool_use_id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

func readAugSession(path string) (*augSession, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s augSession
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// root returns the first workspace folder, or the terminal directory, that an
// IDE state node gives.
func (s *augSession) root() string {
	for _, it := range s.ChatHistory {
		for _, n := range it.Exchange.RequestNodes {
			switch {
			case n.IdeState == nil:
			case len(n.IdeState.Folders) > 0 && n.IdeState.Folders[0].Root != "":
				return filepath.Clean(n.IdeState.Folders[0].Root)
			case n.IdeState.Terminal != nil && n.IdeState.Terminal.Cwd != "":
				return filepath.Clean(n.IdeState.Terminal.Cwd)
			}
		}
	}
	return ""
}

type auggieHandle struct {
	path  string
	mtime time.Time
}

func (h *auggieHandle) poll(s *session) error {
	st, err := os.Stat(h.path)
	if err != nil {
		return err
	}
	s.seen(st.ModTime())
	if st.ModTime().Equal(h.mtime) {
		return nil
	}
	sess, err := readAugSession(h.path)
	if err != nil {
		// A rewrite in progress. The next poll reads the file again.
		return nil
	}
	h.mtime = st.ModTime()
	h.rebuild(s, sess, st.ModTime())
	return nil
}

const maxAugItems = 150

func (h *auggieHandle) rebuild(s *session, sess *augSession, mtime time.Time) {
	s.reset()
	s.Title = sess.CustomTitle
	if s.Title == "" {
		s.Title = sess.Title
	}
	items := sess.ChatHistory
	if len(items) > maxAugItems {
		items = items[len(items)-maxAugItems:]
	}
	results := map[string]*augResult{}
	for i := range items {
		for _, n := range items[i].Exchange.RequestNodes {
			if n.ToolResult != nil {
				results[n.ToolResult.ID] = n.ToolResult
			}
		}
	}
	for i := range items {
		ex := &items[i].Exchange
		t := parseTime(items[i].FinishedAt, mtime)
		s.prompt(t, ex.RequestMessage)
		s.say(t, ex.ResponseText)
		for _, n := range ex.ResponseNodes {
			if n.Tokens != nil {
				s.Ctx = n.Tokens.Input + n.Tokens.CacheRead + n.Tokens.CacheCreate
			}
			if n.ToolUse == nil {
				continue
			}
			tt := t
			switch {
			case n.ToolUse.StartedMs > 0:
				tt = time.UnixMilli(n.ToolUse.StartedMs)
			case n.TimestampMs > 0:
				tt = time.UnixMilli(n.TimestampMs)
			}
			evs := auggieEvents(n.ToolUse.Name, decodeArgs([]byte(n.ToolUse.Input)), s.Cwd)
			for j := range evs {
				evs[j].CallID = n.ToolUse.ID
				if r := results[n.ToolUse.ID]; r != nil {
					auggieResult(&evs[j], r)
				}
			}
			s.add(tt, evs...)
		}
	}
	// The last exchange ends the turn when it calls no tools.
	if n := len(items); n > 0 && items[n-1].Completed && !hasToolUse(&items[n-1]) {
		s.endTurn(parseTime(items[n-1].FinishedAt, mtime), "")
	}
}

func hasToolUse(it *augItem) bool {
	for _, n := range it.Exchange.ResponseNodes {
		if n.ToolUse != nil {
			return true
		}
	}
	return false
}

// auggieEvents converts one Auggie tool call to activity events. The events
// keep the Auggie tool name.
func auggieEvents(name string, in args, cwd string) []Event {
	evs := augToolEvents(name, in, cwd)
	for i := range evs {
		evs[i].Tool = name
	}
	return evs
}

// augToolEvents maps the Auggie tools to the tool names and arguments that
// toolEvents knows.
func augToolEvents(name string, in args, cwd string) []Event {
	switch name {
	case "view":
		if in.str("type") == "directory" {
			return toolEvents("ls", args{"path": in["path"]}, cwd, toolStyle{})
		}
		a := args{"path": in["path"]}
		if r, ok := in["view_range"].([]any); ok && len(r) == 2 {
			a["start_line"] = r[0]
			if end, _ := r[1].(float64); end > 0 {
				a["end_line"] = end
			}
		}
		return toolEvents("view", a, cwd, toolStyle{})
	case "save-file":
		return toolEvents("write", args{"path": in["path"], "content": in["file_content"]}, cwd, toolStyle{})
	case "str-replace-editor":
		var edits []any
		for i := 1; ; i++ {
			n := strconv.Itoa(i)
			newS, ok := in["new_str_"+n]
			if !ok {
				break
			}
			edits = append(edits, map[string]any{"old_string": in["old_str_"+n], "new_string": newS})
		}
		evs := toolEvents("multi_edit", args{"file_path": in["path"], "edits": edits}, cwd, toolStyle{})
		if line, ok := in.num("old_str_start_line_number_1", "insert_line_1"); ok {
			evs[0].Line = max(0, line-1)
		}
		return evs
	case "remove-files":
		files, _ := in["file_paths"].([]any)
		out := make([]Event, 0, len(files))
		for _, f := range files {
			p, _ := f.(string)
			out = append(out, Event{Kind: KindEdit, Tool: name, File: absPath(p, cwd), Deleted: true, Line: -1, EndLine: -1, Hits: -1})
		}
		return out
	case "launch-process":
		return toolEvents("bash", in, cwd, toolStyle{})
	case "grep-search":
		return toolEvents("grep", args{"query": in["query"], "path": in["directory_absolute_path"]}, cwd, toolStyle{})
	case "codebase-retrieval":
		return toolEvents("search", args{"query": in["information_request"]}, cwd, toolStyle{})
	case "web-fetch":
		return toolEvents("webfetch", in, cwd, toolStyle{})
	case "web-search":
		return toolEvents("websearch", in, cwd, toolStyle{})
	case "add_tasks", "update_tasks", "reorganize_tasklist", "view_tasklist":
		return toolEvents("update_plan", in, cwd, toolStyle{})
	}
	if strings.HasPrefix(name, "sub-agent") {
		return toolEvents("task", args{"description": in.str("name", "instruction")}, cwd, toolStyle{})
	}
	return toolEvents(name, in, cwd, toolStyle{})
}

var (
	augCodeRe   = regexp.MustCompile(`<return-code>\s*(-?\d+)\s*</return-code>`)
	augOutputRe = regexp.MustCompile(`(?s)<output>(.*?)</output>`)
)

func auggieResult(e *Event, r *augResult) {
	if e.Kind != KindRun {
		e.Result = OK
		if r.IsError {
			e.Result = Failed
		}
		return
	}
	if strings.HasPrefix(r.Content, "Command may still be running") {
		return
	}
	out, code := r.Content, -1
	if m := augOutputRe.FindStringSubmatch(r.Content); m != nil {
		out = strings.TrimSpace(m[1])
	}
	if m := augCodeRe.FindStringSubmatch(r.Content); m != nil {
		code, _ = strconv.Atoi(m[1])
	}
	runResult(e, out, code, r.IsError)
}
