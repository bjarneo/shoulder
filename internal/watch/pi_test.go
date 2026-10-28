package watch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPiTranscript(t *testing.T) {
	h := &piHandle{}
	s := newSession("pi:x", "pi", h)
	feed(t, h, s,
		`{"type":"session","version":3,"id":"01a18381","timestamp":"2026-10-28T17:14:09.589Z","cwd":"/w"}`,
		`{"type":"model_change","id":"8298d825","parentId":null,"timestamp":"2026-10-28T17:14:09.795Z","provider":"zai","modelId":"glm-5.3-flash"}`,
		`{"type":"thinking_level_change","id":"602f78aa","parentId":"8298d825","timestamp":"2026-10-28T17:14:09.796Z","thinkingLevel":"high"}`,
		`{"type":"message","id":"u1","timestamp":"2026-10-28T17:15:32.891Z","message":{"role":"user","content":[{"type":"text","text":"add a limiter"}]}}`,
		`{"type":"message","id":"a1","timestamp":"2026-10-28T17:15:48.844Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hm"},{"type":"toolCall","id":"call_1","name":"edit","arguments":{"path":"/w/a.go","oldText":"x := 1","newText":"x := 2\ny := 3"}}],"stopReason":"toolUse","usage":{"input":10,"output":5,"cacheRead":5000,"cacheWrite":0}}}`,
		`{"type":"message","id":"r1","timestamp":"2026-10-28T17:15:48.881Z","message":{"role":"toolResult","toolCallId":"call_1","toolName":"edit","content":[{"type":"text","text":"ok"}],"isError":false}}`,
		`{"type":"message","id":"a2","timestamp":"2026-10-28T17:15:49.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"call_2","name":"bash","arguments":{"command":"go test ./...","timeout":30}}],"stopReason":"toolUse"}}`,
		`{"type":"message","id":"r2","timestamp":"2026-10-28T17:15:55.000Z","message":{"role":"toolResult","toolCallId":"call_2","toolName":"bash","content":[{"type":"text","text":"Exit code 1\nFAIL x"}],"isError":true}}`,
		`{"type":"message","id":"a3","timestamp":"2026-10-28T17:15:56.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"call_3","name":"ffgrep","arguments":{"pattern":"KindRun","path":"internal/watch"}}],"stopReason":"toolUse"}}`,
		`{"type":"message","id":"r3","timestamp":"2026-10-28T17:15:56.100Z","message":{"role":"toolResult","toolCallId":"call_3","toolName":"ffgrep","content":[{"type":"text","text":"tool.go:1\nsession.go:2\nwatcher.go:3"}],"isError":false}}`,
		`{"type":"message","id":"a4","timestamp":"2026-10-28T17:16:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Done."}],"stopReason":"stop","usage":{"input":10,"output":8,"cacheRead":4288,"cacheWrite":0}}}`,
	)
	if s.Prompt != "add a limiter" {
		t.Fatalf("prompt %q", s.Prompt)
	}
	kinds := []Kind{}
	for _, e := range s.Events {
		kinds = append(kinds, e.Kind)
	}
	want := []Kind{KindYou, KindEdit, KindRun, KindGrep, KindSay, KindDone}
	if strings.Join(kindStrings(kinds), ",") != strings.Join(kindStrings(want), ",") {
		t.Fatalf("kinds: %v", kinds)
	}
	if e := s.Events[1]; e.CallID != "call_1" || e.File != "/w/a.go" || e.Add != 2 || e.Del != 1 || e.Result != OK {
		t.Fatalf("edit: %+v", e)
	}
	if e := s.Events[2]; e.CallID != "call_2" || e.Result != Failed || e.Exit != 1 || e.Summary() != "✗ exit 1" {
		t.Fatalf("run: %+v", e)
	}
	if e := s.Events[3]; e.CallID != "call_3" || e.Query != "KindRun" || e.Text != "internal/watch" || e.Hits != 3 || e.Result != OK {
		t.Fatalf("grep: %+v", e)
	}
	if !s.TurnEnded || s.Events[len(s.Events)-1].Kind != KindDone || s.Events[len(s.Events)-1].Text != "Done." {
		t.Fatalf("turn end: %+v", s.Events[len(s.Events)-1])
	}
	if s.Ctx != 4298 {
		t.Fatalf("ctx: %d", s.Ctx)
	}
	feed(t, h, s,
		`{"type":"message","id":"u2","timestamp":"2026-10-28T17:16:10.000Z","message":{"role":"user","content":[{"type":"text","text":"also add tests"}]}}`,
		`{"type":"message","id":"a5","timestamp":"2026-10-28T17:16:12.000Z","message":{"role":"assistant","content":[{"type":"text","text":"partial"}],"stopReason":"aborted"}}`,
	)
	if s.Events[len(s.Events)-1].Kind != KindDone || s.Events[len(s.Events)-1].Text != "interrupted" {
		t.Fatalf("abort: %+v", s.Events[len(s.Events)-1])
	}
}

func TestPiScan(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	munged := "-" + strings.ReplaceAll(root, "/", "-") + "-"
	sdir := filepath.Join(dir, "sessions", munged)
	if err := os.MkdirAll(sdir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"session","version":3,"id":"01a18381","timestamp":"2026-10-28T17:14:09.589Z","cwd":"` + root + `"}`,
		`{"type":"message","id":"u1","timestamp":"2026-10-28T17:15:32.891Z","message":{"role":"user","content":[{"type":"text","text":"fix the limiter"}]}}`,
	}
	path := filepath.Join(sdir, "2026-10-28T17-14-09-589Z_01a18381-452e-7277-9899-a971ec9b3c1e.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := New(Options{Root: root})
	newPi().scan(w, time.Now().Add(-time.Hour))
	if len(w.sessions) != 1 {
		t.Fatalf("sessions: %d", len(w.sessions))
	}
	var s *session
	for _, x := range w.sessions {
		s = x
	}
	if s.Source != "pi" || s.Cwd != root || s.Key != "pi:"+path {
		t.Fatalf("session: source %q cwd %q key %q", s.Source, s.Cwd, s.Key)
	}
	if err := s.h.poll(s); err != nil {
		t.Fatal(err)
	}
	if s.Prompt != "fix the limiter" {
		t.Fatalf("prompt: %q", s.Prompt)
	}
}
