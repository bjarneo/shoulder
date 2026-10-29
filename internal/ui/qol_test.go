package ui

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bjarneo/shoulder/internal/watch"
)

func TestHideDone(t *testing.T) {
	m, _ := fixture(t)
	m.pin(0)
	if len(m.snap.Agents) != 3 {
		t.Fatalf("agents: %d", len(m.snap.Agents))
	}
	m.key("x")
	if len(m.snap.Agents) != 2 || m.hidden() != 1 {
		t.Fatalf("x keeps %d agents, hides %d", len(m.snap.Agents), m.hidden())
	}
	out := checkFrame(t, m, 180, 50, "hide-done")
	if !strings.Contains(out, "✓1") || strings.Contains(out, "A3 gemini") {
		t.Fatal("the sidebar shows the done agent, or misses it in the count")
	}
	// A new snapshot keeps the agent hidden.
	m.Update(Snapshot(m.full))
	if len(m.snap.Agents) != 2 {
		t.Fatalf("after a snapshot: %d agents", len(m.snap.Agents))
	}
	m.key("x")
	if len(m.snap.Agents) != 3 || m.hidden() != 0 {
		t.Fatalf("x again keeps %d agents", len(m.snap.Agents))
	}
}

func TestHideDoneKeepsFocusedAgent(t *testing.T) {
	m, _ := fixture(t)
	m.pin(2)
	m.key("x")
	if len(m.snap.Agents) != 3 || tag(m.focusIndex()) != "A3" {
		t.Fatalf("the focused agent is hidden: %d agents, focus %s", len(m.snap.Agents), tag(m.focusIndex()))
	}
}

func TestEditorArgs(t *testing.T) {
	cases := map[string][]string{
		"nvim":          {"nvim", "+12", "/w/a.go"},
		"vim -p":        {"vim", "-p", "+12", "/w/a.go"},
		"code --wait":   {"code", "--wait", "-g", "/w/a.go:12"},
		"/usr/bin/hx":   {"/usr/bin/hx", "/w/a.go:12"},
		"zed":           {"zed", "/w/a.go:12"},
		"":              {"vi", "+12", "/w/a.go"},
		"emacs -nw":     {"emacs", "-nw", "+12", "/w/a.go"},
		"cursor":        {"cursor", "-g", "/w/a.go:12"},
		"nano --unix":   {"nano", "--unix", "+12", "/w/a.go"},
		"micro -clean":  {"micro", "-clean", "+12", "/w/a.go"},
		"subl -n":       {"subl", "-n", "/w/a.go:12"},
		"kak -n":        {"kak", "-n", "+12", "/w/a.go"},
		"code-insiders": {"code-insiders", "-g", "/w/a.go:12"},
	}
	for ed, want := range cases {
		if got := editorArgs(ed, "/w/a.go", 11); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %v, want %v", ed, got, want)
		}
	}
}

func TestCopyPath(t *testing.T) {
	m, _ := fixture(t)
	m.pin(0)
	if m.copyPath() == nil || m.flash == nil || m.flash.text != "copied main.go:6" {
		t.Fatalf("flash: %+v", m.flash)
	}
	m.key("f")
	m.key("g")
	m.key("g")
	m.copyPath()
	if m.flash.text != "copied main.go:1" {
		t.Fatalf("free view: %q", m.flash.text)
	}
}

func TestHistory(t *testing.T) {
	m, dir := fixture(t)
	m.pin(0)
	a := &m.snap.Agents[0]
	n := len(a.Events)
	m.key(",")
	if got := m.histAt(0); got != n-1 {
		t.Fatalf("first step: event %d, want %d", got, n-1)
	}
	if out := checkFrame(t, m, 180, 50, "history"); !strings.Contains(out, "HISTORY") {
		t.Fatal("the status line does not show HISTORY")
	}
	m.key(",")
	if got := m.histAt(0); got != n-2 || a.Events[got].Kind != watch.KindRun {
		t.Fatalf("second step: event %d", got)
	}
	out := checkFrame(t, m, 180, 50, "history-run")
	if !strings.Contains(out, "go test ./...") || strings.Contains(out, "█") {
		t.Fatal("the terminal does not show the selected command")
	}
	m.key(",")
	if got := m.histAt(0); got != n-3 || m.view == nil || m.view.file != filepath.Join(dir, "main.go") || m.view.ucur != 0 {
		t.Fatalf("third step: event %d view %+v", got, m.view)
	}
	for range n {
		m.key(",")
	}
	if m.histAt(0) != 0 {
		t.Fatalf("the oldest step: %d", m.histAt(0))
	}
	for range n {
		m.key(".")
	}
	if m.hist != nil || !m.following() {
		t.Fatal("a step past the newest event does not go back to follow")
	}
	m.key(",")
	m.key("esc")
	if m.hist != nil || !m.following() {
		t.Fatal("esc does not leave history")
	}
}

func TestClickActivitySelectsEvent(t *testing.T) {
	m, _ := fixture(t)
	m.pin(0)
	out := checkFrame(t, m, 180, 50, "click")
	// Find the row of the read in the activity log.
	for y, l := range strings.Split(sgrRe.ReplaceAllString(out, ""), "\n") {
		if i := strings.Index(l, "read "); i > 120 {
			m.Update(tea.MouseClickMsg{X: i, Y: y, Button: tea.MouseLeft})
			break
		}
	}
	if j := m.histAt(0); j < 0 || m.snap.Agents[0].Events[j].Kind != watch.KindRead {
		t.Fatalf("the click selects event %d", j)
	}
}

func TestAwaySummary(t *testing.T) {
	m, dir := fixture(t)
	m.Update(tea.BlurMsg{})
	m.awayAt = time.Now().Add(-5 * time.Minute)
	m.Update(Snapshot(editBy(m, 0, filepath.Join(dir, "main.go"))))
	m.Update(tea.FocusMsg{})
	if want := "while you were away · A1 claude edited 1 file · A2 codex asks you · A3 gemini is done"; m.msg.text != want {
		t.Fatalf("got %q, want %q", m.msg.text, want)
	}
	if out := checkFrame(t, m, 180, 50, "away"); !strings.Contains(out, "while you were away ──") {
		t.Fatal("the activity log has no divider")
	}
	// A short time away gives no summary.
	m.msg = note{}
	m.Update(tea.BlurMsg{})
	m.Update(tea.FocusMsg{})
	if m.msg.text != "" {
		t.Fatalf("short time away: %q", m.msg.text)
	}
}

func TestCommandPreview(t *testing.T) {
	m, _ := fixture(t)
	m.pin(0)
	s := runBy(m, 0, "python3 - <<'EOF'\nimport os\nprint(1)\nprint(2)\nprint(3)\nprint(4)\nprint(5)\nEOF")
	m.Update(Snapshot(s))
	lines, _ := m.termLines(0, len(m.snap.Agents[0].Events), -1)
	var got []string
	for _, l := range lines {
		got = append(got, l.pre+l.text)
	}
	text := strings.Join(got, "\n")
	for _, want := range []string{"$ python3 - <<'EOF'", "> import os", "> print(3)", "> … 3 more lines"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if strings.Contains(text, "print(4)") {
		t.Error("the preview shows too many lines")
	}
}

func TestHelpFitsShortTerminal(t *testing.T) {
	m, _ := fixture(t)
	m.key("?")
	out := sgrRe.ReplaceAllString(checkFrame(t, m, 80, 24, "help-short"), "")
	if !strings.Contains(out, "or esc to close") || !strings.Contains(out, "more keys in a taller terminal") {
		t.Fatalf("the help does not fit 24 rows:\n%s", out)
	}
}
