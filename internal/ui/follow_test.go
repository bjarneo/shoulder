package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bjarneo/shoulder/internal/repo"
	"github.com/bjarneo/shoulder/internal/watch"
)

// gitFixture makes a git repository with a committed file that has an
// uncommitted change, and a model with two agents in it. A1 reads, and A2
// runs a command.
func gitFixture(t *testing.T) (*Model, *repo.Repo, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\nd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"add", "."},
		{"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "first"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(path, []byte("a\nB\nc\nd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := repo.Open(dir)
	now := time.Now()
	agents := []watch.Agent{
		{Key: "claude:a", Source: "claude", Name: "claude", Cwd: dir, Started: now.Add(-time.Minute), Updated: now,
			Events: []watch.Event{{Time: now.Add(-time.Second), Kind: watch.KindRead, File: path, Line: 0, EndLine: 3, Result: watch.Pending}}},
		{Key: "codex:b", Source: "codex", Name: "codex", Cwd: dir, Started: now.Add(-2 * time.Minute), Updated: now.Add(-time.Minute),
			Events: []watch.Event{{Time: now.Add(-time.Minute), Kind: watch.KindRead, File: path, Line: 0, EndLine: 0, Result: watch.OK}}},
	}
	m := New(r, nil, Options{Since: time.Hour})
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m.Update(Snapshot(watch.Snapshot{Agents: agents}))
	m.Update(repoMsg(r.Refresh()))
	m.focusKey = "claude:a"
	return m, r, path
}

// runBy returns a copy of the snapshot where agent i runs a command now.
func runBy(m *Model, i int, cmd string) watch.Snapshot {
	agents := append([]watch.Agent(nil), m.snap.Agents...)
	a := agents[i]
	a.Events = append(append([]watch.Event(nil), a.Events...), watch.Event{
		Time: time.Now(), Kind: watch.KindRun, CallID: "r1", Cmd: cmd, Result: watch.Pending, Line: -1, EndLine: -1,
	})
	a.Updated = time.Now()
	agents[i] = a
	return watch.Snapshot{Agents: agents}
}

func TestShellEditMovesFocus(t *testing.T) {
	m, r, path := gitFixture(t)
	m.Update(Snapshot(runBy(m, 1, "python3 fix.py")))
	time.Sleep(350 * time.Millisecond)
	if err := os.WriteFile(path, []byte("a\nB\nc\nD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Update(repoMsg(r.Refresh()))
	if got := tag(m.focusIndex()); got != "A2" {
		t.Fatalf("focus: got %s, want A2", got)
	}
	m.prepare()
	if p := m.poss[1]; !p.ok || p.file != path || p.line != 3 || !p.editing {
		t.Fatalf("position of A2: %+v", p)
	}
	if m.msg.text != "→ A2 codex edits a.txt" {
		t.Fatalf("message: %q", m.msg.text)
	}
}

func TestEditToolIsNotAShellEdit(t *testing.T) {
	m, r, path := gitFixture(t)
	s := runBy(m, 1, "go test ./...")
	a := &s.Agents[0]
	a.Events = append(a.Events, watch.Event{Time: time.Now(), Kind: watch.KindEdit, File: path, Needle: "D", Line: 3, EndLine: 3})
	m.Update(Snapshot(s))
	m.focusKey, m.lastJump = "claude:a", time.Time{}
	time.Sleep(350 * time.Millisecond)
	if err := os.WriteFile(path, []byte("a\nB\nc\nD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Update(repoMsg(r.Refresh()))
	if len(m.shell) != 0 {
		t.Fatalf("an edit tool call counts as a shell edit: %+v", m.shell)
	}
	if got := tag(m.focusIndex()); got != "A1" {
		t.Fatalf("focus: got %s, want A1", got)
	}
}

func TestFocusGoesToWaitingAgent(t *testing.T) {
	m, _, _ := gitFixture(t)
	agents := append([]watch.Agent(nil), m.snap.Agents...)
	a := agents[1]
	a.Events = append(append([]watch.Event(nil), a.Events...), watch.Event{
		Time: time.Now(), Kind: watch.KindAsk, Text: "Which name?", Result: watch.Pending, Line: -1, EndLine: -1,
	})
	a.Updated = time.Now()
	agents[1] = a
	m.Update(Snapshot(watch.Snapshot{Agents: agents}))
	if got := tag(m.focusIndex()); got != "A2" {
		t.Fatalf("focus: got %s, want A2", got)
	}
	if m.msg.text != "→ A2 codex asks you" {
		t.Fatalf("message: %q", m.msg.text)
	}
}
