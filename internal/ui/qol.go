package ui

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bjarneo/shoulder/internal/watch"
)

// doneGrace is how long an agent that is done stays in view when shoulder
// hides the agents that are done.
const doneGrace = time.Minute

// visible returns the snapshot without the agents that are done, when
// shoulder hides them. The focused agent stays.
func (m *Model) visible(s watch.Snapshot) watch.Snapshot {
	if !m.hideDone {
		return s
	}
	now := time.Now()
	out := watch.Snapshot{Notes: s.Notes}
	for i := range s.Agents {
		a := &s.Agents[i]
		if a.Key == m.focusKey || a.Activity(now).Status != watch.Done || now.Sub(a.Updated) < doneGrace {
			out.Agents = append(out.Agents, *a)
		}
	}
	return out
}

// hidden is the number of agents that shoulder hides.
func (m *Model) hidden() int { return len(m.full.Agents) - len(m.snap.Agents) }

// refilter applies the last snapshot again when time hides an agent that is
// done.
func (m *Model) refilter() tea.Cmd {
	if !m.hideDone {
		return nil
	}
	v := m.visible(m.full)
	same := len(v.Agents) == len(m.snap.Agents)
	for i := 0; same && i < len(v.Agents); i++ {
		same = v.Agents[i].Key == m.snap.Agents[i].Key
	}
	if same {
		return nil
	}
	return m.apply(m.full)
}

func (m *Model) toggleHideDone() tea.Cmd {
	m.hideDone = !m.hideDone
	cmd := m.apply(m.full)
	text := "showing the agents that are done"
	if m.hideDone {
		n := m.hidden()
		text = "hiding " + plural(n, "agent") + " that are done"
		if n == 1 {
			text = "hiding 1 agent that is done"
		}
	}
	return tea.Batch(cmd, m.flashNote(text, m.theme.Dim()))
}

// cursorAt returns the file and the line under the cursor of the focused
// pane, or "".
func (m *Model) cursorAt() (string, int) {
	if !m.following() && m.view != nil {
		return m.view.file, m.view.ucur
	}
	m.prepare()
	if fi := m.focusIndex(); fi >= 0 && m.poss[fi].ok {
		return m.poss[fi].file, m.poss[fi].line
	}
	return "", 0
}

// editorDone is the result of the editor.
type editorDone struct{ err error }

// editorArgs returns the command line that opens a file at a 0-based line.
// Most terminal editors take +LINE. The editors that do not take it get
// FILE:LINE.
func editorArgs(editor, file string, line int) []string {
	words := strings.Fields(editor)
	if len(words) == 0 {
		words = []string{"vi"}
	}
	n := strconv.Itoa(line + 1)
	switch filepath.Base(words[0]) {
	case "code", "code-insiders", "codium", "cursor", "windsurf":
		return append(words, "-g", file+":"+n)
	case "zed", "hx", "helix", "subl", "sublime_text":
		return append(words, file+":"+n)
	}
	return append(words, "+"+n, file)
}

// openEditor opens the file under the cursor in the editor of the user.
// shoulder gives the terminal to the editor until the editor stops.
func (m *Model) openEditor() tea.Cmd {
	file, line := m.cursorAt()
	if file == "" {
		return m.flashNote("there is no file to open", m.theme.Dim())
	}
	args := editorArgs(m.opt.Editor, file, line)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = m.repo.Root
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return editorDone{err} })
}

// copyPath puts path:line of the cursor on the clipboard of the terminal.
func (m *Model) copyPath() tea.Cmd {
	file, line := m.cursorAt()
	if file == "" {
		return m.flashNote("there is no file to copy", m.theme.Dim())
	}
	p := file
	if m.repo.Inside(file) {
		p = m.repo.Rel(file)
	}
	text := p + ":" + strconv.Itoa(line+1)
	return tea.Batch(tea.SetClipboard(text), m.flashNote("copied "+text, m.theme.Dim()))
}

// histSel is the selected event in history mode.
type histSel struct {
	key string // the agent
	id  string // the event, by eventID
	idx int    // the last known index of the event
}

// histAt returns the index of the selected event of agent ai in history
// mode, or -1.
func (m *Model) histAt(ai int) int {
	h := m.hist
	if h == nil || m.follow || ai < 0 || ai >= len(m.snap.Agents) || m.snap.Agents[ai].Key != h.key {
		return -1
	}
	a := &m.snap.Agents[ai]
	if h.idx < len(a.Events) && eventID(a, h.idx) == h.id {
		return h.idx
	}
	// New events move the index when the watcher drops old ones.
	for j := len(a.Events) - 1; j >= 0; j-- {
		if eventID(a, j) == h.id {
			h.idx = j
			return j
		}
	}
	return -1
}

// stepHistory selects the older (dir -1) or the newer (dir 1) event of the
// focused agent. A step past the newest event goes back to follow.
func (m *Model) stepHistory(dir int) tea.Cmd {
	fi := m.focusIndex()
	if fi < 0 || len(m.snap.Agents[fi].Events) == 0 {
		return nil
	}
	n := len(m.snap.Agents[fi].Events)
	j := m.histAt(fi)
	var cmd tea.Cmd
	switch {
	case j < 0 && dir > 0:
		return nil
	case j < 0:
		j = n - 1
		older, newer := m.keys.first(actHistPrev), m.keys.first(actHistNext)
		cmd = m.flashNote("history of "+tag(fi)+" · "+older+" older · "+newer+" newer · esc back to follow", m.theme.Dim())
	default:
		j += dir
	}
	if j >= n {
		m.hist, m.follow = nil, true
		return m.flashNote("back to follow", m.theme.Dim())
	}
	m.selectEvent(fi, max(0, j))
	return cmd
}

// selectEvent shows event j of agent ai: the file at a read or an edit, and
// the terminal at a command.
func (m *Model) selectEvent(ai, j int) {
	a := &m.snap.Agents[ai]
	if m.focusKey != a.Key {
		m.focusAgent(ai)
	}
	if m.following() {
		m.toNormal()
	}
	m.hist = &histSel{key: a.Key, id: eventID(a, j), idx: j}
	m.follow = false
	e := &a.Events[j]
	if !hasPosition(e) {
		return
	}
	f := m.repo.File(e.File)
	if f.Missing || len(f.Lines) == 0 {
		return
	}
	line := min(max(0, e.Line), len(f.Lines)-1)
	if e.Kind == watch.KindEdit {
		start, _, ok := m.editRange(a, j, f)
		if !ok {
			return
		}
		line = start
	}
	m.view = &freeView{file: e.File, ucur: line}
	m.scrollTo(m.view, true)
}

// awayFor is the shortest time away from the terminal that gives a
// summary when you come back.
const awayFor = 30 * time.Second

// focusBack runs when the terminal gets the focus again. After a long
// enough time away, the message line lists what the agents did.
func (m *Model) focusBack() {
	since := m.awayAt
	m.awayAt = time.Time{}
	if since.IsZero() || time.Since(since) < awayFor {
		return
	}
	m.away = since
	if text := m.awaySummary(since); text != "" {
		m.msg = note{text, m.theme.FG()}
	}
}

// awaySummary lists what the agents did since a time: the files they
// edited, the agents that wait for you and the agents that finished.
func (m *Model) awaySummary(since time.Time) string {
	m.prepare()
	var parts []string
	for i := range m.snap.Agents {
		a := &m.snap.Agents[i]
		files := map[string]bool{}
		done := false
		for j := len(a.Events) - 1; j >= 0 && a.Events[j].Time.After(since); j-- {
			switch e := &a.Events[j]; e.Kind {
			case watch.KindEdit:
				if e.File != "" {
					files[e.File] = true
				}
			case watch.KindDone:
				done = true
			}
		}
		if se, ok := m.shell[a.Key]; ok && se.at.After(since) {
			files[se.file] = true
		}
		var what []string
		if n := len(files); n > 0 {
			what = append(what, "edited "+plural(n, "file"))
		}
		switch {
		case m.acts[i].Status == watch.Waiting:
			what = append(what, waitWhy(a, m.acts[i]))
		case done && m.acts[i].Status == watch.Done:
			what = append(what, "is done")
		}
		if len(what) > 0 {
			parts = append(parts, tag(i)+" "+a.Name+" "+strings.Join(what, " and "))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "while you were away · " + strings.Join(parts, " · ")
}

// cmdPreview is the number of lines after the first that the terminal pane
// shows of a command with more lines, such as a script in a heredoc.
const cmdPreview = 4
