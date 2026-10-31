package watch

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// args is the decoded input of a tool call.
type args map[string]any

func decodeArgs(raw []byte) args {
	var a args
	if json.Unmarshal(raw, &a) != nil {
		return args{}
	}
	return a
}

func (a args) str(keys ...string) string {
	for _, k := range keys {
		switch v := a[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case []any:
			// A command given as an argument vector.
			parts := make([]string, 0, len(v))
			for _, p := range v {
				if s, ok := p.(string); ok {
					parts = append(parts, s)
				}
			}
			if len(parts) > 0 {
				return argvScript(parts)
			}
		}
	}
	return ""
}

func (a args) num(keys ...string) (int, bool) {
	for _, k := range keys {
		switch v := a[k].(type) {
		case float64:
			return int(v), true
		case string:
			if n, err := strconv.Atoi(v); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// argvScript turns ["bash", "-lc", "script"] into "script".
func argvScript(argv []string) string {
	if len(argv) >= 3 {
		switch filepath.Base(argv[0]) {
		case "bash", "sh", "zsh", "fish":
			if argv[1] == "-c" || argv[1] == "-lc" || argv[1] == "-ic" {
				return argv[2]
			}
		}
	}
	return strings.Join(argv, " ")
}

// toolStyle selects the line numbering of read offsets.
type toolStyle struct {
	zeroBasedOffset bool
}

// toolEvents converts one tool call to activity events. Most calls give one
// event. A patch gives one event for each file.
func toolEvents(name string, in args, cwd string, st toolStyle) []Event {
	n := strings.ToLower(name)
	if i := strings.LastIndex(n, "__"); i >= 0 && strings.HasPrefix(n, "mcp__") {
		return []Event{{Kind: KindTool, Tool: name, Text: n[i+2:]}}
	}
	e := Event{Tool: name, Line: -1, EndLine: -1, Hits: -1}
	path := func(keys ...string) string { return absPath(in.str(keys...), cwd) }
	switch n {
	case "read", "read_file", "view", "view_file", "cat":
		e.Kind = KindRead
		e.File = path("file_path", "filePath", "absolute_path", "path")
		if s, ok := in.num("start_line"); ok {
			e.Line = max(0, s-1)
			if end, ok := in.num("end_line"); ok {
				e.EndLine = max(e.Line, end-1)
			}
			break
		}
		off, hasOff := in.num("offset")
		switch {
		case !hasOff:
			e.Line = 0
		case st.zeroBasedOffset:
			e.Line = max(0, off)
		default:
			e.Line = max(0, off-1)
		}
		if lim, ok := in.num("limit"); ok && lim > 0 {
			e.EndLine = e.Line + lim - 1
		}
	case "read_many_files":
		e.Kind = KindRead
		e.Text = in.str("include", "paths")
	case "write", "write_file", "create_file", "create":
		e.Kind = KindEdit
		e.File = path("file_path", "filePath", "absolute_path", "path")
		body := in.str("content", "contents", "file_text")
		e.Add = countLines(body)
		e.Line, e.EndLine = 0, max(0, e.Add-1)
		e.Needle = lastLines(body, 1)
		e.Text = "write"
	case "edit", "replace", "str_replace", "str_replace_editor", "search_replace", "edit_file":
		e.Kind = KindEdit
		e.File = path("file_path", "filePath", "absolute_path", "path")
		oldS, newS := in.str("old_string", "oldString", "old_str"), in.str("new_string", "newString", "new_str")
		e.Add, e.Del = changedLines(oldS, newS)
		e.Needle = newS
	case "multiedit", "multi_edit":
		e.Kind = KindEdit
		e.File = path("file_path", "filePath")
		if edits, ok := in["edits"].([]any); ok {
			for _, x := range edits {
				if m, ok := x.(map[string]any); ok {
					a, d := changedLines(args(m).str("old_string", "oldString"), args(m).str("new_string", "newString"))
					e.Add += a
					e.Del += d
					if e.Needle == "" {
						e.Needle = args(m).str("new_string", "newString")
					}
				}
			}
		}
	case "notebookedit", "notebook_edit":
		e.Kind = KindEdit
		e.File = path("notebook_path", "file_path")
		e.Needle = in.str("new_source")
		e.Add = countLines(e.Needle)
	case "apply_patch", "patch", "applypatch":
		text := in.str("patchText", "patch", "input", "patch_text")
		files := parsePatch(text)
		if len(files) == 0 {
			e.Kind = KindEdit
			e.Text = "patch"
			return []Event{e}
		}
		out := make([]Event, 0, len(files))
		for _, f := range files {
			x := e
			x.Kind = KindEdit
			x.File = absPath(f.path, cwd)
			x.Add, x.Del = f.add, f.del
			x.Created, x.Deleted = f.created, f.deleted
			x.Needle = f.needle
			out = append(out, x)
		}
		return out
	case "bash", "shell", "shell_command", "exec_command", "run_shell_command", "local_shell", "run_terminal_cmd", "terminal":
		cmd := in.str("command", "cmd", "script")
		wd := in.str("workdir", "dir_path", "directory", "cwd", "working_directory")
		if wd != "" {
			cwd = absPath(wd, cwd)
		}
		return shellEvents(e, cmd, cwd)
	case "grep", "search_file_content", "grep_search", "grep_files", "rg", "search":
		e.Kind = KindGrep
		e.Query = in.str("pattern", "query", "regex")
		e.Text = in.str("path", "dir_path", "include", "glob")
	case "glob", "find_files", "file_search":
		e.Kind = KindGrep
		e.Query = in.str("pattern", "glob", "query")
		e.Text = "files"
	case "ls", "list", "list_dir", "list_directory":
		e.Kind = KindList
		e.Text = shortPath(path("path", "dir_path", "directory"), cwd)
	case "webfetch", "web_fetch", "fetch":
		e.Kind = KindWeb
		e.Text = in.str("url", "prompt")
	case "websearch", "web_search", "google_web_search":
		e.Kind = KindWeb
		e.Text = in.str("query")
	case "task", "agent", "subagent", "spawn_agent", "delegate":
		e.Kind = KindTask
		e.Text = in.str("description", "agent", "subagent_type", "message", "prompt")
	case "todowrite", "write_todos", "update_plan", "todo_write":
		e.Kind = KindPlan
		e.Text = planText(in)
	case "askuserquestion", "question", "ask_user", "request_user_input", "ask_followup_question":
		e.Kind = KindAsk
		e.Text = questionText(in)
	case "exitplanmode", "exit_plan_mode":
		e.Kind = KindAsk
		e.Text = "plan ready"
	default:
		e.Kind = KindTool
		e.Text = name
		if s := in.str("description", "query", "url", "id", "name"); s != "" {
			e.Text += " " + s
		}
	}
	return []Event{e}
}

func planText(in args) string {
	for _, k := range []string{"todos", "plan"} {
		items, ok := in[k].([]any)
		if !ok {
			continue
		}
		done, current := 0, ""
		for _, it := range items {
			m, _ := it.(map[string]any)
			st, _ := m["status"].(string)
			switch st {
			case "completed", "done":
				done++
			case "in_progress", "in-progress":
				if current == "" {
					current = args(m).str("content", "step", "activeForm", "description")
				}
			}
		}
		s := itoa(done) + "/" + itoa(len(items)) + " done"
		if current != "" {
			s += " · " + current
		}
		return s
	}
	return "plan"
}

func questionText(in args) string {
	if qs, ok := in["questions"].([]any); ok && len(qs) > 0 {
		if m, ok := qs[0].(map[string]any); ok {
			return args(m).str("question", "header")
		}
	}
	return in.str("question", "prompt", "message")
}

func absPath(p, cwd string) string {
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "~/") {
		p = filepath.Join(home(), p[2:])
	}
	if !filepath.IsAbs(p) && cwd != "" {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

// changedLines counts the lines that an exact text replacement adds and
// removes, ignoring the lines that both sides share at the ends.
func changedLines(oldS, newS string) (add, del int) {
	a, b := splitLines(oldS), splitLines(newS)
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	j := 0
	for j < len(a)-i && j < len(b)-i && a[len(a)-1-j] == b[len(b)-1-j] {
		j++
	}
	return len(b) - i - j, len(a) - i - j
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func countLines(s string) int { return len(splitLines(s)) }

func lastLines(s string, n int) string {
	l := splitLines(s)
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

// patchFile is one file section of an apply_patch envelope.
type patchFile struct {
	path             string
	add, del         int
	created, deleted bool
	needle           string
}

// parsePatch reads the "*** Begin Patch" format that Codex and opencode use.
func parsePatch(text string) []patchFile {
	var files []patchFile
	var cur *patchFile
	for _, l := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(l, "*** Update File: "):
			files = append(files, patchFile{path: strings.TrimSpace(l[17:])})
			cur = &files[len(files)-1]
		case strings.HasPrefix(l, "*** Add File: "):
			files = append(files, patchFile{path: strings.TrimSpace(l[14:]), created: true})
			cur = &files[len(files)-1]
		case strings.HasPrefix(l, "*** Delete File: "):
			files = append(files, patchFile{path: strings.TrimSpace(l[17:]), deleted: true})
			cur = nil
		case strings.HasPrefix(l, "*** Move to: "):
			if cur != nil {
				cur.path = strings.TrimSpace(l[13:])
			}
		case strings.HasPrefix(l, "***"):
		case cur == nil:
		case strings.HasPrefix(l, "+"):
			cur.add++
			if cur.needle == "" && strings.TrimSpace(l[1:]) != "" {
				cur.needle = l[1:]
			}
		case strings.HasPrefix(l, "-"):
			cur.del++
		}
	}
	return files
}

func readEvent(e Event, file string, from, to int) Event {
	e.Kind, e.File, e.Line, e.EndLine = KindRead, file, from, to
	return e
}

var sedExpr = regexp.MustCompile(`^(\d+)(?:,(\d+))?p$`)

// sedRange finds the line range in "sed -n 'A,Bp'". It returns 0-based lines.
func sedRange(words []string) (from, to int, ok bool) {
	if len(words) < 3 || filepath.Base(words[0]) != "sed" || words[1] != "-n" {
		return 0, 0, false
	}
	m := sedExpr.FindStringSubmatch(words[2])
	if m == nil {
		return 0, 0, false
	}
	a, _ := strconv.Atoi(m[1])
	b := a
	if m[2] != "" {
		b, _ = strconv.Atoi(m[2])
	}
	return max(0, a-1), max(0, b-1), true
}

var grepValueFlags = map[string]bool{
	"-g": true, "--glob": true, "-t": true, "--type": true, "-T": true, "--type-not": true,
	"-A": true, "-B": true, "-C": true, "-m": true, "--max-count": true, "-e": true,
	"--include": true, "--exclude": true, "--exclude-dir": true, "-f": true, "--max-depth": true,
}

func grepArgs(words []string) (pat, where string) {
	var rest []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		if w == "-e" && i+1 < len(words) && pat == "" {
			pat = words[i+1]
			i++
			continue
		}
		if grepValueFlags[w] {
			i++
			continue
		}
		if strings.HasPrefix(w, "-") {
			continue
		}
		rest = append(rest, w)
	}
	if pat == "" && len(rest) > 0 {
		pat, rest = rest[0], rest[1:]
	}
	return pat, strings.Join(rest, " ")
}

func nonFlags(words []string) []string {
	var out []string
	for _, w := range words {
		if !strings.HasPrefix(w, "-") {
			out = append(out, w)
		}
	}
	return out
}

func lastNonFlag(words []string) string {
	nf := nonFlags(words)
	if len(nf) == 0 {
		return ""
	}
	return nf[len(nf)-1]
}

var (
	exitRe     = regexp.MustCompile(`(?i)(?:exit code:?|process exited with code|exited with code)\s*(-?\d+)`)
	warningRe  = regexp.MustCompile(`(?m)^\s*warning(?:\[|:| )`)
	maxOutLine = 200
)

// runResult fills the result fields of a shell event from its output text.
// exit is -1 when the source did not give an exit code.
func runResult(e *Event, out string, exit int, failed bool) {
	if exit < 0 {
		exit = exitCode(out, failed)
	}
	if i := strings.Index(out, "\nOutput:\n"); i >= 0 {
		out = out[i+9:]
	} else if strings.HasPrefix(out, "Output:\n") {
		out = out[8:]
	}
	finishRun(e, outputLines(out), exit)
}

// exitCode finds the exit code in the output text of a command. Without
// one, it returns 1 when failed is true and 0 when it is false.
func exitCode(out string, failed bool) int {
	if m := exitRe.FindStringSubmatch(out); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	if failed {
		return 1
	}
	return 0
}

// finishRun sets the exit code, the output lines and the result of a shell
// event.
func finishRun(e *Event, lines []string, exit int) {
	e.Exit = exit
	e.Output = lines
	switch {
	case exit != 0:
		e.Result = Failed
	case slices.ContainsFunc(lines, warningRe.MatchString):
		e.Result = Warned
	default:
		e.Result = OK
	}
}

// appendOutput returns a new slice with the lines of old and then the lines
// of add, clipped to the last maxOutLine lines. It does not change old,
// because a snapshot can share it.
func appendOutput(old, add []string) []string {
	out := make([]string, 0, len(old)+len(add))
	out = append(append(out, old...), add...)
	if len(out) > maxOutLine {
		out = out[len(out)-maxOutLine:]
	}
	return out
}

// Summary is the short result text of a finished command.
func (e *Event) Summary() string {
	switch e.Result {
	case Pending:
		return "…"
	case Failed:
		if e.Exit > 0 {
			return "✗ exit " + itoa(e.Exit)
		}
		return "✗ failed"
	case Warned:
		n := 0
		for _, l := range e.Output {
			if warningRe.MatchString(l) {
				n++
			}
		}
		if n == 1 {
			return "! 1 warning"
		}
		return "! " + itoa(n) + " warnings"
	}
	return "✓ ok"
}

func outputLines(s string) []string {
	s = strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), "\n ")
	if s == "" {
		return nil
	}
	l := strings.Split(s, "\n")
	if len(l) > maxOutLine {
		l = l[len(l)-maxOutLine:]
	}
	return l
}

func itoa(n int) string { return strconv.Itoa(n) }

// oneLine collapses whitespace and clips text for the activity log.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
