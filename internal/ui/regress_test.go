package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/bjarneo/shoulder/internal/repo"
	"github.com/bjarneo/shoulder/internal/watch"
)

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lines100() string {
	var b strings.Builder
	for i := range 100 {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

// plainRows returns the rows of a frame without SGR sequences.
func plainRows(out string) []string {
	return strings.Split(sgrRe.ReplaceAllString(out, ""), "\n")
}

func TestFreeViewClampsShrunkFile(t *testing.T) {
	m, dir := fixture(t)
	big := filepath.Join(dir, "big.txt")
	writeFile(t, big, lines100())
	m.openFile(big)
	m.key("G")
	// esc goes back to follow, and the free view stays in the model.
	m.key("esc")
	writeFile(t, big, "a\nb\nc\n")
	time.Sleep(350 * time.Millisecond)
	m.key("d") // this panicked with an index out of range
	m.key("d")

	writeFile(t, big, lines100())
	time.Sleep(350 * time.Millisecond)
	m.openFile(big)
	m.key("G")
	if m.view.ucur != 99 {
		t.Fatalf("G: line %d", m.view.ucur)
	}
	writeFile(t, big, "a\nb\nc\n")
	time.Sleep(350 * time.Millisecond)
	rows := plainRows(checkFrame(t, m, 180, 50, "shrunk"))
	r := m.rowsOf(m.repo.File(big)).at[min(m.view.ucur, 2)]
	if m.view.ucur != 2 || r < m.view.top || r >= m.view.top+m.codeRows() {
		t.Fatalf("the free view is past the end: %+v, cursor row %d", *m.view, r)
	}
	if !strings.Contains(rows[48], " 3:1 ") {
		t.Fatalf("status line: %q", rows[48])
	}
	m.copyPath()
	if m.flash.text != "copied big.txt:3" {
		t.Fatalf("copy: %q", m.flash.text)
	}
}

// renderRow writes s on a canvas of w cells and returns the rendered row.
func renderRow(s string, w int) string {
	c := newCanvas(w, 1)
	c.text(0, 0, w, s, Style{})
	return c.render()
}

func TestCanvasMeasuresLikeRenderer(t *testing.T) {
	defer func(m xansi.Method) { widthMethod = m }(widthMethod)
	texts := []string{"⚠️ warn", "✔️ ok", "👍🏽 yes", "🇳🇴 flag", "👨‍👩‍👧 family", "中文 text", "é accent", "a​b", "│●…·━"}
	for _, method := range []xansi.Method{xansi.WcWidth, xansi.GraphemeWidth} {
		widthMethod = method
		for _, s := range texts {
			if got := method.StringWidth(renderRow(s, 20)); got != 20 {
				t.Errorf("method %d: %q renders %d cells, want 20", method, s, got)
			}
		}
	}
	// The terminal reports mode 2027, and the canvas follows the renderer.
	widthMethod = xansi.WcWidth
	m := New(repo.Open(t.TempDir()), nil, Options{Since: time.Hour})
	m.Update(tea.ModeReportMsg{Mode: xansi.ModeUnicodeCore, Value: xansi.ModeSet})
	if widthMethod != xansi.GraphemeWidth {
		t.Fatal("mode 2027 does not switch the width method")
	}
}

func TestFramesMatchRendererWidth(t *testing.T) {
	defer func(m xansi.Method) { widthMethod = m }(widthMethod)
	m, dir := fixture(t)
	p := filepath.Join(dir, "emoji.md")
	writeFile(t, p, "- ⚠️ warning\n- 👍🏽 thumbs\n- 🇳🇴 flag\n- 👨‍👩‍👧 family\n- 中文 text\n- é\n")
	m.openFile(p)
	for _, method := range []xansi.Method{xansi.WcWidth, xansi.GraphemeWidth} {
		widthMethod = method
		m.Update(tea.WindowSizeMsg{Width: 180, Height: 50})
		for i, l := range strings.Split(m.View().Content, "\n") {
			// The renderer measures the row with the same method. The
			// locale, such as LANG=ja_JP.UTF-8, does not change it.
			if got := method.StringWidth(l); got != 180 {
				t.Errorf("method %d: row %d is %d cells", method, i, got)
			}
		}
	}
}

func TestFillRepairsWideChars(t *testing.T) {
	c := newCanvas(10, 1)
	c.text(0, 0, 10, "中中中中中", Style{})
	c.fill(3, 0, 4, 1, none) // cuts the characters at cells 2-3 and 6-7
	if got := width(c.render()); got != 10 {
		t.Fatalf("the row is %d cells", got)
	}
	c = newCanvas(10, 1)
	c.text(0, 0, 10, "a中中中中", Style{})
	c.text(0, 0, 10, "中", Style{}) // covers the first half of a wide character
	if got := width(c.render()); got != 10 {
		t.Fatalf("after an overwrite, the row is %d cells", got)
	}

	m, dir := fixture(t)
	p := filepath.Join(dir, "cjk.txt")
	var b strings.Builder
	for i := range 60 {
		if i%2 == 1 {
			b.WriteString("a")
		}
		b.WriteString(strings.Repeat("中文", 40) + "\n")
	}
	writeFile(t, p, b.String())
	m.openFile(p)
	m.key("?")
	checkFrame(t, m, 180, 50, "help-cjk")
}

// splitsFixture adds n agents with one log entry each to the fixture.
func splitsFixture(t *testing.T, n int) *Model {
	m, dir := fixture(t)
	agents := append([]watch.Agent(nil), m.snap.Agents...)
	now := time.Now()
	for k := range n {
		agents = append(agents, watch.Agent{
			Key: fmt.Sprint("x:", k), Source: "claude", Name: "claude", Cwd: dir, Started: now.Add(time.Duration(k) * time.Second), Updated: now,
			Events: []watch.Event{
				{Time: now.Add(-2 * time.Second), Kind: watch.KindYou, Text: "older entry"},
				{Time: now.Add(-time.Second), Kind: watch.KindSay, Text: fmt.Sprint("NEWEST", k)},
			},
		})
	}
	m.Update(Snapshot(watch.Snapshot{Agents: agents}))
	m.key("s")
	return m
}

func TestShortSplitPanesStayInside(t *testing.T) {
	// 7 agents in 16 rows: panes of 4 rows show 1 log row, the newest.
	m := splitsFixture(t, 4)
	out := sgrRe.ReplaceAllString(checkFrame(t, m, 120, 16, "splits-16"), "")
	for k := range 4 {
		if !strings.Contains(out, fmt.Sprint("NEWEST", k)) {
			t.Errorf("the pane of x:%d does not show its newest entry:\n%s", k, out)
		}
	}
	// 9 agents in 12 rows: panes of 3 rows have no room for the log.
	m = splitsFixture(t, 6)
	m.msg = note{text: "hi"}
	rows := plainRows(checkFrame(t, m, 120, 12, "splits-12"))
	if strings.Contains(strings.Join(rows, "\n"), "NEWEST") {
		t.Fatalf("log rows go out of their panes:\n%s", strings.Join(rows, "\n"))
	}
	if got := strings.Join(strings.Fields(rows[11]), " "); got != "hi ? keys" {
		t.Fatalf("the message line shows text of a pane: %q", rows[11])
	}
}

func TestHighlightRunsInCommand(t *testing.T) {
	m, dir := fixture(t)
	main := filepath.Join(dir, "main.go")
	m.View()
	h := m.hl[main]
	if h == nil || !h.plain {
		t.Fatalf("View highlights the file: %+v", h)
	}
	version := h.version
	// The fixture drops the commands of Update, so the highlight that it
	// started does not end.
	m.hlBusy = map[string]int{}
	cmd := m.highlightCmds()
	if cmd == nil {
		t.Fatal("no highlight command for the shown file")
	}
	msg, ok := cmd().(hlMsg)
	if !ok || msg.version != version {
		t.Fatalf("result: %T %+v", msg, msg)
	}
	// A result for an old version goes away.
	m.Update(hlMsg{path: main, version: version - 1, lang: "old"})
	if m.hl[main].lang == "old" {
		t.Fatal("the result of an old version shows")
	}
	m.Update(msg)
	if h := m.hl[main]; h.plain || h.lang != "go" || len(m.hlBusy) != 0 {
		t.Fatalf("after the result: plain %v lang %q busy %v", h.plain, h.lang, m.hlBusy)
	}
	if m.highlightCmds() != nil {
		t.Fatal("a second highlight starts for the same version")
	}
}

func TestEscapeSequences(t *testing.T) {
	m, dir := fixture(t)
	s := runBy(m, 0, "go test ./...")
	a := &s.Agents[0]
	e := &a.Events[len(a.Events)-1]
	e.Output = []string{
		"\x1b[31mFAIL\x1b[0m demo/pkg 0.2s",
		"see \x1b]8;;http://x.test\x1b\\the link\x1b]8;;\x1b\\ \x1b[2K\x1b(Bnow",
		"tests \x1b[1;32mok\x1b[m done",
		"\u009b31mC1 text",
	}
	e.Result = watch.Failed
	m.Update(Snapshot(s))
	m.pin(0)
	out := checkFrame(t, m, 180, 50, "sgr")
	plain := sgrRe.ReplaceAllString(out, "")
	for _, bad := range []string{"[31m", "]8;;", "x.test", "[2K", "(B", "31mC1"} {
		if strings.Contains(plain, bad) {
			t.Errorf("the frame shows the escape text %q", bad)
		}
	}
	for _, want := range []string{"FAIL demo/pkg", "see the link now", "C1 text"} {
		if !strings.Contains(plain, want) {
			t.Errorf("the frame does not show %q", want)
		}
	}
	if !strings.Contains(out, "\x1b[0;31mFAIL") || !strings.Contains(out, "\x1b[0;1;32mok") {
		t.Error("the terminal pane does not show the colors of the output")
	}
	if c := m.cls("\x1b[31mFAIL\x1b[0m x"); c != m.theme.Del() {
		t.Errorf("a colored FAIL line is not red: %+v", c)
	}
	// Escape sequences in file text go away.
	p := filepath.Join(dir, "log.txt")
	writeFile(t, p, "start \x1b[31mred\x1b[0m end\n")
	m.openFile(p)
	if plain := sgrRe.ReplaceAllString(checkFrame(t, m, 180, 50, "sgr-file"), ""); strings.Contains(plain, "[31m") || !strings.Contains(plain, "start red end") {
		t.Error("the file pane shows escape text")
	}
}

func TestCtrlCQuitsInHelp(t *testing.T) {
	m, _ := fixture(t)
	m.key("?")
	cmd := m.key("ctrl+c")
	if cmd == nil {
		t.Fatal("ctrl+c does nothing in the help")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c does not quit in the help")
	}
}

func TestScanChangesLoadsFewFiles(t *testing.T) {
	m, r, _ := gitFixture(t)
	var paths []string
	for i := range 200 {
		p := filepath.Join(r.Root, fmt.Sprintf("gen/f%03d.txt", i))
		writeFile(t, p, "x\n")
		paths = append(paths, p)
	}
	m.mtimes = nil
	m.Update(repoMsg(r.Refresh()))
	// A file that scanChanges loaded keeps its old lines for 300 ms. A file
	// that it did not load shows the new lines at once.
	loaded := 0
	for _, p := range paths {
		writeFile(t, p, "y\nz\n")
		if len(r.File(p).Lines) != 2 {
			loaded++
		}
	}
	if loaded > warmPerScan {
		t.Fatalf("one scan loaded %d files, want %d or fewer", loaded, warmPerScan)
	}
}

func TestStableTags(t *testing.T) {
	m, dir := fixture(t)
	now := time.Now()
	tagOf := func(key string) string {
		for i := range m.snap.Agents {
			if m.snap.Agents[i].Key == key {
				return m.tag(i)
			}
		}
		return "hidden"
	}
	// A new agent that started before the others gets the next free tag.
	agents := append([]watch.Agent{{
		Key: "claude:early", Source: "claude", Name: "claude", Cwd: dir, Started: now.Add(-time.Hour), Updated: now,
		Events: []watch.Event{{Time: now, Kind: watch.KindYou, Text: "hello"}},
	}}, m.snap.Agents...)
	// A1 is done for a long time, so that hide-done hides it.
	agents[1].TurnEnded, agents[1].Updated = true, now.Add(-time.Hour)
	m.Update(Snapshot(watch.Snapshot{Agents: agents}))
	want := map[string]string{"claude:a": "A1", "codex:b": "A2", "gemini:c": "A3", "claude:early": "A4"}
	for k, w := range want {
		if got := tagOf(k); got != w {
			t.Fatalf("%s: tag %s, want %s", k, got, w)
		}
	}
	if i := m.byNum(4); m.color(i) != m.theme.Agent(3) {
		t.Fatal("the color does not follow the tag")
	}
	m.key("2")
	m.key("x")
	if tagOf("claude:a") != "hidden" || tagOf("codex:b") != "A2" || tagOf("claude:early") != "A4" {
		t.Fatalf("hide-done moves the tags: %v", m.nums)
	}
	m.key("4")
	if m.focusKey != "claude:early" {
		t.Fatalf("4 pins %s, want the agent with tag A4", m.focusKey)
	}
	// A digit pins a hidden agent by its tag, and the agent shows again.
	m.key("1")
	if m.focusKey != "claude:a" || tagOf("claude:a") != "A1" {
		t.Fatalf("1 pins %s", m.focusKey)
	}
	m.key("7")
	if m.flash == nil || m.flash.text != "there is no agent A7" {
		t.Fatalf("7: %+v", m.flash)
	}
	// An agent that leaves the snapshot frees its tag for a new agent.
	m.Update(Snapshot(watch.Snapshot{Agents: agents[1:]}))
	m.Update(Snapshot(watch.Snapshot{Agents: append(agents[1:], watch.Agent{
		Key: "codex:new", Source: "codex", Name: "codex", Cwd: dir, Started: now, Updated: now,
	})}))
	if tagOf("codex:new") != "A4" || tagOf("codex:b") != "A2" {
		t.Fatalf("tags after a change: %v", m.nums)
	}
}

func TestKeysGiveFeedback(t *testing.T) {
	m := New(repo.Open(t.TempDir()), nil, Options{Since: time.Hour})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.Update(Snapshot(watch.Snapshot{}))
	for k, want := range map[string]string{
		"3": "there is no agent A3", "L": "there are no buffers yet", "H": "there are no buffers yet",
		"f": "there is no file to scroll", "j": "there is no file to scroll", "G": "there is no file to scroll",
		"tab": "there are no agents yet", ",": "there is no agent to step through", "e": "there is no file to open",
		"y": "there is no file to copy", "s": "there are no agents to split yet",
	} {
		m.flash = nil
		m.key(k)
		if m.flash == nil || m.flash.text != want {
			t.Errorf("%s: %+v, want %q", k, m.flash, want)
		}
	}
	m.key("]")
	m.key("c")
	if m.flash == nil || m.flash.text != "there is no file to scroll" {
		t.Errorf("]c: %+v", m.flash)
	}
	f, _ := fixture(t)
	f.pin(0)
	f.key(".")
	if f.flash == nil || f.flash.text != "there is no newer step" {
		t.Errorf(".: %+v", f.flash)
	}
}

func TestLongPathsKeepFileName(t *testing.T) {
	if got := clipLeft("internal/watch/scratchpad/cpu0", 16); got != "…scratchpad/cpu0" {
		t.Fatalf("clipLeft: %q", got)
	}
	m, dir := fixture(t)
	p := filepath.Join(dir, "internal/a/very/long/directory/name/for/this/test/scratchpad/cpu0.go")
	writeFile(t, p, "package cpu0\n")
	m.openFile(p)
	rows := plainRows(checkFrame(t, m, 80, 24, "long-path"))
	if !strings.Contains(rows[1], "scratchpad › cpu0.go") || !strings.Contains(rows[1], "…") {
		t.Errorf("the winbar loses the file name: %q", rows[1])
	}
	if !strings.Contains(rows[22], "scratchpad/cpu0.go") {
		t.Errorf("the status line loses the file name: %q", rows[22])
	}
	// The FILES list keeps the end of a long directory.
	m.Update(Snapshot(editBy(m, 0, p)))
	rows = plainRows(checkFrame(t, m, 110, 40, "long-dir"))
	if !strings.Contains(strings.Join(rows, "\n"), "▾ …") || !strings.Contains(strings.Join(rows, "\n"), "test/scratchpad/") {
		t.Errorf("the FILES list loses the end of the directory:\n%s", strings.Join(rows, "\n"))
	}
	// The empty state keeps the end of a long project path.
	root := filepath.Join(t.TempDir(), "a", "very", "long", "path", "to", "the", "project", "named", "shoulderproject")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	e := New(repo.Open(root), nil, Options{Since: time.Hour})
	e.Update(Snapshot(watch.Snapshot{}))
	if out := sgrRe.ReplaceAllString(checkFrame(t, e, 60, 12, "long-root"), ""); !strings.Contains(out, "named/shoulderproject") {
		t.Errorf("the empty state loses the project name:\n%s", out)
	}
}

func TestAgentsInTopBar(t *testing.T) {
	m, _ := fixture(t)
	m.pin(0)
	rows := plainRows(checkFrame(t, m, 80, 24, "strip"))
	if !strings.Contains(rows[0], "● A1  ○ A2  ✓ A3") {
		t.Fatalf("top bar: %q", rows[0])
	}
	// A click on an agent in the top bar pins it.
	x := strings.Index(rows[0], "○ A2")
	m.Update(tea.MouseClickMsg{X: len([]rune(rows[0][:x])), Y: 0, Button: tea.MouseLeft})
	if m.tag(m.focusIndex()) != "A2" {
		t.Fatalf("the click pins %s", m.tag(m.focusIndex()))
	}
	// With the sidebar, the top bar has no agents.
	if rows := plainRows(checkFrame(t, m, 180, 50, "no-strip")); strings.Contains(rows[0], "○ A2") {
		t.Fatalf("top bar with the sidebar: %q", rows[0])
	}
}

func TestEmptyStateHints(t *testing.T) {
	m := New(repo.Open(t.TempDir()), nil, Options{Since: time.Hour})
	m.Update(Snapshot(watch.Snapshot{}))
	out := sgrRe.ReplaceAllString(checkFrame(t, m, 120, 30, "empty-hints"), "")
	if strings.Contains(out, "free ·") {
		t.Error("the winbar shows free scroll without an agent")
	}
	for _, want := range []string{"opencode or omp", "press A to watch the agents of all projects"} {
		if !strings.Contains(out, want) {
			t.Errorf("the empty state does not show %q", want)
		}
	}
	m.Update(Snapshot(watch.Snapshot{Elsewhere: 3}))
	if out := sgrRe.ReplaceAllString(checkFrame(t, m, 120, 30, "empty-elsewhere"), ""); !strings.Contains(out, "3 agents in other projects · press A to watch them") {
		t.Error("the empty state does not count the agents of other projects")
	}
	m.key("A")
	if out := sgrRe.ReplaceAllString(checkFrame(t, m, 120, 30, "empty-all"), ""); strings.Contains(out, "press A") {
		t.Error("the hint shows when all projects show")
	}
}

func TestTabsMarkTheCut(t *testing.T) {
	m, dir := fixture(t)
	for i := range 12 {
		p := filepath.Join(dir, fmt.Sprintf("a_long_buffer_name_%02d.go", i))
		writeFile(t, p, "package x\n")
		m.tabs = append(m.tabs, p)
	}
	rows := plainRows(checkFrame(t, m, 100, 30, "tabs-cut"))
	top := []rune(rows[0])
	at := strings.Index(rows[0], m.repo.Name+" ")
	i := len([]rune(rows[0][:at]))
	if top[i-1] != ' ' || top[i-2] != '…' {
		t.Fatalf("the cut of the tabs: %q", rows[0])
	}
}
