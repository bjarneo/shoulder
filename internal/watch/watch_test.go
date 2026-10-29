package watch

import (
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
	}
	for _, c := range cases {
		e := shellEvent(Event{Line: -1, EndLine: -1}, c.cmd, "/w")
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
