// Package ui draws the agent watch TUI.
package ui

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/bjarneo/shoulder/internal/repo"
	"github.com/bjarneo/shoulder/internal/watch"
)

// Options are the start settings of the UI.
type Options struct {
	Splits bool // start with one split per agent
	Signs  bool // show changes as signs instead of inline deleted lines
	All    bool // start with the agents of all projects
	Since  time.Duration
	Keys   *Keymap // nil for the default keys
}

// Model is the Bubble Tea model of the TUI.
type Model struct {
	opt     Options
	repo    *repo.Repo
	watcher *watch.Watcher
	theme   Theme
	keys    *Keymap
	side    [][2]string // the key list of the sidebar
	helpTab [][2]string // the rows of the help screen
	w, h    int
	now     time.Time

	snap   watch.Snapshot
	acts   []watch.Activity
	poss   []pos
	loaded bool

	focusKey string
	auto     bool // the focus jumps to where agents work
	all      bool // the agents of all projects show, not only of this project
	want     *jumpTo
	lastJump time.Time
	follow   bool
	view     *freeView
	splits   bool
	inline   bool
	help     bool
	pending  string
	flash    *note
	flashID  int
	msg      note

	ftop     map[string]ftop
	zones    []zone
	sweeps   map[string]*sweep
	lastRead map[string]string
	edits    map[string][2]int
	rows     map[string]*rowCache
	hl       map[string]*highlighted
	seen     map[string]time.Time
	running  map[string]bool
	runEnd   map[string]time.Time // when the result of a command showed up
	mtimes   map[string]time.Time // the changed files and their times on disk
	shell    map[string]shellEdit // by agent key
	waiting  map[string]bool      // agents that wait for the user
	tabs     []string
	files    []fileRow
	lastRows int
}

type freeView struct {
	file string
	top  int
	ucur int // line index in the file
}

type ftop struct {
	file string
	top  int
}

type note struct {
	text string
	c    Color
}

type zone struct {
	x, y, w, h int
	fn         func() tea.Cmd
}

type sweep struct {
	id    string
	start time.Time
}

type pos struct {
	ok       bool
	file     string
	line     int
	col      int
	readFrom int
	editing  bool
}

type row struct {
	line  int // -1 for a deleted line
	ghost string
}

type rowCache struct {
	version int
	inline  bool
	rows    []row
	at      []int // row index of each file line
}

type fileRow struct {
	path   string
	label  string
	dir    bool
	add    int
	del    int
	code   byte
	inRepo bool
}

type (
	snapMsg   watch.Snapshot
	repoMsg   repo.State
	tickMsg   struct{}
	animMsg   struct{}
	themeMsg  struct{}
	unflash   int
	repoAgain struct{}
)

// New creates the model. Run the watcher in the background and send its
// snapshots with Snapshot.
func New(r *repo.Repo, w *watch.Watcher, opt Options) *Model {
	keys := opt.Keys
	if keys == nil {
		keys, _ = NewKeymap(nil)
	}
	return &Model{
		opt: opt, repo: r, watcher: w, keys: keys, side: keys.sidebar(), helpTab: keys.help(),
		follow: true, auto: true, all: opt.All, splits: opt.Splits, inline: !opt.Signs,
		ftop: map[string]ftop{}, sweeps: map[string]*sweep{}, lastRead: map[string]string{},
		edits: map[string][2]int{}, rows: map[string]*rowCache{}, hl: map[string]*highlighted{},
		seen: map[string]time.Time{}, running: map[string]bool{}, runEnd: map[string]time.Time{},
		shell: map[string]shellEdit{}, waiting: map[string]bool{},
		msg: note{text: "reading agent sessions in " + r.Root + " …"},
	}
}

// Snapshot wraps a watcher snapshot as a message for the program.
func Snapshot(s watch.Snapshot) tea.Msg { return snapMsg(s) }

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.refreshRepo(), tick(), queryTheme(), themeTick())
}

func themeTick() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return themeMsg{} })
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func animate() tea.Cmd {
	return tea.Tick(33*time.Millisecond, func(time.Time) tea.Msg { return animMsg{} })
}

func queryTheme() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, tea.RequestForegroundColor, tea.Raw(paletteQuery()))
}

func (m *Model) refreshRepo() tea.Cmd {
	r := m.repo
	return func() tea.Msg { return repoMsg(r.Refresh()) }
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.KeyPressMsg:
		return m, m.key(msg.String())
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			for i := len(m.zones) - 1; i >= 0; i-- {
				z := m.zones[i]
				if msg.X >= z.x && msg.X < z.x+z.w && msg.Y >= z.y && msg.Y < z.y+z.h {
					return m, z.fn()
				}
			}
		}
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.moveU(-3)
		case tea.MouseWheelDown:
			m.moveU(3)
		}
	case snapMsg:
		return m, m.apply(watch.Snapshot(msg))
	case repoMsg:
		m.repo.Apply(repo.State(msg))
		m.buildFiles()
		m.scanChanges()
		m.prepare()
		m.autoStep()
		return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return repoAgain{} })
	case repoAgain:
		return m, m.refreshRepo()
	case tickMsg:
		// A jump that waited for the dwell time, or a focused agent that
		// finished, moves the focus here.
		m.prepare()
		m.noteWaiting(false)
		m.autoStep()
		return m, tick()
	case animMsg:
		if m.animating() {
			return m, animate()
		}
	case themeMsg:
		// Ask again so that a theme change in the terminal shows up. A
		// terminal that never replied gets no more queries.
		if m.theme.hasBG || m.theme.hasFG {
			return m, tea.Batch(queryTheme(), themeTick())
		}
	case tea.BackgroundColorMsg:
		m.theme.setBG(msg.Color)
	case tea.ForegroundColorMsg:
		m.theme.setFG(msg.Color)
	case uv.UnknownOscEvent:
		m.theme.setPalette(string(msg))
	case unflash:
		if int(msg) == m.flashID {
			m.flash = nil
		}
	}
	return m, nil
}

// apply takes a new snapshot from the watcher.
func (m *Model) apply(s watch.Snapshot) tea.Cmd {
	first := !m.loaded
	m.loaded = true
	m.now = time.Now()
	old := map[string]bool{}
	for _, a := range m.snap.Agents {
		old[a.Key] = true
	}
	m.snap = s
	m.acts = m.acts[:0]
	for i := range s.Agents {
		m.acts = append(m.acts, s.Agents[i].Activity(m.now))
	}
	if m.focusIndex() < 0 && len(s.Agents) > 0 {
		m.focusKey = m.defaultFocus()
	}
	var cmds []tea.Cmd
	newest, newestAt, newestFile := -1, time.Time{}, ""
	for i := range s.Agents {
		a := &s.Agents[i]
		label := tag(i) + " " + a.Name
		if !first && !old[a.Key] && m.now.Sub(a.Updated) < recentFor {
			m.msg = note{label + " started · " + a.Task(), m.theme.Agent(i)}
		}
		for j := range a.Events {
			e := &a.Events[j]
			k := a.Key + "\x00" + e.CallID
			if e.Kind == watch.KindRun && e.CallID != "" {
				if e.Result == watch.Pending {
					m.running[k] = true
				} else if m.running[k] {
					delete(m.running, k)
					m.runEnd[k] = m.now
					if !first {
						c := m.theme.Dim()
						switch e.Result {
						case watch.Failed:
							c = m.theme.Del()
						case watch.Warned:
							c = m.theme.Syn(rTy)
						}
						m.msg = note{label + " · " + clip(firstLine(e.Cmd), 60) + " → " + e.Summary(), c}
					}
				}
			}
			if !e.Time.After(m.seen[a.Key]) {
				continue
			}
			// An agent that shows up later, such as after a switch to all
			// projects, brings old events. Only recent events count.
			if !first && m.now.Sub(e.Time) < recentFor {
				switch e.Kind {
				case watch.KindDone:
					m.msg = note{label + " done · " + e.Text, m.theme.Agent(i)}
				case watch.KindAsk:
					m.msg = note{label + " asks you · " + e.Text, m.theme.Agent(i)}
				case watch.KindEdit:
					if e.File != "" && m.inScope(e.File) && e.Time.After(newestAt) {
						newest, newestAt, newestFile = i, e.Time, e.File
					}
				}
			}
		}
		if n := len(a.Events); n > 0 && a.Events[n-1].Time.After(m.seen[a.Key]) {
			m.seen[a.Key] = a.Events[n-1].Time
		}
		// A new read starts the sweep of the read cursor.
		if c := m.acts[i].Cur; c >= 0 && a.Events[c].Kind == watch.KindRead {
			id := eventID(a, c)
			if m.lastRead[a.Key] != id {
				m.lastRead[a.Key] = id
				if !first && m.now.Sub(a.Events[c].Time) < 10*time.Second {
					m.sweeps[a.Key] = &sweep{id: id, start: m.now}
				}
			}
		}
	}
	if first {
		m.msg = m.welcome()
	}
	if len(s.Notes) > 0 && first {
		cmds = append(cmds, m.flashNote(strings.Join(s.Notes, " · "), m.theme.Del()))
	}
	m.prepare()
	if newest >= 0 {
		m.want = &jumpTo{key: s.Agents[newest].Key, why: "edits " + m.rel(newestFile)}
	}
	m.noteWaiting(first)
	m.autoStep()
	m.buildTabs()
	m.buildFiles()
	if m.animating() {
		cmds = append(cmds, animate())
	}
	return tea.Batch(cmds...)
}

func (m *Model) welcome() note {
	n := len(m.snap.Agents)
	if n == 0 {
		if m.all {
			return note{"no agent sessions in the last " + durText(m.opt.Since), m.theme.Dim()}
		}
		return note{"no agent sessions in " + m.repo.Root + " in the last " + durText(m.opt.Since) + " · start claude, codex, gemini or opencode here", m.theme.Dim()}
	}
	where := ""
	if m.repo.State.Branch != "" {
		where = " on " + m.repo.State.Branch
	}
	text := m.repo.Name + " · " + plural(n, "agent") + where
	if m.all {
		text = "all projects · " + plural(n, "agent")
	}
	if k := m.keys.first(actHelp); k != "" {
		text += " · " + k + " for keys"
	}
	return note{text, m.theme.Dim()}
}

// defaultFocus picks the agent with the newest edit in the project, or else
// the agent with the most recent activity.
func (m *Model) defaultFocus() string {
	if i, _ := m.latestEdit(); i >= 0 {
		return m.snap.Agents[i].Key
	}
	best := 0
	for i, a := range m.snap.Agents {
		if a.Updated.After(m.snap.Agents[best].Updated) {
			best = i
		}
	}
	return m.snap.Agents[best].Key
}

// latestEdit returns the agent with the newest edit of a project file and
// that file, or -1.
func (m *Model) latestEdit() (int, string) {
	best, at, file := -1, time.Time{}, ""
	for i := range m.snap.Agents {
		a := &m.snap.Agents[i]
		for j := len(a.Events) - 1; j >= 0; j-- {
			e := &a.Events[j]
			if e.Kind == watch.KindEdit && e.File != "" && m.inScope(e.File) {
				if e.Time.After(at) {
					best, at, file = i, e.Time, e.File
				}
				break
			}
		}
	}
	return best, file
}

// recentFor is the age of the events that start messages and jumps.
const recentFor = 30 * time.Second

// autoDwell is the shortest time between two jumps of auto focus, so that
// agents that work at the same time do not make the view flicker.
const autoDwell = 1500 * time.Millisecond

// jumpTo is a jump of auto focus that waits for the dwell time.
type jumpTo struct {
	key string
	why string
}

// autoStep moves the focus to where the agents work, when auto focus is on
// and the view follows. A new edit comes first. When the focused agent is
// done, the focus moves to an agent that still works in the project.
func (m *Model) autoStep() {
	if !m.auto || !m.following() || len(m.snap.Agents) == 0 {
		m.want = nil
		return
	}
	if time.Since(m.lastJump) < autoDwell {
		return
	}
	fi := m.focusIndex()
	if w := m.want; w != nil {
		m.want = nil
		for i, a := range m.snap.Agents {
			if a.Key == w.key && i != fi {
				m.jump(i, w.why)
			}
		}
		return
	}
	if fi >= 0 && m.acts[fi].Status != watch.Done {
		return
	}
	// An agent that works comes before an agent that waits for the user.
	best, rank := -1, 0
	for i := range m.snap.Agents {
		r := 0
		switch {
		case m.acts[i].Status == watch.Active && m.poss[i].ok && m.inScope(m.poss[i].file):
			r = 2
		case m.acts[i].Status == watch.Waiting && m.now.Sub(m.snap.Agents[i].Updated) < waitRecent:
			r = 1
		}
		if r == 0 {
			continue
		}
		if r > rank || (r == rank && m.snap.Agents[i].Updated.After(m.snap.Agents[best].Updated)) {
			best, rank = i, r
		}
	}
	if best >= 0 && best != fi {
		why := m.acts[best].Act + " " + m.rel(m.poss[best].file)
		if rank == 1 {
			why = waitWhy(&m.snap.Agents[best], m.acts[best])
		}
		m.jump(best, why)
	}
}

// waitRecent is the age of a wait that auto focus goes to. An agent that
// waits for hours is not news.
const waitRecent = 2 * time.Minute

// noteWaiting asks auto focus to go to an agent that starts to wait for the
// user: it asks a question, or a tool call waits for approval.
func (m *Model) noteWaiting(first bool) {
	for i := range m.snap.Agents {
		a := &m.snap.Agents[i]
		w := m.acts[i].Status == watch.Waiting
		if w && !m.waiting[a.Key] && !first && m.now.Sub(a.Updated) < waitRecent {
			m.want = &jumpTo{key: a.Key, why: waitWhy(a, m.acts[i])}
		}
		m.waiting[a.Key] = w
	}
}

func waitWhy(a *watch.Agent, act watch.Activity) string {
	if c := act.Cur; c >= 0 && a.Events[c].Kind == watch.KindAsk {
		return "asks you"
	}
	for i := len(a.Events) - 1; i >= 0; i-- {
		if e := &a.Events[i]; e.Kind == watch.KindAsk && e.Result == watch.Pending {
			return "asks you"
		}
	}
	return "waits for your approval"
}

// shellEdit is a change on disk that no edit tool call explains. It
// happened while the agent ran a command, such as sed -i or a script.
type shellEdit struct {
	file string
	at   time.Time // the time of the file on disk
	seen time.Time // when shoulder saw the change
}

// maxScan is the largest number of changed files that scanChanges checks.
const maxScan = 200

// scanChanges looks for changed files in git that changed on disk since the
// last scan. A change that no edit tool call explains goes to the agent that
// ran a command at that time, so that the view follows edits through the
// shell.
func (m *Model) scanChanges() {
	first := m.mtimes == nil
	if first {
		m.mtimes = map[string]time.Time{}
	}
	if len(m.repo.State.Changes) > maxScan {
		return
	}
	for rel, c := range m.repo.State.Changes {
		if c.Code == 'D' {
			continue
		}
		p := filepath.Join(m.repo.Root, filepath.FromSlash(rel))
		seen := time.Now()
		f := m.repo.File(p)
		at := f.ModTime()
		if f.Missing || at.IsZero() {
			continue
		}
		prev, known := m.mtimes[p]
		m.mtimes[p] = at
		if first || (known && !at.After(prev)) || seen.Sub(at) > recentFor {
			continue
		}
		// A command that changes more files ends at the newest change.
		if i := m.shellAgent(p, at); i >= 0 {
			key := m.snap.Agents[i].Key
			if old, ok := m.shell[key]; !ok || at.After(old.at) {
				m.shell[key] = shellEdit{file: p, at: at, seen: seen}
				m.want = &jumpTo{key: key, why: "edits " + m.rel(p)}
			}
		}
	}
}

// shellAgent returns the agent whose command changed a file at the time at:
// of the commands that ran at that time, the one that started last. It
// returns -1 when an edit tool call explains the change.
func (m *Model) shellAgent(p string, at time.Time) int {
	best, bestStart := -1, time.Time{}
	for i := range m.snap.Agents {
		a := &m.snap.Agents[i]
		if a.Cwd != "" && !m.repo.Inside(a.Cwd) && !strings.HasPrefix(p, a.Cwd+string(filepath.Separator)) {
			continue
		}
		for j := len(a.Events) - 1; j >= 0; j-- {
			e := &a.Events[j]
			if e.Time.Before(at.Add(-2 * time.Minute)) {
				break
			}
			if e.Kind == watch.KindEdit && e.File == p {
				return -1
			}
			if e.Kind != watch.KindRun || e.Time.After(at.Add(time.Second)) {
				continue
			}
			end, ok := m.runEnd[a.Key+"\x00"+e.CallID]
			switch {
			case e.Result == watch.Pending:
				end = time.Now()
			case ok:
			case j+1 < len(a.Events):
				end = a.Events[j+1].Time
			default:
				end = e.Time.Add(10 * time.Second)
			}
			if at.After(end.Add(2*time.Second)) || !e.Time.After(bestStart) {
				continue
			}
			best, bestStart = i, e.Time
		}
	}
	return best
}

// shellPos is the position of an agent in a file that its command changed,
// when the change is newer than the last read or edit of the agent.
func (m *Model) shellPos(i int) (pos, bool) {
	a := &m.snap.Agents[i]
	se, ok := m.shell[a.Key]
	if !ok {
		return pos{}, false
	}
	for j := len(a.Events) - 1; j >= 0; j-- {
		if e := &a.Events[j]; hasPosition(e) {
			if e.Time.After(se.at) {
				return pos{}, false
			}
			break
		}
	}
	f := m.repo.File(se.file)
	if f.Missing || len(f.Lines) == 0 {
		return pos{}, false
	}
	line := 0
	switch {
	case !f.LastAt.Before(se.seen):
		line = f.LastTo
	case len(f.Hunks) > 0:
		line = f.Hunks[0]
	}
	line = min(line, len(f.Lines)-1)
	t := f.Lines[line].Text
	return pos{ok: true, file: se.file, line: line, col: displayCol(t, utf8.RuneCountInString(t)), readFrom: -1, editing: m.acts[i].Act == "running"}, true
}

func (m *Model) jump(i int, why string) {
	m.focusKey = m.snap.Agents[i].Key
	m.lastJump = time.Now()
	m.msg = note{"→ " + tag(i) + " " + m.snap.Agents[i].Name + " " + why, m.theme.Agent(i)}
}

// pin focuses an agent and turns auto focus off, so the focus stays there.
func (m *Model) pin(i int) tea.Cmd {
	if i < 0 || i >= len(m.snap.Agents) {
		return nil
	}
	m.focusAgent(i)
	if !m.auto {
		return nil
	}
	m.auto, m.want = false, nil
	text := tag(i) + " is pinned"
	if k := m.keys.first(actAuto); k != "" {
		text += " · " + k + " turns auto focus on again"
	}
	return m.flashNote(text, m.theme.Dim())
}

// toggleAuto turns auto focus on or off. On, the focus goes to the newest
// edit at once.
func (m *Model) toggleAuto() tea.Cmd {
	m.auto, m.want = !m.auto, nil
	if !m.auto {
		return m.flashNote("auto focus is off · the focus stays on this agent", m.theme.Dim())
	}
	m.follow = true
	m.lastJump = time.Time{}
	m.prepare()
	if i, file := m.latestEdit(); i >= 0 {
		m.want = &jumpTo{key: m.snap.Agents[i].Key, why: "edits " + m.rel(file)}
	}
	m.autoStep()
	return m.flashNote("auto focus is on · the focus jumps to new edits", m.theme.Dim())
}

// toggleAll switches between the agents of this project and the agents of
// all projects.
func (m *Model) toggleAll() tea.Cmd {
	m.all = !m.all
	if m.watcher != nil {
		m.watcher.SetAll(m.all)
	}
	if m.all {
		return m.flashNote("watching the agents of all projects", m.theme.Dim())
	}
	return m.flashNote("watching the agents in "+m.repo.Root, m.theme.Dim())
}

// inScope reports whether a file belongs to the watched projects.
func (m *Model) inScope(p string) bool { return m.all || m.repo.Inside(p) }

// project returns the project name of an agent: the name of this project,
// or the last part of the directory of the agent.
func (m *Model) project(i int) string {
	cwd := m.snap.Agents[i].Cwd
	if cwd == "" || m.repo.Inside(cwd) {
		return m.repo.Name
	}
	return filepath.Base(cwd)
}

func (m *Model) focusIndex() int {
	for i, a := range m.snap.Agents {
		if a.Key == m.focusKey {
			return i
		}
	}
	return -1
}

func (m *Model) focusAgent(i int) {
	if i >= 0 && i < len(m.snap.Agents) {
		m.focusKey = m.snap.Agents[i].Key
		m.follow = true
	}
}

func (m *Model) following() bool { return m.follow || m.view == nil }

// prepare derives the state of each agent for the current time.
func (m *Model) prepare() {
	m.now = time.Now()
	m.acts = m.acts[:0]
	m.poss = m.poss[:0]
	for i := range m.snap.Agents {
		m.acts = append(m.acts, m.snap.Agents[i].Activity(m.now))
	}
	for i := range m.snap.Agents {
		m.poss = append(m.poss, m.position(i))
	}
}

func eventID(a *watch.Agent, i int) string {
	e := &a.Events[i]
	return a.Key + "|" + e.Time.String() + "|" + e.CallID + "|" + e.File
}

// position finds the file line of an agent: the line it reads or the end
// of the text it edited.
func (m *Model) position(i int) pos {
	if p, ok := m.shellPos(i); ok {
		return p
	}
	a, act := &m.snap.Agents[i], m.acts[i]
	cur := act.Cur
	// A file that is gone, such as a scratch file, shows nothing, and an
	// edit without a known line has no place yet. Use the last position of
	// the agent that has both.
	for cur >= 0 {
		e := &a.Events[cur]
		f := m.repo.File(e.File)
		if !f.Missing {
			if e.Kind != watch.KindEdit {
				break
			}
			if _, _, ok := m.editRange(a, cur, f); ok {
				break
			}
		}
		cur--
		for cur >= 0 && !hasPosition(&a.Events[cur]) {
			cur--
		}
	}
	if cur < 0 {
		return pos{readFrom: -1}
	}
	if cur != act.Cur {
		act.Act = "thinking"
		act.Cur = cur
	}
	e := &a.Events[cur]
	f := m.repo.File(e.File)
	p := pos{ok: true, file: e.File, readFrom: -1}
	n := len(f.Lines)
	if n == 0 {
		return p
	}
	switch e.Kind {
	case watch.KindRead:
		from := min(max(0, e.Line), n-1)
		to := e.EndLine
		if to < 0 || to >= n {
			to = min(n-1, from+1999)
		}
		to = max(from, to)
		p.line = to
		if sw := m.sweeps[a.Key]; sw != nil && sw.id == eventID(a, act.Cur) {
			if prog := sweepProgress(sw, to-from, m.now); prog < 1 {
				p.line = from + int(float64(to-from)*prog)
				p.readFrom = from
			}
		}
		if act.Act == "reading" {
			p.readFrom = from
		}
	case watch.KindEdit:
		_, end, _ := m.editRange(a, act.Cur, f)
		p.line = end
		t := f.Lines[end].Text
		p.col = displayCol(t, utf8.RuneCountInString(t))
		p.editing = act.Act == "editing"
	}
	return p
}

func hasPosition(e *watch.Event) bool {
	return e.File != "" && (e.Kind == watch.KindRead || e.Kind == watch.KindEdit) && !e.Deleted
}

func sweepDur(n int) time.Duration {
	return min(1200*time.Millisecond, max(250*time.Millisecond, time.Duration(n)*12*time.Millisecond))
}

func sweepProgress(sw *sweep, n int, now time.Time) float64 {
	return min(1, float64(now.Sub(sw.start))/float64(sweepDur(n)))
}

func (m *Model) animating() bool {
	busy := false
	for k, sw := range m.sweeps {
		if time.Since(sw.start) < 1300*time.Millisecond {
			busy = true
		} else {
			delete(m.sweeps, k)
		}
	}
	return busy
}

// editRange finds the lines of an edit in the current file. It looks for the
// inserted text near the line that the agent reported, because later edits
// move lines.
// It reports false when the edit has no inserted text in the file and no
// line number yet, such as a deletion before its result arrives.
func (m *Model) editRange(a *watch.Agent, i int, f *repo.File) (int, int, bool) {
	e := &a.Events[i]
	key := eventID(a, i) + "|" + strconv.Itoa(e.Line) + "|" + strconv.Itoa(f.Version)
	if r, ok := m.edits[key]; ok {
		return r[0], r[1], true
	}
	if len(m.edits) > 2000 {
		m.edits = map[string][2]int{}
	}
	n := len(f.Lines)
	start, end := -1, -1
	needle := strings.Split(strings.TrimSuffix(e.Needle, "\n"), "\n")
	probe := -1
	for j, l := range needle {
		if len(strings.TrimSpace(l)) >= 3 {
			probe = j
			break
		}
	}
	if probe >= 0 {
		want := strings.TrimSpace(needle[probe])
		best := -1
		for j := range f.Lines {
			if strings.TrimSpace(f.Lines[j].Text) != want {
				continue
			}
			if best < 0 || (e.Line >= 0 && abs(j-probe-e.Line) < abs(best-probe-e.Line)) {
				best = j
			}
		}
		if best >= 0 {
			start = best - probe
			end = start + len(needle) - 1
		}
	}
	if start < 0 && e.Line >= 0 {
		start, end = e.Line, max(e.Line, e.EndLine)
	}
	if start < 0 || n == 0 {
		return 0, 0, false
	}
	start = min(start, n-1)
	end = min(max(start, end), n-1)
	m.edits[key] = [2]int{start, end}
	return start, end, true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// rowsOf lays out the rows of a file: its lines and, in the inline style,
// the deleted lines.
func (m *Model) rowsOf(f *repo.File) *rowCache {
	rc := m.rows[f.Path]
	if rc != nil && rc.version == f.Version && rc.inline == m.inline {
		return rc
	}
	rc = &rowCache{version: f.Version, inline: m.inline, at: make([]int, len(f.Lines))}
	for i := range f.Lines {
		l := &f.Lines[i]
		if m.inline {
			for _, g := range l.Above {
				rc.rows = append(rc.rows, row{line: -1, ghost: g})
			}
			if l.State == repo.Changed {
				rc.rows = append(rc.rows, row{line: -1, ghost: l.Old})
			}
		}
		rc.at[i] = len(rc.rows)
		rc.rows = append(rc.rows, row{line: i})
		if m.inline {
			for _, g := range l.Below {
				rc.rows = append(rc.rows, row{line: -1, ghost: g})
			}
		}
	}
	m.rows[f.Path] = rc
	return rc
}

func (m *Model) highlightOf(f *repo.File) *highlighted {
	h := m.hl[f.Path]
	if h != nil && h.version == f.Version {
		return h
	}
	lines := make([]string, len(f.Lines))
	for i := range f.Lines {
		lines[i] = f.Lines[i].Text
	}
	spans, lang := highlight(f.Path, lines)
	h = &highlighted{version: f.Version, lang: lang, lines: spans}
	m.hl[f.Path] = h
	return h
}

// buildTabs lists the buffers: files that agents edited and the files that
// agents are on, in the order the agents first touched them.
func (m *Model) buildTabs() {
	first := map[string]time.Time{}
	note := func(p string, t time.Time) {
		if p == "" || !m.inScope(p) || m.repo.File(p).Missing {
			return
		}
		if old, ok := first[p]; !ok || t.Before(old) {
			first[p] = t
		}
	}
	for i := range m.snap.Agents {
		a := &m.snap.Agents[i]
		for j := range a.Events {
			if e := &a.Events[j]; e.Kind == watch.KindEdit && !e.Deleted {
				note(e.File, e.Time)
			}
		}
		if c := m.acts[i].Cur; c >= 0 && m.acts[i].Status != watch.Done {
			note(a.Events[c].File, a.Events[c].Time)
		}
	}
	m.tabs = m.tabs[:0]
	for p := range first {
		m.tabs = append(m.tabs, p)
	}
	sort.Slice(m.tabs, func(i, j int) bool {
		a, b := first[m.tabs[i]], first[m.tabs[j]]
		if !a.Equal(b) {
			return a.Before(b)
		}
		return m.tabs[i] < m.tabs[j]
	})
}

// buildFiles lists the changed files and the files that agents work on. In
// git, the status gives the changed files. Outside git, the files that
// agents edited stand in for them.
func (m *Model) buildFiles() {
	set := map[string]bool{}
	for rel := range m.repo.State.Changes {
		set[filepath.Join(m.repo.Root, rel)] = true
	}
	for i := range m.snap.Agents {
		a := &m.snap.Agents[i]
		for j := range a.Events {
			// Outside git, and in other projects, the edits stand in for
			// the git changes.
			if e := &a.Events[j]; e.Kind == watch.KindEdit && e.File != "" && (!m.repo.Git || !m.repo.Inside(e.File)) && !m.repo.File(e.File).Missing {
				set[e.File] = true
			}
		}
		if i < len(m.acts) {
			if c := m.acts[i].Cur; c >= 0 && m.acts[i].Status != watch.Done {
				set[a.Events[c].File] = true
			}
		}
	}
	var paths []string
	for p := range set {
		if m.inScope(p) {
			paths = append(paths, p)
		}
	}
	sort.Slice(paths, func(i, j int) bool { return m.rel(paths[i]) < m.rel(paths[j]) })
	m.files = m.files[:0]
	lastDir := "."
	for _, p := range paths {
		rel := m.rel(p)
		inside := m.repo.Inside(p)
		dir := filepath.Dir(rel)
		if dir != "." && dir != lastDir {
			m.files = append(m.files, fileRow{dir: true, label: "▾ " + dir + "/"})
		}
		lastDir = dir
		r := fileRow{path: p, label: filepath.Base(rel), inRepo: inside}
		if dir != "." {
			r.label = "  " + r.label
		}
		if c, ok := m.repo.State.Changes[filepath.ToSlash(rel)]; ok && inside {
			r.add, r.del, r.code = c.Add, c.Del, c.Code
		} else if !m.repo.Git || !inside {
			f := m.repo.File(p)
			r.add, r.del = f.Add+f.Mod, f.Del
			switch {
			case f.Created:
				r.code = 'A'
			case f.Changed():
				r.code = 'M'
			}
		}
		m.files = append(m.files, r)
	}
}

func (m *Model) flashNote(text string, c Color) tea.Cmd {
	m.flashID++
	m.flash = &note{text, c}
	id := m.flashID
	return tea.Tick(3500*time.Millisecond, func(time.Time) tea.Msg { return unflash(id) })
}

// key handles one key press. A key that starts a longer sequence waits in
// m.pending for the next key.
func (m *Model) key(k string) tea.Cmd {
	if m.help {
		if a := m.keys.seqs[k]; k == "esc" || a == actHelp || a == actQuit {
			m.help = false
		}
		return nil
	}
	if k == "ctrl+c" {
		return tea.Quit
	}
	if m.pending == "" && len(k) == 1 && k[0] >= '1' && k[0] <= '9' {
		return m.pin(int(k[0] - '1'))
	}
	seq := k
	if m.pending != "" {
		seq = m.pending + " " + k
	}
	m.pending = ""
	if a, ok := m.keys.seqs[seq]; ok {
		return m.do(a)
	}
	if m.keys.pre[seq] {
		m.pending = seq
	}
	return nil
}

// do runs an action.
func (m *Model) do(a action) tea.Cmd {
	n := len(m.snap.Agents)
	switch a {
	case actPageDown:
		m.moveU(m.codeRows() / 2)
	case actPageUp:
		m.moveU(-m.codeRows() / 2)
	case actAgentNext:
		if n > 0 {
			return m.pin((max(0, m.focusIndex()) + 1) % n)
		}
	case actAgentPrev:
		if n > 0 {
			return m.pin((max(0, m.focusIndex()) + n - 1) % n)
		}
	case actAuto:
		return m.toggleAuto()
	case actAllProjects:
		return m.toggleAll()
	case actFollow:
		if m.following() {
			m.toNormal()
		} else {
			m.follow = true
		}
	case actBack:
		m.follow = true
	case actDown:
		m.moveU(1)
	case actUp:
		m.moveU(-1)
	case actTop:
		m.moveU(-1 << 30)
	case actBottom:
		m.moveU(1 << 30)
	case actHunkNext:
		return m.hunk(1)
	case actHunkPrev:
		return m.hunk(-1)
	case actBufferNext:
		return m.cycleBuf(1)
	case actBufferPrev:
		return m.cycleBuf(-1)
	case actSplits:
		m.ftop = map[string]ftop{}
		m.splits = !m.splits
	case actInlineDiff:
		m.inline = !m.inline
		if m.view != nil {
			m.scrollTo(m.view, true)
		}
	case actReload:
		m.watcher.Reload()
		m.follow = true
		return m.flashNote("reading all sessions again", m.theme.Dim())
	case actHelp:
		m.help = true
	case actQuit:
		return tea.Quit
	}
	return nil
}

// codeRows is the height of the focused code pane in the last frame.
func (m *Model) codeRows() int { return max(4, m.lastRows) }

func (m *Model) termRows(h int) int {
	if h < 16 {
		return 0
	}
	return min(11, max(3, h/4))
}

// toNormal leaves follow mode at the position of the focused agent.
func (m *Model) toNormal() {
	if !m.following() {
		return
	}
	m.prepare()
	fi := m.focusIndex()
	file, line := "", 0
	if fi >= 0 && m.poss[fi].ok {
		file, line = m.poss[fi].file, m.poss[fi].line
	} else if len(m.tabs) > 0 {
		file = m.tabs[0]
	}
	if file == "" {
		return
	}
	key := "main"
	if m.splits {
		key = "p" + strconv.Itoa(fi)
	}
	top := 0
	if ft, ok := m.ftop[key]; ok && ft.file == file {
		top = ft.top
	}
	m.view = &freeView{file: file, top: top, ucur: line}
	m.follow = false
}

func (m *Model) openFile(p string) {
	m.view = &freeView{file: p}
	m.follow = false
}

func (m *Model) scrollTo(v *freeView, center bool) {
	f := m.repo.File(v.file)
	if len(f.Lines) == 0 {
		v.top, v.ucur = 0, 0
		return
	}
	rc := m.rowsOf(f)
	r, R := rc.at[v.ucur], m.codeRows()
	switch {
	case center:
		v.top = max(0, r-R/3)
	case r < v.top+2:
		v.top = max(0, r-2)
	case r > v.top+R-3:
		v.top = r - R + 3
	}
}

func (m *Model) moveU(d int) {
	m.toNormal()
	v := m.view
	if v == nil {
		return
	}
	f := m.repo.File(v.file)
	v.ucur = max(0, min(len(f.Lines)-1, v.ucur+d))
	m.scrollTo(v, false)
}

func (m *Model) hunk(dir int) tea.Cmd {
	m.toNormal()
	v := m.view
	if v == nil {
		return nil
	}
	f := m.repo.File(v.file)
	rel := m.repo.Rel(v.file)
	t := -1
	if dir > 0 {
		for _, h := range f.Hunks {
			if h > v.ucur {
				t = h
				break
			}
		}
	} else {
		for i := len(f.Hunks) - 1; i >= 0; i-- {
			if f.Hunks[i] < v.ucur {
				t = f.Hunks[i]
				break
			}
		}
	}
	if t < 0 {
		where := "below"
		if dir < 0 {
			where = "above"
		}
		return m.flashNote("no more hunks "+where+" in "+rel, m.theme.Dim())
	}
	v.ucur = t
	m.scrollTo(v, true)
	idx := 0
	for i, h := range f.Hunks {
		if h == t {
			idx = i + 1
		}
	}
	return m.flashNote("hunk "+strconv.Itoa(idx)+" of "+strconv.Itoa(len(f.Hunks))+" · "+rel, m.theme.Dim())
}

func (m *Model) curFile() string {
	if m.following() {
		if fi := m.focusIndex(); fi >= 0 && fi < len(m.poss) {
			return m.poss[fi].file
		}
		return ""
	}
	return m.view.file
}

func (m *Model) cycleBuf(d int) tea.Cmd {
	if len(m.tabs) == 0 {
		return nil
	}
	m.prepare()
	i := -1
	cur := m.curFile()
	for j, p := range m.tabs {
		if p == cur {
			i = j
		}
	}
	if i < 0 && d < 0 {
		i = 0
	}
	m.openFile(m.tabs[(i+d+len(m.tabs))%len(m.tabs)])
	return nil
}

func tag(i int) string { return "A" + strconv.Itoa(i+1) }

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return strconv.Itoa(n) + " " + w + "s"
}

func durText(d time.Duration) string {
	if d%time.Hour == 0 {
		return strconv.Itoa(int(d.Hours())) + "h"
	}
	return strconv.Itoa(int(d.Minutes())) + "m"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
