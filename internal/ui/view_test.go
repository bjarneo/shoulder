package ui

import (
	"image/color"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bjarneo/shoulder/internal/repo"
	"github.com/bjarneo/shoulder/internal/watch"
)

const mainGo = `package main

import "fmt"

// greet returns a greeting.
func greet(name string) string {
	return "hello, " + name
}

func main() {
	fmt.Println(greet("world"))
}
`

// fixture builds a project with two files and three agents that read, edit
// and run commands in it.
func fixture(t *testing.T) (*Model, string) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mainPath := write("main.go", mainGo)
	limPath := write("internal/limit/limit.go", "package limit\n\nconst Burst = 20\n")
	r := repo.Open(dir)
	// The first load is the baseline outside git. Change the file after it.
	r.File(mainPath)
	write("main.go", strings.Replace(strings.Replace(mainGo, `"hello, "`, `"hi, "`, 1), "import \"fmt\"\n", "", 1)+"\n// done\n")
	// The repo reads a file at most once per 300 ms.
	time.Sleep(350 * time.Millisecond)

	now := time.Now()
	ev := func(ago time.Duration, e watch.Event) watch.Event {
		e.Time = now.Add(-ago)
		if e.Line == 0 && e.EndLine == 0 && e.Kind != watch.KindRead {
			e.Line, e.EndLine = -1, -1
		}
		return e
	}
	agents := []watch.Agent{
		{
			Key: "claude:a", Source: "claude", Name: "claude", Title: "Shorter greeting", Cwd: dir,
			Started: now.Add(-5 * time.Minute), Updated: now.Add(-2 * time.Second), Ctx: 80_000, CtxMax: 200_000,
			Events: []watch.Event{
				ev(5*time.Minute, watch.Event{Kind: watch.KindYou, Text: "make the greeting shorter"}),
				ev(4*time.Minute, watch.Event{Kind: watch.KindRead, File: mainPath, Line: 0, EndLine: 11, Result: watch.OK}),
				ev(3*time.Minute, watch.Event{Kind: watch.KindRun, Cmd: "go test ./...", Output: []string{"ok  \tdemo\t0.2s", "warning: unused"}, Result: watch.Warned}),
				ev(2*time.Second, watch.Event{Kind: watch.KindEdit, Tool: "Edit", CallID: "e1", File: mainPath, Needle: `return "hi, " + name`, Line: 6, EndLine: 6, Add: 1, Del: 1}),
			},
		},
		{
			Key: "codex:b", Source: "codex", Name: "codex", Prompt: "add a rate limit", Cwd: dir,
			Started: now.Add(-4 * time.Minute), Updated: now.Add(-time.Minute), Ctx: 20_000, CtxMax: 272_000,
			Events: []watch.Event{
				ev(4*time.Minute, watch.Event{Kind: watch.KindYou, Text: "add a rate limit"}),
				ev(time.Minute, watch.Event{Kind: watch.KindRead, File: limPath, Line: 0, EndLine: 2, Result: watch.OK}),
				ev(time.Minute, watch.Event{Kind: watch.KindAsk, Text: "Which burst size?"}),
			},
		},
		{
			Key: "gemini:c", Source: "gemini", Name: "gemini", Prompt: "write docs", Cwd: dir,
			Started: now.Add(-3 * time.Minute), Updated: now.Add(-time.Minute), TurnEnded: true,
			Events: []watch.Event{
				ev(3*time.Minute, watch.Event{Kind: watch.KindYou, Text: "write docs"}),
				ev(2*time.Minute, watch.Event{Kind: watch.KindGrep, Query: "greet", Hits: 2, Result: watch.OK}),
				ev(time.Minute, watch.Event{Kind: watch.KindSay, Text: "The docs are done."}),
				ev(time.Minute, watch.Event{Kind: watch.KindDone, Text: "The docs are done."}),
			},
		},
	}
	m := New(r, nil, Options{Since: 3 * time.Hour})
	m.Update(tea.WindowSizeMsg{Width: 180, Height: 50})
	m.Update(Snapshot(watch.Snapshot{Agents: agents}))
	return m, dir
}

func withTheme(m *Model) {
	m.theme.setFG(color.RGBA{0xc0, 0xca, 0xf5, 0xff})
	m.theme.setBG(color.RGBA{0x1a, 0x1b, 0x26, 0xff})
	m.theme.setPalette("\x1b]4;1;rgb:f7f7/7676/8e8e\x07\x1b]4;2;rgb:9e/ce/6a\x07\x1b]4;3;rgb:e0af/6868/0000\x07" +
		"\x1b]4;4;rgb:7a/a2/f7\x07\x1b]4;5;rgb:bb/9a/f7\x07\x1b]4;6;rgb:7d/cf/ff\x07")
}

var sgrRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func checkFrame(t *testing.T, m *Model, w, h int, name string) string {
	t.Helper()
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	out := m.View().Content
	lines := strings.Split(out, "\n")
	if len(lines) != h {
		t.Fatalf("%s %dx%d: %d lines", name, w, h, len(lines))
	}
	for i, l := range lines {
		if got := width(sgrRe.ReplaceAllString(l, "")); got != w {
			t.Fatalf("%s %dx%d: line %d is %d cells wide", name, w, h, i, got)
		}
	}
	if p := os.Getenv("SHOULDER_DUMP"); p != "" && w == 180 {
		_ = os.WriteFile(filepath.Join(p, name+".txt"), []byte(out), 0o644)
	}
	return out
}

func TestViewLayouts(t *testing.T) {
	sizes := [][2]int{{180, 50}, {140, 40}, {110, 32}, {80, 24}, {60, 12}}
	for _, themed := range []bool{false, true} {
		m, _ := fixture(t)
		prefix := "plain"
		if themed {
			withTheme(m)
			prefix = "themed"
		}
		for _, sz := range sizes {
			checkFrame(t, m, sz[0], sz[1], prefix+"-follow")
		}
		m.key("s")
		for _, sz := range sizes {
			checkFrame(t, m, sz[0], sz[1], prefix+"-splits")
		}
		m.key("s")
		m.key("d")
		checkFrame(t, m, 180, 50, prefix+"-signs")
		m.key("d")
		m.key("f")
		m.key("j")
		m.key("]")
		m.key("c")
		checkFrame(t, m, 180, 50, prefix+"-normal")
		m.key("?")
		checkFrame(t, m, 180, 50, prefix+"-help")
	}
}

func TestViewWithoutAgents(t *testing.T) {
	r := repo.Open(t.TempDir())
	m := New(r, nil, Options{Since: time.Hour})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.Update(Snapshot(watch.Snapshot{}))
	out := checkFrame(t, m, 120, 30, "empty")
	if !strings.Contains(out, "no agents yet") {
		t.Fatal("empty state is missing")
	}
	m.key("s")
	m.key("j")
	m.key("2")
	checkFrame(t, m, 120, 30, "empty-splits")
}

// editBy returns a copy of the snapshot where agent i edits path now.
func editBy(m *Model, i int, path string) watch.Snapshot {
	agents := append([]watch.Agent(nil), m.snap.Agents...)
	a := agents[i]
	a.Events = append(append([]watch.Event(nil), a.Events...), watch.Event{
		Time: time.Now(), Kind: watch.KindEdit, File: path, Needle: "package", Line: -1, EndLine: -1,
	})
	a.Updated, a.TurnEnded = time.Now(), false
	agents[i] = a
	return watch.Snapshot{Agents: agents}
}

func TestAutoFocusJumpsToEdits(t *testing.T) {
	m, dir := fixture(t)
	main, lim := filepath.Join(dir, "main.go"), filepath.Join(dir, "internal/limit/limit.go")
	focus := func() string { return tag(m.focusIndex()) }
	if focus() != "A1" {
		t.Fatalf("start focus: %s, want the agent with the newest edit", focus())
	}

	m.Update(Snapshot(editBy(m, 1, lim)))
	if focus() != "A2" || !strings.Contains(m.msg.text, "A2 codex edits internal/limit/limit.go") {
		t.Fatalf("after A2 edits: focus %s, msg %q", focus(), m.msg.text)
	}

	// A second edit within the dwell time waits for the next tick.
	m.Update(Snapshot(editBy(m, 0, main)))
	if focus() != "A2" {
		t.Fatalf("jumped within the dwell time to %s", focus())
	}
	m.lastJump = time.Now().Add(-2 * autoDwell)
	m.Update(tickMsg{})
	if focus() != "A1" {
		t.Fatalf("after the dwell: focus %s", focus())
	}

	// A pinned agent keeps the focus.
	m.key("2")
	m.lastJump = time.Time{}
	m.Update(Snapshot(editBy(m, 0, main)))
	if focus() != "A2" || m.auto {
		t.Fatalf("pinned: focus %s auto %v", focus(), m.auto)
	}

	// a turns auto focus on and goes to the newest edit.
	m.key("a")
	if focus() != "A1" || !m.auto {
		t.Fatalf("auto on: focus %s auto %v", focus(), m.auto)
	}

	// Free scroll does not jump.
	m.key("f")
	m.lastJump = time.Time{}
	m.Update(Snapshot(editBy(m, 1, lim)))
	if focus() != "A1" {
		t.Fatalf("free scroll jumped to %s", focus())
	}
}

func TestAutoFocusLeavesFinishedAgent(t *testing.T) {
	m, _ := fixture(t)
	m.key("3") // pin the finished agent A3
	m.key("a")
	m.prepare()
	if m.acts[0].Status != watch.Active {
		t.Fatalf("A1 should be active: %+v", m.acts[0])
	}
	m.focusKey = m.snap.Agents[2].Key
	m.lastJump = time.Time{}
	m.Update(tickMsg{})
	if got := tag(m.focusIndex()); got != "A1" {
		t.Fatalf("focus stayed on a finished agent: %s", got)
	}
}

func TestPendingDeleteKeepsPosition(t *testing.T) {
	m, dir := fixture(t)
	// A deletion has no inserted text, and its line number comes with the
	// result. Until then, the cursor stays where the agent was.
	agents := append([]watch.Agent(nil), m.snap.Agents...)
	a := agents[0]
	a.Events = append(append([]watch.Event(nil), a.Events...), watch.Event{
		Time: time.Now(), Kind: watch.KindEdit, File: filepath.Join(dir, "main.go"), Line: -1, EndLine: -1,
	})
	agents[0] = a
	m.Update(Snapshot(watch.Snapshot{Agents: agents}))
	m.prepare()
	if p := m.poss[0]; !p.ok || p.line != 5 {
		t.Fatalf("position during a pending deletion: %+v", p)
	}
}

func TestEditCursorFollowsText(t *testing.T) {
	m, _ := fixture(t)
	m.prepare()
	p := m.poss[0]
	// The agent reported line 7, but the fixture removed a line above it.
	if !p.ok || p.line != 5 {
		t.Fatalf("edit position: %+v", p)
	}
	if !p.editing {
		t.Fatalf("agent should be editing: %+v", m.acts[0])
	}
}

func TestAllProjects(t *testing.T) {
	m, _ := fixture(t)
	other := t.TempDir()
	path := filepath.Join(other, "notes.md")
	if err := os.WriteFile(path, []byte("# notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agents := append([]watch.Agent(nil), m.snap.Agents...)
	agents = append(agents, watch.Agent{
		Key: "codex:other", Source: "codex", Name: "codex", Prompt: "write notes", Cwd: other,
		Started: time.Now(), Updated: time.Now(),
		Events: []watch.Event{{Time: time.Now(), Kind: watch.KindEdit, File: path, Needle: "# notes", Line: -1, EndLine: -1}},
	})
	m.Update(Snapshot(watch.Snapshot{Agents: agents}))
	hasFile := func() bool {
		for _, f := range m.files {
			if f.path == path {
				return true
			}
		}
		return false
	}
	if hasFile() || contains(m.tabs, path) {
		t.Fatal("a file of another project shows in project mode")
	}
	m.key("A")
	if !m.all {
		t.Fatal("A does not turn on all projects")
	}
	m.Update(Snapshot(watch.Snapshot{Agents: agents}))
	if !hasFile() || !contains(m.tabs, path) {
		t.Fatal("the file of another project is missing in all projects")
	}
	out := checkFrame(t, m, 180, 50, "all")
	if !strings.Contains(out, filepath.Base(other)+" · ") || !strings.Contains(out, "all projects") {
		t.Fatal("the agent row or the top line does not show the project")
	}
	m.key("A")
	if m.all {
		t.Fatal("A does not turn off all projects")
	}
}

func TestOldEventsDoNotJump(t *testing.T) {
	m, dir := fixture(t)
	m.pin(1)
	m.toggleAuto()
	m.lastJump = time.Time{}
	agents := append([]watch.Agent(nil), m.snap.Agents...)
	old := time.Now().Add(-16 * time.Hour)
	agents = append(agents, watch.Agent{
		Key: "claude:old", Source: "claude", Name: "claude", Cwd: dir, Started: old, Updated: old, TurnEnded: true,
		Events: []watch.Event{{Time: old, Kind: watch.KindEdit, File: filepath.Join(dir, "main.go"), Needle: "package", Line: -1, EndLine: -1}},
	})
	focus := m.focusKey
	m.Update(Snapshot(watch.Snapshot{Agents: agents}))
	if m.focusKey != focus {
		t.Fatalf("an old edit moved the focus to %s", m.focusKey)
	}
}
