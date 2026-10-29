package watch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShellEvent(t *testing.T) {
	cases := []struct {
		cmd      string
		kind     Kind
		file     string
		from, to int
		query    string
	}{
		{"sed -n '10,40p' src/main.rs", KindRead, "/w/src/main.rs", 9, 39, ""},
		{"cd /w && sed -n 5p a.go", KindRead, "/w/a.go", 4, 4, ""},
		{"nl -ba lib.go | sed -n '1,80p'", KindRead, "/w/lib.go", 0, 79, ""},
		{"cat README.md", KindRead, "/w/README.md", 0, -1, ""},
		{"head -n 20 go.mod", KindRead, "/w/go.mod", 0, 19, ""},
		{`rg -n "fn main" src`, KindGrep, "", -1, -1, "fn main"},
		{"grep -rn -e TODO --include '*.go' .", KindGrep, "", -1, -1, "TODO"},
		{"cat a.go | grep x", KindRun, "", -1, -1, ""},
		{"go test ./... > out.txt", KindRun, "", -1, -1, ""},
		{"cargo build", KindRun, "", -1, -1, ""},
		{"sed -n 1,20p a.go 2>/dev/null | head -5", KindRead, "/w/a.go", 0, 19, ""},
		{`grep -n -E 'a|b;c' x.go`, KindGrep, "", -1, -1, "a|b;c"},
		{"cat $HOME/a.go", KindRun, "", -1, -1, ""},
		{"echo hi && go vet ./...", KindRun, "", -1, -1, ""},
	}
	for _, c := range cases {
		evs := shellEvents(Event{Line: -1, EndLine: -1}, c.cmd, "/w")
		if len(evs) != 1 {
			t.Errorf("%q: got %d events", c.cmd, len(evs))
			continue
		}
		e := evs[0]
		if e.Kind != c.kind || e.File != c.file || e.Query != c.query {
			t.Errorf("%q: got kind %s file %q query %q", c.cmd, e.Kind, e.File, e.Query)
			continue
		}
		if c.kind == KindRead && (e.Line != c.from || e.EndLine != c.to) {
			t.Errorf("%q: got lines %d-%d", c.cmd, e.Line, e.EndLine)
		}
		if e.Cmd == "" {
			t.Errorf("%q: command text is lost", c.cmd)
		}
	}
}

func TestShellEventsWithParts(t *testing.T) {
	type want struct {
		kind Kind
		file string
		cmd  bool
	}
	cases := []struct {
		cmd  string
		want []want
	}{
		{"cat flake.nix && cat go.mod && which nix; sed -n 395,425p internal/ui/view.go", []want{
			{KindRead, "/w/flake.nix", false}, {KindRead, "/w/go.mod", false}, {KindRead, "/w/internal/ui/view.go", false}, {KindRun, "", true},
		}},
		{"sed -n 40,156p repo.go && echo ---- && sed -n 1,20p diff.go", []want{
			{KindRead, "/w/repo.go", true}, {KindRead, "/w/diff.go", false},
		}},
		{"cd /x && sed -n 1,5p a.go && cd sub && cat b.go", []want{
			{KindRead, "/x/a.go", true}, {KindRead, "/x/sub/b.go", false},
		}},
		{"go build ./... && grep -rn TODO internal | head", []want{
			{KindGrep, "", false}, {KindRun, "", true},
		}},
		{"cat > a.go <<'EOF'\npackage a\nEOF", []want{{KindRun, "", true}}},
		{"echo 'a && b'", []want{{KindRun, "", true}}},
		{`sed -n "1,5p" 'a b.go'`, []want{{KindRead, "/w/a b.go", true}}},
		{"cat 'unclosed", []want{{KindRun, "", true}}},
	}
	for _, c := range cases {
		evs := shellEvents(Event{Line: -1, EndLine: -1}, c.cmd, "/w")
		if len(evs) != len(c.want) {
			t.Errorf("%q: got %d events, want %d", c.cmd, len(evs), len(c.want))
			continue
		}
		for i, w := range c.want {
			e := evs[i]
			if e.Kind != w.kind || e.File != w.file || (e.Cmd != "") != w.cmd {
				t.Errorf("%q event %d: got kind %s file %q cmd %q", c.cmd, i, e.Kind, e.File, e.Cmd)
			}
		}
	}
}

func TestParsePatch(t *testing.T) {
	files := parsePatch(`*** Begin Patch
*** Update File: src/a.go
@@ func a()
-	return 1
+	return 2
+	// two
*** Add File: src/b.go
+package b
*** Delete File: src/c.go
*** End Patch`)
	if len(files) != 3 {
		t.Fatalf("files: %+v", files)
	}
	if f := files[0]; f.path != "src/a.go" || f.add != 2 || f.del != 1 || f.needle != "\treturn 2" {
		t.Errorf("update: %+v", f)
	}
	if f := files[1]; !f.created || f.add != 1 {
		t.Errorf("add: %+v", f)
	}
	if f := files[2]; !f.deleted {
		t.Errorf("delete: %+v", f)
	}
}

func feed(t *testing.T, h interface{ line(*session, []byte) }, s *session, lines ...string) {
	t.Helper()
	for _, l := range lines {
		h.line(s, []byte(l))
	}
}

func TestClaudeTranscript(t *testing.T) {
	h := &claudeHandle{}
	s := newSession("claude:x", "claude", h)
	feed(t, h, s,
		`{"type":"user","cwd":"/w","timestamp":"2026-10-27T10:00:00Z","message":{"role":"user","content":"add a limiter"}}`,
		`{"type":"ai-title","aiTitle":"Rate limiter"}`,
		`{"type":"assistant","cwd":"/w","timestamp":"2026-10-27T10:00:05Z","message":{"content":[{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"/w/a.go","old_string":"x := 1","new_string":"x := 2\ny := 3"}}],"usage":{"input_tokens":10,"cache_read_input_tokens":5000}}}`,
		`{"type":"user","cwd":"/w","timestamp":"2026-10-27T10:00:06Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},"toolUseResult":{"structuredPatch":[{"oldStart":4,"newStart":4,"lines":[" a","-x := 1","+x := 2","+y := 3"]}]}}`,
		`{"type":"assistant","cwd":"/w","timestamp":"2026-10-27T10:00:07Z","message":{"content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"go test ./..."}}]}}`,
	)
	if s.Title != "Rate limiter" || s.Prompt != "add a limiter" {
		t.Fatalf("title %q prompt %q", s.Title, s.Prompt)
	}
	act := s.Activity(time.Date(2026, 10, 27, 10, 0, 8, 0, time.UTC))
	if act.Act != "running" {
		t.Fatalf("act: %+v", act)
	}
	feed(t, h, s,
		`{"type":"user","timestamp":"2026-10-27T10:00:09Z","message":{"content":[{"type":"tool_result","tool_use_id":"t2","is_error":true,"content":"Exit code 1\nFAIL x"}]},"toolUseResult":"Error"}`,
		`{"type":"assistant","timestamp":"2026-10-27T10:00:10Z","message":{"content":[{"type":"text","text":"The test fails."}],"stop_reason":"end_turn"}}`,
		`{"type":"system","subtype":"turn_duration","timestamp":"2026-10-27T10:00:10Z"}`,
	)
	var edit, run *Event
	for i := range s.Events {
		switch s.Events[i].Kind {
		case KindEdit:
			edit = &s.Events[i]
		case KindRun:
			run = &s.Events[i]
		}
	}
	if edit == nil || edit.Line != 4 || edit.EndLine != 5 || edit.Add != 2 || edit.Del != 1 {
		t.Fatalf("edit: %+v", edit)
	}
	if run == nil || run.Result != Failed || run.Exit != 1 || run.Summary() != "✗ exit 1" {
		t.Fatalf("run: %+v", run)
	}
	if !s.TurnEnded || s.Events[len(s.Events)-1].Kind != KindDone || s.Events[len(s.Events)-1].Text != "The test fails." {
		t.Fatalf("turn end: %+v", s.Events[len(s.Events)-1])
	}
	if s.Ctx != 5010 {
		t.Fatalf("ctx: %d", s.Ctx)
	}
}

func TestCodexRollout(t *testing.T) {
	h := &codexHandle{procs: map[string]string{}, polls: map[string]string{}}
	s := newSession("codex:x", "codex", h)
	feed(t, h, s,
		`{"timestamp":"2026-10-27T10:00:00Z","type":"session_meta","payload":{"id":"1","cwd":"/w"}}`,
		`{"timestamp":"2026-10-27T10:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"fix the build"}}`,
		`{"timestamp":"2026-10-27T10:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the build"}]}}`,
		`{"timestamp":"2026-10-27T10:00:02Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"sed -n '1,40p' main.go\"}","call_id":"c1"}}`,
		`{"timestamp":"2026-10-27T10:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"Chunk ID: a\nProcess exited with code 0\nOutput:\npackage main"}}`,
		`{"timestamp":"2026-10-27T10:00:04Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"c2","input":"*** Begin Patch\n*** Update File: main.go\n@@\n-a\n+b\n*** End Patch"}}`,
		`{"timestamp":"2026-10-27T10:00:05Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c2","output":"Success."}}`,
		`{"timestamp":"2026-10-27T10:00:06Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"go test ./...\"}","call_id":"c3"}}`,
		`{"timestamp":"2026-10-27T10:00:07Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c3","output":"Process running with session ID 7\nOutput:\n=== RUN"}}`,
		`{"timestamp":"2026-10-27T10:00:08Z","type":"response_item","payload":{"type":"function_call","name":"write_stdin","arguments":"{\"session_id\":7,\"chars\":\"\"}","call_id":"c4"}}`,
		`{"timestamp":"2026-10-27T10:00:09Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c4","output":"Process exited with code 1\nOutput:\nFAIL"}}`,
		`{"timestamp":"2026-10-27T10:00:10Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":12000},"model_context_window":258400}}}`,
		`{"timestamp":"2026-10-27T10:00:11Z","type":"event_msg","payload":{"type":"task_complete","last_agent_message":"The build is fixed."}}`,
	)
	kinds := []Kind{}
	for _, e := range s.Events {
		kinds = append(kinds, e.Kind)
	}
	want := []Kind{KindYou, KindRead, KindEdit, KindRun, KindDone}
	if strings.Join(kindStrings(kinds), ",") != strings.Join(kindStrings(want), ",") {
		t.Fatalf("kinds: %v", kinds)
	}
	if e := s.Events[1]; e.File != "/w/main.go" || e.EndLine != 39 {
		t.Fatalf("read: %+v", e)
	}
	if e := s.Events[2]; e.File != "/w/main.go" || e.Add != 1 || e.Del != 1 || e.Result != OK {
		t.Fatalf("patch: %+v", e)
	}
	if e := s.Events[3]; e.Result != Failed || e.Exit != 1 {
		t.Fatalf("background run: %+v", e)
	}
	if s.CtxMax != 258400 || s.Ctx != 12000 || !s.TurnEnded {
		t.Fatalf("ctx %d/%d ended %v", s.Ctx, s.CtxMax, s.TurnEnded)
	}
}

func kindStrings(k []Kind) []string {
	out := make([]string, len(k))
	for i, x := range k {
		out[i] = string(x)
	}
	return out
}

func TestGeminiRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-2026-10-27T10-00-abcd1234.jsonl")
	lines := []string{
		`{"sessionId":"abcd1234","projectHash":"x","startTime":"2026-10-27T10:00:00Z","kind":"main"}`,
		`{"id":"u1","timestamp":"2026-10-27T10:00:00Z","type":"user","content":[{"text":"read the config"}]}`,
		`{"id":"g1","timestamp":"2026-10-27T10:00:01Z","type":"gemini","content":"","toolCalls":[{"id":"tc1","name":"read_file","args":{"file_path":"/w/config.toml","offset":10,"limit":5},"status":"executing","timestamp":"2026-10-27T10:00:01Z"}]}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &geminiHandle{path: path, t: tail{path: path}, msgs: map[string]*gemMsg{}}
	s := newSession("gemini:x", "gemini", h)
	s.Cwd = "/w"
	if err := h.poll(s); err != nil {
		t.Fatal(err)
	}
	if len(s.Events) != 2 || s.Events[1].Kind != KindRead || s.Events[1].Line != 10 || s.Events[1].EndLine != 14 || s.Events[1].Result != Pending {
		t.Fatalf("events: %+v", s.Events)
	}
	more := []string{
		`{"id":"g1","timestamp":"2026-10-27T10:00:01Z","type":"gemini","content":"","toolCalls":[{"id":"tc1","name":"read_file","args":{"file_path":"/w/config.toml"},"status":"success","timestamp":"2026-10-27T10:00:01Z"},{"id":"tc2","name":"run_shell_command","args":{"command":"make"},"status":"awaiting_approval","timestamp":"2026-10-27T10:00:02Z"}]}`,
	}
	appendLines(t, path, more)
	if err := h.poll(s); err != nil {
		t.Fatal(err)
	}
	act := s.Activity(time.Date(2026, 10, 27, 10, 0, 3, 0, time.UTC))
	if act.Status != Waiting || !strings.Contains(act.Detail, "approve") {
		t.Fatalf("approval: %+v", act)
	}
	appendLines(t, path, []string{`{"$rewindTo":"g1"}`})
	if err := h.poll(s); err != nil {
		t.Fatal(err)
	}
	if len(s.Events) != 1 || s.Events[0].Kind != KindYou {
		t.Fatalf("after rewind: %+v", s.Events)
	}
}

func appendLines(t *testing.T, path string, lines []string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
}

func TestOpencodeMessages(t *testing.T) {
	h := &opencodeHandle{msgs: map[string]*ocMsg{
		"m1": {seq: 1, typ: "user", created: 1000, data: `{"text":"rename the flag"}`},
		"m2": {seq: 2, typ: "assistant", created: 2000, data: `{"content":[
			{"type":"tool","id":"c1","name":"read","state":{"status":"completed","input":{"path":"/w/main.go","offset":3,"limit":10}}},
			{"type":"tool","id":"c2","name":"shell","state":{"status":"completed","input":{"command":"go vet ./..."},"metadata":{"exit":2,"output":"vet: bad"}}},
			{"type":"tool","id":"c3","name":"question","state":{"status":"running","input":{"questions":[{"question":"Keep the old name?"}]}}}
		],"tokens":{"input":5,"cache":{"read":300000,"write":10}}}`},
	}}
	s := newSession("opencode:x", "opencode", h)
	s.Cwd = "/w"
	h.rebuild(s)
	if len(s.Events) != 4 {
		t.Fatalf("events: %+v", s.Events)
	}
	if e := s.Events[1]; e.Line != 2 || e.EndLine != 11 {
		t.Fatalf("read: %+v", e)
	}
	if e := s.Events[2]; e.Result != Failed || e.Exit != 2 {
		t.Fatalf("shell: %+v", e)
	}
	if act := s.Activity(time.UnixMilli(3000)); act.Status != Waiting {
		t.Fatalf("question: %+v", act)
	}
	if s.CtxMax != 400_000 {
		t.Fatalf("ctx max: %d", s.CtxMax)
	}
	h.msgs["m3"] = &ocMsg{seq: 3, typ: "idle", created: 4000, data: `{"outcome":"succeeded"}`}
	h.rebuild(s)
	if !s.TurnEnded {
		t.Fatal("idle message did not end the turn")
	}
}

func TestOmpSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	proj := filepath.Join(dir, "sessions", "--w--")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(proj, "2026-10-27T10-00-00-000Z_ab.jsonl")
	lines := []string{
		`{"type":"title","v":1,"title":"Fix the build","source":"auto"}`,
		`{"type":"session","version":3,"id":"ab","timestamp":"2026-10-27T10:00:00.000Z","cwd":"/w"}`,
		`{"type":"message","id":"m1","parentId":null,"timestamp":"2026-10-27T10:00:00.000Z","message":{"role":"user","content":[{"type":"text","text":"fix the build"}],"attribution":"user"}}`,
		`{"type":"message","id":"m2","parentId":"m1","timestamp":"2026-10-27T10:00:01.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"look at the tests"},{"type":"text","text":"I will run the tests."},{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"go test ./...","i":"Running the tests"}}],"stopReason":"toolUse"}}`,
		`{"type":"message","id":"m3","parentId":"m2","timestamp":"2026-10-27T10:00:02.000Z","message":{"role":"toolResult","toolCallId":"c1","toolName":"bash","content":[{"type":"text","text":"--- FAIL: TestX"}],"details":{"exitCode":1,"wallTimeMs":12},"isError":true}}`,
		`{"type":"message","id":"m4","parentId":"m3","timestamp":"2026-10-27T10:00:03.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c2","name":"read","arguments":{"path":"/w/main.go","offset":10,"limit":5}}],"stopReason":"toolUse"}}`,
		`{"type":"message","id":"m5","parentId":"m4","timestamp":"2026-10-27T10:00:04.000Z","message":{"role":"toolResult","toolCallId":"c2","toolName":"read","content":[{"type":"text","text":"10:package main"}],"details":{"totalLines":40},"isError":false}}`,
		`{"type":"message","id":"m6","parentId":"m5","timestamp":"2026-10-27T10:00:05.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c3","name":"edit","arguments":{"path":"/w/main.go","old_string":"x := 1","new_string":"x := 2\n y := 3"}}],"stopReason":"toolUse"}}`,
		`{"type":"message","id":"m7","parentId":"m6","timestamp":"2026-10-27T10:00:06.000Z","message":{"role":"toolResult","toolCallId":"c3","toolName":"edit","content":[{"type":"text","text":"ok"}],"details":{"path":"/w/main.go","firstChangedLine":4,"op":"update"},"isError":false}}`,
		`{"type":"message","id":"m8","parentId":"m7","timestamp":"2026-10-27T10:00:07.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c4","name":"hub","arguments":{"op":"find","i":"Looking for a daemon"}}],"stopReason":"toolUse"}}`,
		`{"type":"message","id":"m9","parentId":"m8","timestamp":"2026-10-27T10:00:08.000Z","message":{"role":"toolResult","toolCallId":"c4","toolName":"hub","content":[{"type":"text","text":"Tool hub not found"}],"details":{"isError":true},"isError":true}}`,
		`{"type":"message","id":"m10","parentId":"m9","timestamp":"2026-10-27T10:00:09.000Z","message":{"role":"assistant","content":[{"type":"text","text":"The build is fixed."}],"stopReason":"stop","contextSnapshot":{"promptTokens":12345}}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := New(Options{Root: "/w", Since: time.Hour})
	newOmp().scan(w, time.Now().Add(-time.Hour))
	s := w.sessions["omp:"+path]
	if s == nil {
		t.Fatal("session not found")
	}
	if err := s.h.poll(s); err != nil {
		t.Fatal(err)
	}
	if s.Title != "Fix the build" || s.Prompt != "fix the build" {
		t.Fatalf("title %q prompt %q", s.Title, s.Prompt)
	}
	var kinds []Kind
	for _, e := range s.Events {
		kinds = append(kinds, e.Kind)
	}
	want := []Kind{KindYou, KindSay, KindRun, KindRead, KindEdit, KindTool, KindSay, KindDone}
	if strings.Join(kindStrings(kinds), ",") != strings.Join(kindStrings(want), ",") {
		t.Fatalf("kinds: %v", kinds)
	}
	if e := s.Events[2]; e.Cmd != "go test ./..." || e.Result != Failed || e.Exit != 1 {
		t.Fatalf("run: %+v", e)
	}
	if e := s.Events[3]; e.File != "/w/main.go" || e.Line != 9 || e.EndLine != 13 {
		t.Fatalf("read: %+v", e)
	}
	if e := s.Events[4]; e.File != "/w/main.go" || e.Needle != "x := 2\n y := 3" || e.Line != 3 || e.Add != 2 || e.Del != 1 {
		t.Fatalf("edit: %+v", e)
	}
	if e := s.Events[5]; e.Tool != "hub" || e.Text != "Looking for a daemon" || e.Result != Failed {
		t.Fatalf("unknown tool: %+v", e)
	}
	if s.Ctx != 12345 || !s.TurnEnded {
		t.Fatalf("ctx %d ended %v", s.Ctx, s.TurnEnded)
	}
	appendLines(t, path, []string{
		`{"type":"message","id":"m11","parentId":"m10","timestamp":"2026-10-27T10:00:10.000Z","message":{"role":"user","content":[{"type":"text","text":"also fix the docs"}],"attribution":"user"}}`,
	})
	if err := s.h.poll(s); err != nil {
		t.Fatal(err)
	}
	if len(s.Events) != len(want)+1 {
		t.Fatalf("events after one appended line: %+v", s.Events)
	}
	if e := s.Events[len(s.Events)-1]; e.Kind != KindYou || e.Text != "also fix the docs" {
		t.Fatalf("appended prompt: %+v", e)
	}
}

func TestTailSkipsPartialLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	if err := os.WriteFile(path, []byte("one\ntw"), 0o644); err != nil {
		t.Fatal(err)
	}
	tl := tail{path: path}
	var got []string
	collect := func(b []byte) { got = append(got, string(b)) }
	if err := tl.lines(collect); err != nil {
		t.Fatal(err)
	}
	appendLines(t, path, []string{"o", "three"})
	if err := tl.lines(collect); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "one,two,three" {
		t.Fatalf("lines: %v", got)
	}
}

func TestWatchAllProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, v := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "GEMINI_CLI_HOME", "XDG_DATA_HOME", "PI_CODING_AGENT_DIR", "PI_CODING_AGENT_SESSION_DIR"} {
		t.Setenv(v, "")
	}
	rootA, rootB := filepath.Join(home, "Code", "a"), filepath.Join(home, "Code", "b")
	for _, root := range []string{rootA, rootB} {
		dir := filepath.Join(home, ".claude", "projects", nonAlnum.ReplaceAllString(root, "-"))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Format(time.RFC3339)
		line := `{"type":"user","cwd":"` + root + `","timestamp":"` + now + `","message":{"role":"user","content":"work in ` + filepath.Base(root) + `"}}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w := New(Options{Root: rootA, Since: time.Hour})
	snaps := make(chan Snapshot, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx, func(s Snapshot) { snaps <- s })
	wait := func(n int) {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for {
			select {
			case s := <-snaps:
				if len(s.Agents) == n {
					return
				}
			case <-deadline:
				t.Fatalf("no snapshot with %d agents", n)
			}
		}
	}
	wait(1)
	w.SetAll(true)
	wait(2)
	w.SetAll(false)
	wait(1)
}

func TestLongCommandWithReadsIsRunning(t *testing.T) {
	start := time.Now().Add(-time.Minute)
	evs := shellEvents(Event{Tool: "Bash", CallID: "c1", Line: -1, EndLine: -1}, "go build ./... && sed -n 1,20p a.go", "/w")
	for i := range evs {
		evs[i].Time = start
	}
	a := Agent{Events: evs, Updated: start}
	if act := a.Activity(time.Now()); act.Status != Active || act.Act != "running" {
		t.Fatalf("got %+v", act)
	}
}

func TestAuggieSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".augment", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ide := func(root string) string {
		return `{"id":2,"type":4,"ide_state_node":{"workspace_folders":[{"repository_root":"` + root + `","folder_root":"` + root + `"}]}}`
	}
	first := `{"completed":true,"finishedAt":"2026-10-29T10:00:01.000Z","exchange":{"request_message":"fix add","response_text":"","request_nodes":[{"id":1,"type":0,"text_node":{"content":"fix add"}},` + ide("/w") + `],"response_nodes":[` +
		`{"id":1,"type":5,"tool_use":{"tool_use_id":"t1","tool_name":"view","input_json":"{\"path\":\"calc.go\",\"view_range\":[3,5]}","started_at_ms":1793268001000}},` +
		`{"id":2,"type":5,"tool_use":{"tool_use_id":"t2","tool_name":"str-replace-editor","input_json":"{\"command\":\"str_replace\",\"path\":\"calc.go\",\"old_str_1\":\"a - b\",\"new_str_1\":\"a + b\",\"old_str_start_line_number_1\":4}"}},` +
		`{"id":3,"type":5,"tool_use":{"tool_use_id":"t3","tool_name":"launch-process","input_json":"{\"command\":\"go test ./...\",\"cwd\":\"/w\",\"wait\":true}"}},` +
		`{"id":4,"type":10,"token_usage":{"input_tokens":4,"cache_read_input_tokens":20000,"cache_creation_input_tokens":300}}]}}`
	second := `{"completed":true,"finishedAt":"2026-10-29T10:00:05.000Z","exchange":{"request_message":"","response_text":"The test fails.","request_nodes":[` + ide("/w") + `,` +
		`{"id":1,"type":1,"tool_result_node":{"tool_use_id":"t1","content":"package main","is_error":false}},` +
		`{"id":2,"type":1,"tool_result_node":{"tool_use_id":"t2","content":"Successfully edited the file calc.go.","is_error":false}},` +
		`{"id":3,"type":1,"tool_result_node":{"tool_use_id":"t3","content":"Here are the results from executing the command.\n<return-code>\n1\n</return-code>\n<output>\nFAIL calc\n</output>","is_error":true}}],` +
		`"response_nodes":[{"id":0,"type":0,"content":"The test fails."}]}}`
	write := func(name string, items ...string) string {
		t.Helper()
		path := filepath.Join(dir, name+".json")
		body := `{"sessionId":"` + name + `","title":"Fix add","chatHistory":[` + strings.Join(items, ",") + `]}`
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	path := write("a1", first)
	other := strings.ReplaceAll(first, `"/w"`, `"/x"`)
	write("b2", other)

	w := New(Options{Root: "/w", Since: time.Hour})
	src := newAuggie()
	src.scan(w, time.Now().Add(-time.Hour))
	if len(w.sessions) != 1 {
		t.Fatalf("sessions: %v", w.sessions)
	}
	s := w.sessions["auggie:"+path]
	if s == nil || s.Cwd != "/w" {
		t.Fatalf("session: %+v", s)
	}
	if err := s.h.poll(s); err != nil {
		t.Fatal(err)
	}
	if s.Title != "Fix add" || s.Prompt != "fix add" || s.Ctx != 20304 {
		t.Fatalf("title %q prompt %q ctx %d", s.Title, s.Prompt, s.Ctx)
	}
	if e := s.Events[1]; e.Kind != KindRead || e.File != "/w/calc.go" || e.Line != 2 || e.EndLine != 4 || !e.Time.Equal(time.UnixMilli(1793268001000)) {
		t.Fatalf("view: %+v", e)
	}
	if e := s.Events[2]; e.Kind != KindEdit || e.Tool != "str-replace-editor" || e.Add != 1 || e.Del != 1 || e.Line != 3 || e.Needle != "a + b" {
		t.Fatalf("edit: %+v", e)
	}
	if e := s.Events[3]; e.Kind != KindRun || e.Cmd != "go test ./..." || e.Result != Pending {
		t.Fatalf("run: %+v", e)
	}
	if act := s.Activity(time.Date(2026, 10, 29, 10, 0, 2, 0, time.UTC)); act.Status != Active || act.Act != "running" {
		t.Fatalf("act: %+v", act)
	}

	write("a1", first, second)
	if err := s.h.poll(s); err != nil {
		t.Fatal(err)
	}
	var kinds []Kind
	for _, e := range s.Events {
		kinds = append(kinds, e.Kind)
	}
	want := []Kind{KindYou, KindRead, KindEdit, KindRun, KindSay, KindDone}
	if strings.Join(kindStrings(kinds), ",") != strings.Join(kindStrings(want), ",") {
		t.Fatalf("kinds: %v", kinds)
	}
	if e := s.Events[3]; e.Result != Failed || e.Exit != 1 || e.Summary() != "✗ exit 1" || strings.Join(e.Output, "|") != "FAIL calc" {
		t.Fatalf("run result: %+v", e)
	}
	if !s.TurnEnded || s.Events[len(s.Events)-1].Text != "The test fails." {
		t.Fatalf("turn end: %+v", s.Events[len(s.Events)-1])
	}

	// A file that auggie is rewriting does not drop the events.
	if err := os.WriteFile(path, []byte(`{"sessionId":"a1","chatHis`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.h.poll(s); err != nil || len(s.Events) != len(want) {
		t.Fatalf("partial file: %v %+v", err, s.Events)
	}
}

func TestAuggieSubagent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s1.json")
	call := `{"completed":true,"finishedAt":"2026-10-29T10:00:01.000Z","exchange":{"request_message":"use a sub-agent to read calc.go","response_text":"","request_nodes":[{"id":1,"type":4,"ide_state_node":{"workspace_folders":[{"repository_root":"/w","folder_root":"/w"}]}}],"response_nodes":[` +
		`{"id":1,"type":5,"tool_use":{"tool_use_id":"t1","tool_name":"sub-agent-explore","input_json":"{\"action\":\"run\",\"name\":\"read-files\",\"instruction\":\"Read calc.go and report what it contains.\"}","started_at_ms":1793268001000}}]}}`
	reply := `{"completed":true,"finishedAt":"2026-10-29T10:00:30.000Z","exchange":{"request_message":"","response_text":"calc.go defines add.","request_nodes":[` +
		`{"id":1,"type":1,"tool_result_node":{"tool_use_id":"t1","content":"## Summary\n\ncalc.go defines add(a, b int) int.","is_error":false}}],` +
		`"response_nodes":[{"id":0,"type":0,"content":"calc.go defines add."}]}}`
	write := func(items ...string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(`{"sessionId":"s1","chatHistory":[`+strings.Join(items, ",")+`]}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(call)
	h := &auggieHandle{path: path}
	s := newSession("auggie:s1", "auggie", h)
	s.Cwd = "/w"
	if err := h.poll(s); err != nil {
		t.Fatal(err)
	}
	if e := s.Events[1]; e.Kind != KindTask || e.Tool != "sub-agent-explore" || e.Text != "read-files" || e.Result != Pending {
		t.Fatalf("task: %+v", e)
	}
	// The subagent works longer than the approval estimate, and stays active.
	if act := s.Activity(time.Date(2026, 10, 29, 10, 0, 25, 0, time.UTC)); act.Status != Active || act.Act != "delegating" {
		t.Fatalf("act: %+v", act)
	}
	write(call, reply)
	if err := h.poll(s); err != nil {
		t.Fatal(err)
	}
	var kinds []Kind
	for _, e := range s.Events {
		kinds = append(kinds, e.Kind)
	}
	want := []Kind{KindYou, KindTask, KindSay, KindDone}
	if strings.Join(kindStrings(kinds), ",") != strings.Join(kindStrings(want), ",") {
		t.Fatalf("kinds: %v", kinds)
	}
	if e := s.Events[1]; e.Result != OK || e.Summary() != "✓ ok" {
		t.Fatalf("task result: %+v", e)
	}
	if !s.TurnEnded || s.Events[3].Text != "calc.go defines add." {
		t.Fatalf("turn end: %+v", s.Events[3])
	}
}

func TestAuggieTools(t *testing.T) {
	cases := []struct {
		name, input string
		kind        Kind
		file        string
		from, to    int
		add, del    int
		text        string
	}{
		{"view", `{"path":"a.go"}`, KindRead, "/w/a.go", 0, -1, 0, 0, ""},
		{"view", `{"path":"a.go","view_range":[10,-1]}`, KindRead, "/w/a.go", 9, -1, 0, 0, ""},
		{"view", `{"path":"src","type":"directory"}`, KindList, "", -1, -1, 0, 0, "src"},
		{"save-file", `{"path":"new.md","file_content":"one\ntwo\n"}`, KindEdit, "/w/new.md", 0, 1, 2, 0, "write"},
		{"str-replace-editor", `{"command":"str_replace","path":"a.go","old_str_1":"a","new_str_1":"b\nc","old_str_2":"x\ny","new_str_2":"z"}`, KindEdit, "/w/a.go", -1, -1, 3, 3, ""},
		{"str-replace-editor", `{"command":"insert","path":"a.go","insert_line_1":7,"new_str_1":"// note"}`, KindEdit, "/w/a.go", 6, -1, 1, 0, ""},
		{"launch-process", `{"command":"cat notes.txt","cwd":"/w/sub","wait":true}`, KindRead, "/w/sub/notes.txt", 0, -1, 0, 0, ""},
		{"grep-search", `{"query":"func add","directory_absolute_path":"/w"}`, KindGrep, "", -1, -1, 0, 0, "/w"},
		{"codebase-retrieval", `{"information_request":"where is add defined"}`, KindGrep, "", -1, -1, 0, 0, ""},
		{"web-fetch", `{"url":"https://go.dev"}`, KindWeb, "", -1, -1, 0, 0, "https://go.dev"},
		{"web-search", `{"query":"bubbletea"}`, KindWeb, "", -1, -1, 0, 0, "bubbletea"},
		{"update_tasks", `{}`, KindPlan, "", -1, -1, 0, 0, "plan"},
		{"sub-agent-explore", `{"action":"run","name":"read-files","instruction":"Read calc.go."}`, KindTask, "", -1, -1, 0, 0, "read-files"},
		{"sub-agent-explore", `{"action":"run","instruction":"Read calc.go."}`, KindTask, "", -1, -1, 0, 0, "Read calc.go."},
		{"read-process", `{"terminal_id":3}`, KindTool, "", -1, -1, 0, 0, "read-process"},
	}
	for _, c := range cases {
		evs := auggieEvents(c.name, decodeArgs([]byte(c.input)), "/w")
		if len(evs) != 1 {
			t.Errorf("%s %s: got %d events", c.name, c.input, len(evs))
			continue
		}
		e := evs[0]
		if e.Kind != c.kind || e.File != c.file || e.Tool != c.name || e.Add != c.add || e.Del != c.del || e.Text != c.text {
			t.Errorf("%s %s: got %+v", c.name, c.input, e)
			continue
		}
		if e.File != "" && (e.Line != c.from || e.EndLine != c.to) {
			t.Errorf("%s %s: got lines %d-%d", c.name, c.input, e.Line, e.EndLine)
		}
	}
	evs := auggieEvents("remove-files", decodeArgs([]byte(`{"file_paths":["a.go","/w/b.go"]}`)), "/w")
	if len(evs) != 2 || evs[0].File != "/w/a.go" || !evs[0].Deleted || evs[1].File != "/w/b.go" {
		t.Fatalf("remove-files: %+v", evs)
	}
}

func TestAuggieResult(t *testing.T) {
	cases := []struct {
		content string
		isErr   bool
		result  Result
		exit    int
		out     string
	}{
		{"Here are the results from executing the command.\n<return-code>\n0\n</return-code>\n<output>\nok\n</output>", false, OK, 0, "ok"},
		{"Here are the results from executing the command. <return-code> 2 </return-code> <output> no such file </output>", true, Failed, 2, "no such file"},
		{"Command may still be running. You can use read-process to get more output. Terminal ID 4 Output so far: <output> building </output>", false, Pending, 0, ""},
		{"Process launched with terminal ID 5", false, OK, 0, "Process launched with terminal ID 5"},
		{"Failed to launch process: no shell", true, Failed, 1, "Failed to launch process: no shell"},
	}
	for _, c := range cases {
		e := Event{Kind: KindRun}
		auggieResult(&e, &augResult{Content: c.content, IsError: c.isErr})
		if e.Result != c.result || e.Exit != c.exit || strings.Join(e.Output, "|") != c.out {
			t.Errorf("%q: got result %d exit %d output %q", c.content, e.Result, e.Exit, e.Output)
		}
	}
	e := Event{Kind: KindEdit}
	auggieResult(&e, &augResult{Content: "Failed to edit", IsError: true})
	if e.Result != Failed {
		t.Fatalf("edit result: %+v", e)
	}
}
