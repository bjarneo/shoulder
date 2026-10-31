package watch

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeHome makes an empty home directory for all sources.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, v := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "GEMINI_CLI_HOME", "XDG_DATA_HOME", "PI_CODING_AGENT_DIR", "PI_CODING_AGENT_SESSION_DIR"} {
		t.Setenv(v, "")
	}
	return home
}

// writeLog writes lines to a new file in dir, below home.
func writeLog(t *testing.T, path string, lines ...string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func claudePath(home, root, name string) string {
	return filepath.Join(home, ".claude", "projects", nonAlnum.ReplaceAllString(root, "-"), name)
}

func claudePrompt(cwd string, at time.Time, text string) string {
	return `{"type":"user","cwd":"` + cwd + `","timestamp":"` + at.UTC().Format(time.RFC3339) + `","message":{"role":"user","content":"` + text + `"}}`
}

func codexPath(home string, day time.Time, name string) string {
	return filepath.Join(home, ".codex", "sessions", day.Format("2006/01/02"), name)
}

func codexMeta(cwd string) string {
	return `{"timestamp":"2026-10-27T10:00:00Z","type":"session_meta","payload":{"id":"1","cwd":"` + cwd + `"}}`
}

func codexPrompt(at time.Time, text string) string {
	return `{"timestamp":"` + at.UTC().Format(time.RFC3339) + `","type":"event_msg","payload":{"type":"user_message","message":"` + text + `"}}`
}

func setTime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestPendingCallShowsAgeAndGoesIdle(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	run := func(age time.Duration) Event {
		return Event{Time: now.Add(-age), Kind: KindRun, Tool: "Bash", Cmd: "go test ./...", Line: -1, EndLine: -1}
	}
	agent := func(e Event, updated, wrote time.Time) Agent {
		return Agent{Updated: updated, Wrote: wrote, Events: []Event{{Time: e.Time, Kind: KindYou, Text: "go"}, e}}
	}
	cases := []struct {
		name   string
		a      Agent
		status Status
		act    string
		detail string
	}{
		{"young run", agent(run(30*time.Second), now.Add(-30*time.Second), time.Time{}), Active, "running", "$ go test ./..."},
		{"long run", agent(run(5*time.Minute), now.Add(-5*time.Minute), time.Time{}), Active, "running", "5m · $ go test ./..."},
		{"run with log writes", agent(run(47*time.Minute), now.Add(-47*time.Minute), now.Add(-time.Minute)), Active, "running", "47m · $ go test ./..."},
		{"lost run", agent(run(5*time.Hour), now.Add(-5*time.Hour), time.Time{}), Done, "idle", "no result · $ go test ./..."},
	}
	approval := run(2 * time.Hour)
	approval.Approval = true
	cases = append(cases, struct {
		name   string
		a      Agent
		status Status
		act    string
		detail string
	}{"old approval", agent(approval, now.Add(-2*time.Hour), time.Time{}), Waiting, "waiting", "2h · on you · approve Bash"})
	for _, c := range cases {
		act := c.a.Activity(now)
		if act.Status != c.status || act.Act != c.act || act.Detail != c.detail {
			t.Errorf("%s: got %+v", c.name, act)
		}
	}
}

func TestCodexBackgroundCommandWithRead(t *testing.T) {
	h := &codexHandle{procs: map[string]string{}, polls: map[string]string{}}
	s := newSession("codex:x", "codex", h)
	feed(t, h, s,
		codexMeta("/w"),
		`{"timestamp":"2026-10-27T10:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"build it"}}`,
		`{"timestamp":"2026-10-27T10:00:06Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"sed -n 1,20p a.go && cargo build\"}","call_id":"c3"}}`,
		`{"timestamp":"2026-10-27T10:00:07Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c3","output":"Process running with session ID 7\nOutput:\nCompiling a"}}`,
		`{"timestamp":"2026-10-27T10:00:08Z","type":"response_item","payload":{"type":"function_call","name":"write_stdin","arguments":"{\"session_id\":7,\"chars\":\"\"}","call_id":"c4"}}`,
		`{"timestamp":"2026-10-27T10:00:09Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c4","output":"Process exited with code 0\nOutput:\nFinished"}}`,
		`{"timestamp":"2026-10-27T10:00:10Z","type":"event_msg","payload":{"type":"agent_message","message":"Now I look at the tests."}}`,
	)
	for _, e := range s.Events {
		if isTool(e.Kind) && e.Result == Pending {
			t.Errorf("%s %s stays pending", e.Kind, e.File)
		}
	}
	if act := s.Activity(time.Date(2026, 10, 27, 10, 1, 0, 0, time.UTC)); act.Status == Waiting {
		t.Errorf("the agent works, but shows %+v", act)
	}
}

func TestCodexBackgroundOutputIsKept(t *testing.T) {
	h := &codexHandle{procs: map[string]string{}, polls: map[string]string{}}
	s := newSession("codex:x", "codex", h)
	lines := []string{
		codexMeta("/w"),
		`{"timestamp":"2026-10-27T10:00:06Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"cargo build\"}","call_id":"c3"}}`,
		`{"timestamp":"2026-10-27T10:00:07Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c3","output":"Process running with session ID 7\nOutput:\nCompiling a"}}`,
	}
	poll := func(id, out string) []string {
		return []string{
			`{"timestamp":"2026-10-27T10:00:08Z","type":"response_item","payload":{"type":"function_call","name":"write_stdin","arguments":"{\"session_id\":7,\"chars\":\"\"}","call_id":"` + id + `"}}`,
			`{"timestamp":"2026-10-27T10:00:09Z","type":"response_item","payload":{"type":"function_call_output","call_id":"` + id + `","output":"` + out + `"}}`,
		}
	}
	// Output that looks like an exit code does not end the command.
	lines = append(lines, poll("p1", `Process running with session ID 7\nOutput:\nCompiling b\nexit code 3`)...)
	feed(t, h, s, lines...)
	if e := s.Events[0]; e.Result != Pending {
		t.Fatalf("the command ended early: %+v", e)
	}
	for i := 0; i < 300; i++ {
		feed(t, h, s, poll("q"+itoa(i), `Process running with session ID 7\nOutput:\nline `+itoa(i))...)
	}
	if n := len(s.Events[0].Output); n != maxOutLine {
		t.Fatalf("output has %d lines, want %d", n, maxOutLine)
	}
	feed(t, h, s, poll("p2", `Process exited with code 0\nOutput:\n`)...)
	e := s.Events[0]
	if e.Result != OK || e.Exit != 0 || len(e.Output) != maxOutLine || e.Output[len(e.Output)-1] != "line 299" {
		t.Fatalf("result %d exit %d output %d lines, last %q", e.Result, e.Exit, len(e.Output), e.Output[len(e.Output)-1])
	}

	h = &codexHandle{procs: map[string]string{}, polls: map[string]string{}}
	s = newSession("codex:y", "codex", h)
	feed(t, h, s, lines[:3]...)
	feed(t, h, s, poll("p1", `Process running with session ID 7\nOutput:\nCompiling b`)...)
	feed(t, h, s, poll("p2", `Process exited with code 0\nOutput:\n`)...)
	if got := strings.Join(s.Events[0].Output, ","); got != "Compiling a,Compiling b" {
		t.Fatalf("output: %q", got)
	}
}

func TestCodexSamePromptInTwoTurns(t *testing.T) {
	h := &codexHandle{procs: map[string]string{}, polls: map[string]string{}}
	s := newSession("codex:x", "codex", h)
	turn := func(at string) []string {
		return []string{
			`{"timestamp":"` + at + `","type":"event_msg","payload":{"type":"task_started"}}`,
			`{"timestamp":"` + at + `","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}}`,
			`{"timestamp":"` + at + `","type":"event_msg","payload":{"type":"user_message","message":"continue"}}`,
			`{"timestamp":"` + at + `","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]}}`,
			`{"timestamp":"` + at + `","type":"event_msg","payload":{"type":"agent_message","message":"Done."}}`,
			`{"timestamp":"` + at + `","type":"event_msg","payload":{"type":"task_complete","last_agent_message":"Done."}}`,
		}
	}
	feed(t, h, s, codexMeta("/w"))
	feed(t, h, s, turn("2026-10-27T10:00:01Z")...)
	feed(t, h, s, turn("2026-10-27T10:01:01Z")...)
	var got []string
	for _, e := range s.Events {
		got = append(got, string(e.Kind))
	}
	if strings.Join(got, ",") != "you,say,done,you,say,done" {
		t.Fatalf("events: %v", got)
	}
}

func TestNotesClearAndMissingLogs(t *testing.T) {
	home := fakeHome(t)
	root := filepath.Join(home, "proj")
	f := writeLog(t, claudePath(home, root, "s.jsonl"), claudePrompt(root, time.Now(), "hi"))
	w := New(Options{Root: root, Since: time.Hour})
	w.step(true)
	if n := len(w.snapshot().Agents); n != 1 {
		t.Fatalf("agents: %d", n)
	}
	// A log that the watcher cannot read gives a note.
	if err := os.Chmod(f, 0); err != nil {
		t.Fatal(err)
	}
	if !w.step(false) || len(w.snapshot().Notes) != 1 {
		t.Skip("the file stays readable, for example for root")
	}
	// The note goes away when the log reads again.
	if err := os.Chmod(f, 0o644); err != nil {
		t.Fatal(err)
	}
	w.step(false)
	if notes := w.snapshot().Notes; len(notes) != 0 {
		t.Fatalf("notes after a good read: %v", notes)
	}
	// A missing log removes the session without a note.
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	w.step(false)
	if sn := w.snapshot(); len(sn.Notes) != 0 || len(sn.Agents) != 0 {
		t.Fatalf("after remove: notes %v agents %d", sn.Notes, len(sn.Agents))
	}
	// The session comes back with its log.
	writeLog(t, f, claudePrompt(root, time.Now(), "hi"))
	w.step(true)
	if n := len(w.snapshot().Agents); n != 1 {
		t.Fatalf("agents after the log came back: %d", n)
	}
}

func TestQuietSessionIsParked(t *testing.T) {
	home := fakeHome(t)
	root := filepath.Join(home, "proj")
	now := time.Now()
	f := writeLog(t, claudePath(home, root, "s.jsonl"),
		claudePrompt(root, now.Add(-44*time.Hour), "old work"),
		`{"type":"atis-latch","timestamp":"`+now.UTC().Format(time.RFC3339)+`"}`)
	setTime(t, f, now.Add(-30*time.Second))
	w := New(Options{Root: root, Since: 24 * time.Hour})
	w.step(true)
	if n := len(w.snapshot().Agents); n != 0 || len(w.parked) != 1 {
		t.Fatalf("a session quiet for 44h shows: agents %d parked %d", n, len(w.parked))
	}
	// A metadata line does not bring the session back.
	appendLines(t, f, []string{`{"type":"atis-latch"}`})
	setTime(t, f, now.Add(-20*time.Second))
	w.step(true)
	if n := len(w.snapshot().Agents); n != 0 || len(w.parked) != 1 {
		t.Fatalf("after a metadata line: agents %d parked %d", n, len(w.parked))
	}
	// The watcher does not poll a parked session. Only a scan finds the
	// new write.
	appendLines(t, f, []string{claudePrompt(root, time.Now(), "new work")})
	setTime(t, f, now.Add(-10*time.Second))
	w.step(false)
	if n := len(w.snapshot().Agents); n != 0 {
		t.Fatalf("a parked session was polled: agents %d", n)
	}
	w.step(true)
	sn := w.snapshot()
	if len(sn.Agents) != 1 || len(sn.Agents[0].Events) != 2 || sn.Agents[0].Wrote.IsZero() {
		t.Fatalf("after a new event: %+v", sn.Agents)
	}
}

func TestCodexResumeInOldDateFolder(t *testing.T) {
	home := fakeHome(t)
	now := time.Now()
	writeLog(t, codexPath(home, now.AddDate(0, 0, -3), "rollout-a.jsonl"), codexMeta("/w"), codexPrompt(now, "resume a"))
	w := New(Options{Root: "/w", Since: time.Hour})
	w.step(true)
	if n := len(w.snapshot().Agents); n != 1 {
		t.Fatalf("the walk at the start does not find the resumed rollout: %d agents", n)
	}
	// A scan between two walks reads only the recent date folders.
	writeLog(t, codexPath(home, now.AddDate(0, 0, -5), "rollout-b.jsonl"), codexMeta("/w"), codexPrompt(now, "resume b"))
	w.step(true)
	if n := len(w.snapshot().Agents); n != 1 {
		t.Fatalf("a scan walked all folders: %d agents", n)
	}
	w.sources[1].(*codex).walked = time.Time{}
	w.step(true)
	if n := len(w.snapshot().Agents); n != 2 {
		t.Fatalf("the next walk does not find the second rollout: %d agents", n)
	}
}

func TestCwdCacheOfOtherProjects(t *testing.T) {
	home := fakeHome(t)
	now := time.Now()
	f := writeLog(t, codexPath(home, now, "rollout-a.jsonl"), codexMeta("/other"), codexPrompt(now, "x"))
	root := filepath.Join(home, "proj")
	cf := writeLog(t, claudePath(home, root+"-site", "s.jsonl"), claudePrompt(root+"-site", now, "x"))
	w := New(Options{Root: root, Since: time.Hour})
	w.step(true)
	if !w.sources[1].(*codex).files[f].away || !w.sources[0].(*claude).files[cf].away {
		t.Fatal("the scan does not cache the logs of other projects")
	}
	// The scan uses the cache and does not read the heads again.
	writeLog(t, f, codexMeta(root), codexPrompt(now, "x"))
	writeLog(t, cf, claudePrompt(root, now, "x"))
	w.step(true)
	if n := len(w.snapshot().Agents); n != 0 {
		t.Fatalf("the scan read a head again: %d agents", n)
	}
	// A new scope starts with new caches.
	w.closeSources()
	w.init()
	w.step(true)
	if n := len(w.snapshot().Agents); n != 2 {
		t.Fatalf("after init: %d agents", n)
	}
}

func TestElsewhereCount(t *testing.T) {
	home := fakeHome(t)
	now := time.Now()
	root, other, sibling := filepath.Join(home, "a"), filepath.Join(home, "b"), filepath.Join(home, "a-site")
	writeLog(t, claudePath(home, root, "mine.jsonl"), claudePrompt(root, now, "mine"))
	writeLog(t, claudePath(home, other, "o.jsonl"), claudePrompt(other, now, "other"))
	writeLog(t, claudePath(home, other, filepath.Join("o", "subagents", "agent-1.jsonl")), claudePrompt(other, now, "sub"))
	writeLog(t, claudePath(home, sibling, "x.jsonl"), claudePrompt(sibling, now, "sibling"))
	old := writeLog(t, claudePath(home, other, "old.jsonl"), claudePrompt(other, now.Add(-3*time.Hour), "old"))
	setTime(t, old, now.Add(-3*time.Hour))
	writeLog(t, codexPath(home, now, "rollout-a.jsonl"), codexMeta(other), codexPrompt(now, "x"))
	omp := filepath.Join(home, ".omp", "agent", "sessions", "--b--", "s.jsonl")
	writeLog(t, omp, `{"type":"session","timestamp":"`+now.UTC().Format(time.RFC3339)+`","cwd":"`+other+`"}`)
	w := New(Options{Root: root, Since: time.Hour})
	w.step(true)
	sn := w.snapshot()
	// o.jsonl and x.jsonl of Claude, the Codex rollout and the omp session.
	if len(sn.Agents) != 1 || sn.Elsewhere != 4 {
		t.Fatalf("agents %d elsewhere %d", len(sn.Agents), sn.Elsewhere)
	}
	w.opt.All = true
	w.closeSources()
	w.init()
	w.step(true)
	if sn := w.snapshot(); sn.Elsewhere != 0 {
		t.Fatalf("elsewhere %d with all projects", sn.Elsewhere)
	}
}

func TestShellQuotesAndSubshells(t *testing.T) {
	type want struct {
		kind  Kind
		file  string
		query string
	}
	cases := []struct {
		cmd  string
		want []want
	}{
		{`rg "\"version\"" package.json`, []want{{KindGrep, "", `"version"`}}},
		{`grep -n "say \"hi there\"" a.go`, []want{{KindGrep, "", `say "hi there"`}}},
		{`grep -n "foo$" a.go`, []want{{KindGrep, "", "foo$"}}},
		{`sed -n 1,5p my\ file.go`, []want{{KindRead, "/w/my file.go", ""}}},
		{`head -n 5 my\ file.go`, []want{{KindRead, "/w/my file.go", ""}}},
		{`cat 'it'\''s.go'`, []want{{KindRead, "/w/it's.go", ""}}},
		{"sed -n 1,5p \\\n  a.go 2> /dev/null", []want{{KindRead, "/w/a.go", ""}}},
		{"(cd sub && cat a.go)", []want{{KindRead, "/w/sub/a.go", ""}}},
		{"(cd sub && cat a.go) && cat b.go", []want{{KindRead, "/w/sub/a.go", ""}, {KindRead, "/w/b.go", ""}}},
		{"echo $(cat a.txt; cat b.txt)", []want{{KindRead, "/w/a.txt", ""}, {KindRead, "/w/b.txt", ""}}},
		{"echo \"$(cd sub; cat a.go)\" && cat b.go", []want{{KindRead, "/w/sub/a.go", ""}, {KindRead, "/w/b.go", ""}}},
		{"echo `cat a.go`", []want{{KindRead, "/w/a.go", ""}}},
		{"diff <(sed -n 1,5p a.go) <(sed -n 1,5p b.go)", []want{{KindRead, "/w/a.go", ""}, {KindRead, "/w/b.go", ""}, {KindRun, "", ""}}},
		{"cd - && cat a.go", []want{{KindRun, "", ""}}},
		{"cd sub && cd .. && cd - && cat a.go", []want{{KindRead, "/w/sub/a.go", ""}}},
		{`cd "$DIR" && cat a.go`, []want{{KindRun, "", ""}}},
		{"pushd sub && cat a.go && popd && cat b.go", []want{{KindRead, "/w/sub/a.go", ""}, {KindRead, "/w/b.go", ""}}},
		{"cat a.go # read; rm -rf x", []want{{KindRead, "/w/a.go", ""}}},
		{"VERSION=$(cat VERSION) && echo $VERSION", []want{{KindRead, "/w/VERSION", ""}}},
		{"LC_ALL=C grep -rn TODO .", []want{{KindGrep, "", "TODO"}}},
		{"if grep -q x a.go; then cat b.go; fi", []want{{KindGrep, "", "x"}, {KindRead, "/w/b.go", ""}}},
		{"echo x > a.txt && cat a.txt", []want{{KindRead, "/w/a.txt", ""}, {KindRun, "", ""}}},
		{"cat a.go > b.go", []want{{KindRun, "", ""}}},
		{"for f in a b; do cat $f; done", []want{{KindRun, "", ""}}},
		{"case $x in a) cat a.go;; esac", []want{{KindRun, "", ""}}},
		{"f() { cat a.go; }", []want{{KindRun, "", ""}}},
	}
	for _, c := range cases {
		evs := shellEvents(Event{Line: -1, EndLine: -1}, c.cmd, "/w")
		if len(evs) != len(c.want) {
			t.Errorf("%q: got %d events %+v, want %d", c.cmd, len(evs), evs, len(c.want))
			continue
		}
		for i, w := range c.want {
			if e := evs[i]; e.Kind != w.kind || e.File != w.file || (w.query != "" && e.Query != w.query) {
				t.Errorf("%q event %d: got kind %s file %q query %q", c.cmd, i, e.Kind, e.File, e.Query)
			}
		}
		if evs[0].Cmd != c.cmd && evs[len(evs)-1].Cmd != c.cmd {
			t.Errorf("%q: command text is lost", c.cmd)
		}
	}
}

func TestShellParserDoesNotPanic(t *testing.T) {
	odd := []string{"", "\\", "$", "$(", "(", ")", "'", "\"", "`", "a|", "|a", "&&", ";;", "2>", "<(", "$((1", "${",
		"$'", "\\\n", "cd", "cd ~", "pushd", "popd", "#", "a #b", "2>&", ">&-", "x=", "(())", "((", "$((1))"}
	odd = append(odd, strings.Repeat("$(", 200)+"cat a"+strings.Repeat(")", 200))
	odd = append(odd, strings.Repeat("(", 200)+"cat a"+strings.Repeat(")", 200))
	r := rand.New(rand.NewSource(1))
	const alphabet = "ab /.-~$()`'\"\\|&;<>#{}=\n\t012"
	for i := 0; i < 20000; i++ {
		b := make([]byte, r.Intn(24))
		for j := range b {
			b[j] = alphabet[r.Intn(len(alphabet))]
		}
		odd = append(odd, string(b))
	}
	for _, s := range odd {
		evs := shellEvents(Event{Line: -1, EndLine: -1}, s, "/w")
		if len(evs) == 0 {
			t.Fatalf("%q: no events", s)
		}
	}
}

func TestGeminiTurnEndsAfterErrorOrCancel(t *testing.T) {
	for _, c := range []struct {
		last, text string
	}{
		{`{"id":"e1","timestamp":"2026-10-27T10:00:02Z","type":"error","content":"quota exceeded"}`, "error: quota exceeded"},
		{`{"id":"g2","timestamp":"2026-10-27T10:00:02Z","type":"gemini","content":"","toolCalls":[{"id":"t1","name":"run_shell_command","args":{"command":"rm x"},"status":"cancelled"}]}`, "interrupted"},
	} {
		path := filepath.Join(t.TempDir(), "session-x.jsonl")
		writeLog(t, path,
			`{"id":"u1","timestamp":"2026-10-27T10:00:00Z","type":"user","content":[{"text":"hi"}]}`,
			`{"id":"g1","timestamp":"2026-10-27T10:00:01Z","type":"gemini","content":"working"}`,
			c.last)
		h := &geminiHandle{path: path, t: tail{path: path}, msgs: map[string]*gemMsg{}}
		s := newSession("gemini:x", "gemini", h)
		if err := h.poll(s); err != nil {
			t.Fatal(err)
		}
		h.last = time.Now().Add(-time.Minute)
		h.maybeEnd(s)
		if e := s.Events[len(s.Events)-1]; !s.TurnEnded || e.Kind != KindDone || e.Text != c.text {
			t.Errorf("last %s: ended %v event %+v", c.last[:30], s.TurnEnded, e)
		}
	}
}

func TestGeminiLegacyRewriteInOneTick(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-x.json")
	at := time.Now().Add(-time.Minute).Truncate(time.Second)
	// The reader sees the file while Gemini CLI writes it.
	writeLog(t, path, `{"messages":[{"id":"u1","type":"user","content":"hi"`)
	setTime(t, path, at)
	h := &geminiHandle{path: path, legacy: true, msgs: map[string]*gemMsg{}}
	s := newSession("gemini:x", "gemini", h)
	if err := h.poll(s); err != nil {
		t.Fatal(err)
	}
	// The full file has the same modification time.
	writeLog(t, path, `{"messages":[{"id":"u1","timestamp":"2026-10-27T10:00:00Z","type":"user","content":"hi"}]}`)
	setTime(t, path, at)
	if err := h.poll(s); err != nil {
		t.Fatal(err)
	}
	if len(s.Events) != 1 || s.Events[0].Kind != KindYou {
		t.Fatalf("events: %+v", s.Events)
	}
}
