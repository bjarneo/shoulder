package ui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/bjarneo/shoulder/internal/repo"
	"github.com/bjarneo/shoulder/internal/watch"
)

// paneInfo describes what a code pane shows, for the status line.
type paneInfo struct {
	file   string
	f      *repo.File
	top    int
	total  int
	rows   int
	follow bool
	agent  int
	ucur   int
}

func (m *Model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "shoulder · " + m.repo.Name
	if m.w == 0 || m.h == 0 {
		return v
	}
	m.prepare()
	m.zones = m.zones[:0]
	c := newCanvas(m.w, m.h)
	if m.w < 60 || m.h < 12 {
		c.text(1, 0, m.w, "shoulder needs a terminal of at least 60×12 cells", Style{FG: m.theme.Dim()})
		v.Content = c.render()
		return v
	}
	var fp paneInfo
	if m.splits && len(m.snap.Agents) > 0 {
		fp = m.drawSplits(c, 1, m.h-3)
	} else {
		fp = m.drawFollow(c, 1, m.h-3)
	}
	m.drawTabs(c, fp.file)
	m.drawStatus(c, m.h-2, fp)
	m.drawMsg(c, m.h-1)
	if m.help {
		m.drawHelp(c)
	}
	v.Content = c.render()
	return v
}

func (m *Model) addZone(x, y, w, h int, fn func() tea.Cmd) {
	m.zones = append(m.zones, zone{x, y, w, h, fn})
}

// header draws a bold panel title with an optional right-aligned note.
func (m *Model) header(c *Canvas, x, y, w int, title, right string) {
	t := &m.theme
	c.text(x+1, y, x+w-1, title, Style{FG: t.Dim(), Bold: true})
	if right != "" {
		c.text(x+w-1-width(right), y, x+w-1, right, Style{FG: t.Dim()})
	}
}

func (m *Model) vline(c *Canvas, x, y, h int, bg Color) {
	for yy := y; yy < y+h; yy++ {
		c.put(x, yy, "│", 1, Style{FG: m.theme.Border(), BG: bg})
	}
}

// sidebarWidths returns the widths of the agent and activity panels.
func sidebarWidths(w int) (int, int) {
	left, right := 0, 0
	if w >= 100 {
		left = 32
	}
	switch {
	case w >= 160:
		right = 46
	case w >= 130:
		right = 38
	}
	return left, right
}

func (m *Model) drawFollow(c *Canvas, y0, h int) paneInfo {
	t := &m.theme
	lw, rw := sidebarWidths(m.w)
	x := 0
	if lw > 0 {
		m.drawSidebar(c, 0, y0, lw, h)
		m.vline(c, lw, y0, h, t.Sunk())
		x = lw + 1
	}
	cw := m.w - x
	if rw > 0 {
		cw -= rw + 1
	}
	termH := m.termRows(h)
	codeH := h - 1
	if termH > 0 {
		codeH -= termH + 1
	}
	fi := m.focusIndex()
	fol := m.following()
	info := m.drawCode(c, "main", fi, fol, x, y0+1, cw, codeH)
	m.drawWinbar(c, x, y0, cw, info, fi, fol, false)
	if termH > 0 {
		m.drawTerminal(c, x, y0+1+codeH, cw, termH+1, fi)
	}
	if rw > 0 {
		m.vline(c, m.w-rw-1, y0, h, t.Sunk())
		m.drawActivity(c, m.w-rw, y0, rw, h, fi)
	}
	return info
}

func (m *Model) actLabel(i int) string {
	if m.acts[i].Status == watch.Waiting {
		return "waiting on you"
	}
	return m.acts[i].Act
}

func (m *Model) stateColor(i int) Color {
	switch m.acts[i].Status {
	case watch.Done:
		if m.acts[i].Act == "idle" {
			return m.theme.Dim()
		}
		return m.theme.Add()
	case watch.Waiting:
		return m.theme.Dim()
	}
	return m.theme.Agent(i)
}

// drawWinbar draws the title row of a code pane.
func (m *Model) drawWinbar(c *Canvas, x, y, w int, info paneInfo, ai int, fol, split bool) {
	t := &m.theme
	bg := t.Raised()
	if split && m.focusIndex() != ai {
		bg = defColor
	}
	c.fill(x, y, w, 1, bg)
	state, stateC := "free · esc to follow", t.Dim()
	if fol && ai >= 0 {
		state, stateC = m.actLabel(ai), m.stateColor(ai)
	}
	right := x + w - 1 - width(state)
	c.text(right, y, x+w-1, state, Style{FG: stateC})
	cx := x + 1
	if ai >= 0 && (fol || split) {
		cx = c.text(cx, y, right-1, tag(ai), Style{FG: t.Agent(ai), Bold: true}) + 1
	}
	if split && ai >= 0 {
		cx = c.text(cx, y, right-1, m.snap.Agents[ai].Name, Style{FG: t.FG()}) + 1
	}
	if info.file != "" {
		crumbs := m.rel(info.file)
		if !split {
			crumbs = strings.ReplaceAll(strings.TrimPrefix(crumbs, "/"), "/", " › ")
		}
		switch {
		case info.f != nil && info.f.Created:
			crumbs += "  [new]"
		case info.f != nil && info.f.Changed():
			crumbs += "  [+]"
		}
		fg := t.FG()
		if split {
			fg = t.Dim()
		}
		c.text(cx, y, right-1, clip(crumbs, right-1-cx), Style{FG: fg})
	}
}

// rel returns a path relative to the project, or with ~ for the home
// directory.
func (m *Model) rel(p string) string {
	if m.repo.Inside(p) {
		return m.repo.Rel(p)
	}
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, h+"/") {
		return "~/" + p[len(h)+1:]
	}
	return p
}

func (m *Model) drawCode(c *Canvas, key string, ai int, fol bool, x, y, w, h int) paneInfo {
	t := &m.theme
	info := paneInfo{agent: ai, follow: fol, rows: h}
	if fol {
		if ai >= 0 && m.poss[ai].ok {
			info.file = m.poss[ai].file
		}
	} else if m.view != nil {
		info.file, info.ucur = m.view.file, m.view.ucur
	}
	if key == "main" || (ai >= 0 && ai == m.focusIndex()) {
		m.lastRows = h
	}
	if info.file == "" {
		m.drawEmpty(c, x, y, w, h, ai, key != "main")
		return info
	}
	f := m.repo.File(info.file)
	info.f = f
	switch {
	case f.Missing:
		m.centered(c, x, y, w, h, []string{"file not found", m.rel(info.file)})
		return info
	case f.Binary:
		m.centered(c, x, y, w, h, []string{"binary file", m.rel(info.file)})
		return info
	case f.TooBig:
		m.centered(c, x, y, w, h, []string{"file is too large to show", m.rel(info.file)})
		return info
	}
	rc := m.rowsOf(f)
	hl := m.highlightOf(f)
	info.total = len(rc.rows)
	n := len(f.Lines)
	if fol && n > 0 {
		ar := rc.at[min(m.poss[ai].line, n-1)]
		top := max(0, ar-h/3)
		if prev, ok := m.ftop[key]; ok && prev.file == info.file {
			top = prev.top
		}
		mt, mb := min(2, h/4), min(5, h/3)
		if ar < top+mt {
			top = max(0, ar-mt)
		} else if ar > top+h-mb {
			top = ar - h + mb
		}
		m.ftop[key] = ftop{info.file, top}
		info.top = top
	} else if !fol {
		info.top = m.view.top
	}
	type here struct {
		i int
		p pos
	}
	var hs []here
	for i := range m.snap.Agents {
		if m.acts[i].Status != watch.Done && m.poss[i].ok && m.poss[i].file == info.file {
			hs = append(hs, here{i, m.poss[i]})
		}
	}
	for r := 0; r < h; r++ {
		yy, ri := y+r, info.top+r
		if ri >= len(rc.rows) {
			c.text(x, yy, x+w, "~", Style{FG: t.Faint()})
			continue
		}
		rw := rc.rows[ri]
		if rw.line < 0 {
			c.fill(x, yy, w, 1, t.LineDel())
			c.text(x+5, yy, x+w, "▎", Style{FG: t.Del()})
			c.text(x+7, yy, x+w, expandTabs(rw.ghost, 0), Style{FG: t.Ghost()})
			continue
		}
		i := rw.line
		l := &f.Lines[i]
		bg, numC, sign, signC := none, t.Faint(), "", t.Faint()
		switch l.State {
		case repo.Added:
			sign, signC = "▎", t.Add()
		case repo.Changed:
			sign, signC = "▎", t.Mod()
		}
		if sign != "" && m.inline {
			bg = t.LineAdd()
		}
		if !m.inline && sign == "" {
			if len(l.Below) > 0 {
				sign, signC = "▁", t.Del()
			} else if len(l.Above) > 0 {
				sign, signC = "▔", t.Del()
			}
		}
		if !fol && i == info.ucur {
			if bg == none {
				bg = t.Raised()
			}
			numC = t.FG()
		}
		virt, virtC := "", none
		var curs []int
		var cursC []Color
		for _, hh := range hs {
			ac := t.Agent(hh.i)
			if hh.p.readFrom >= 0 && i >= hh.p.readFrom && i <= hh.p.line {
				numC = ac
			}
			if hh.p.line != i {
				continue
			}
			numC = ac
			if hh.p.editing {
				curs, cursC = append(curs, hh.p.col), append(cursC, ac)
			} else if tb := t.tint(ac, 0.13); tb != none {
				bg = tb
			}
			if virt != "" {
				virt += "   "
			}
			virt += "● " + tag(hh.i) + " " + m.actLabel(hh.i)
			virtC = ac
		}
		if bg != none {
			c.fill(x, yy, w, 1, bg)
		}
		c.text(x, yy, x+4, padLeft(strconv.Itoa(i+1), 4), Style{FG: numC})
		c.text(x+5, yy, x+6, sign, Style{FG: signC})
		tx, end := x+7, x+7
		if i < len(hl.lines) {
			for _, sp := range hl.lines[i] {
				end = c.text(end, yy, x+w, sp.text, Style{FG: t.Syn(sp.role), Italic: sp.role == rCom})
			}
		}
		if virt != "" {
			c.text(end+3, yy, x+w, virt, Style{FG: virtC, Italic: true})
		}
		for k, col := range curs {
			if cx := tx + col; cx < x+w {
				ac := cursC[k]
				c.paint(cx, yy, 1, func(st *Style) {
					*st = Style{FG: ac, BG: st.BG, Reverse: true}
				})
			}
		}
	}
	return info
}

func (m *Model) centered(c *Canvas, x, y, w, h int, lines []string) {
	t := &m.theme
	y0 := y + max(0, (h-len(lines))/2)
	for i, l := range lines {
		st := Style{FG: t.Dim()}
		if i == 0 {
			st = Style{FG: t.FG()}
		}
		l = clip(l, w-2)
		c.text(x+max(0, (w-width(l))/2), y0+i, x+w, l, st)
	}
}

// drawEmpty fills a code pane that has no file to show.
func (m *Model) drawEmpty(c *Canvas, x, y, w, h, ai int, split bool) {
	t := &m.theme
	type ln struct {
		s  string
		st Style
	}
	var lines []ln
	if ai < 0 {
		where, start := "shoulder watches "+m.repo.Root, "start claude, codex, gemini or opencode in this project"
		if m.all {
			where, start = "shoulder watches all projects", "start claude, codex, gemini or opencode in any project"
		}
		lines = []ln{
			{"no agents yet", Style{FG: t.FG(), Bold: true}},
			{where, Style{FG: t.Dim()}},
			{"", Style{}},
			{start, Style{FG: t.Dim()}},
			{"sessions from the last " + durText(m.opt.Since) + " show here", Style{FG: t.Faint()}},
		}
	} else {
		a := &m.snap.Agents[ai]
		if !split {
			lines = append(lines, ln{tag(ai) + " " + a.Name, Style{FG: t.Agent(ai), Bold: true}})
		}
		lines = append(lines,
			ln{m.actLabel(ai), Style{FG: t.FG()}},
			ln{"", Style{}},
			ln{a.Task(), Style{FG: t.Dim()}},
		)
	}
	y0 := y + max(0, (h-len(lines))/2)
	for i, l := range lines {
		s := clip(l.s, w-4)
		c.text(x+max(0, (w-width(s))/2), y0+i, x+w, s, l.st)
	}
}

func (m *Model) drawSidebar(c *Canvas, x, y, w, h int) {
	t := &m.theme
	c.fill(x, y, w, h, t.Sunk())
	n := len(m.snap.Agents)
	var act, wait, done int
	for _, a := range m.acts {
		switch a.Status {
		case watch.Active:
			act++
		case watch.Waiting:
			wait++
		default:
			done++
		}
	}
	m.header(c, x, y, w, "AGENTS", "●"+strconv.Itoa(act)+" ○"+strconv.Itoa(wait)+" ✓"+strconv.Itoa(done))
	keysH := len(m.side) + 2
	need := n*3 - 1
	if n == 0 {
		need = 1
	}
	showKeys := h-1-need-3 >= keysH
	bottom := y + h
	if showKeys {
		bottom -= keysH
	}
	agentsH := min(need, bottom-y-1-3)
	if len(m.files) > 0 {
		// Leave room for the files when there are many agents.
		agentsH = min(agentsH, max(5, (bottom-y-1)*3/5))
	}
	gap := 1
	if need > agentsH {
		gap = 0
	}
	per := 2 + gap
	visible := max(1, (agentsH+gap)/per)
	start := 0
	if fi := m.focusIndex(); fi >= visible {
		start = fi - visible + 1
	}
	yy := y + 1
	if n == 0 {
		c.text(x+1, yy, x+w-1, "none in the last "+durText(m.opt.Since), Style{FG: t.Faint()})
	}
	for i := start; i < n && i < start+visible; i++ {
		m.drawAgentRow(c, x, yy, w, i)
		yy += per
	}
	fy := y + 1 + agentsH + 1
	changed := 0
	for _, f := range m.files {
		if !f.dir && f.code != 0 {
			changed++
		}
	}
	if fy < bottom {
		m.header(c, x, fy, w, "FILES", strconv.Itoa(changed)+" changed")
		m.drawFiles(c, x, fy+1, w, bottom-fy-1)
	}
	if showKeys {
		m.drawKeys(c, x, bottom, w)
	}
}

func (m *Model) drawAgentRow(c *Canvas, x, y, w, i int) {
	t := &m.theme
	a, act := &m.snap.Agents[i], m.acts[i]
	ac := t.Agent(i)
	focused := i == m.focusIndex()
	if focused {
		c.fill(x, y, w, 2, t.Raised())
		c.text(x, y, x+1, "▎", Style{FG: ac})
		c.text(x, y+1, x+1, "▎", Style{FG: ac})
	}
	glyph, glyphC := "●", ac
	switch act.Status {
	case watch.Done:
		glyph, glyphC = "✓", t.Add()
		if act.Act == "idle" {
			glyphC = t.Dim()
		}
	case watch.Waiting:
		glyph, glyphC = "○", t.Dim()
	}
	label := act.Act
	right := x + w - 1 - width(label)
	c.text(right, y, x+w-1, label, Style{FG: m.stateColor(i)})
	cx := c.text(x+1, y, right, glyph, Style{FG: glyphC}) + 1
	cx = c.text(cx, y, right, tag(i), Style{FG: ac, Bold: true}) + 1
	cx = c.text(cx, y, right-1, clip(a.Name, right-1-cx), Style{FG: t.FG()}) + 1
	// The task tells apart agents of the same type. An agent in another
	// project shows the project first.
	task := a.Task()
	if a.Cwd != "" && !m.repo.Inside(a.Cwd) {
		task = m.project(i) + " · " + task
	}
	if room := right - 1 - cx; room >= 4 {
		c.text(cx, y, right-1, clip(task, room), Style{FG: t.Faint()})
	}

	bar := 0
	if a.CtxMax > 0 {
		bar = min(8, (a.Ctx*8+a.CtxMax/2)/a.CtxMax)
	}
	bx := x + w - 1 - 8
	c.text(bx, y+1, x+w-1, strings.Repeat("━", bar), Style{FG: ac})
	c.text(bx+bar, y+1, x+w-1, strings.Repeat("━", 8-bar), Style{FG: t.BarOff()})
	c.text(x+3, y+1, bx-1, clip(m.detail(i), bx-1-(x+3)), Style{FG: t.Dim()})
	m.addZone(x, y, w, 2, func() tea.Cmd { return m.pin(i) })
}

// detail is the second line of an agent row.
func (m *Model) detail(i int) string {
	act, p := m.acts[i], m.poss[i]
	switch {
	case act.Act == "reading" && p.ok:
		return m.rel(p.file) + ":" + strconv.Itoa(p.line+1)
	case act.Act == "editing" && p.ok:
		return m.rel(p.file) + ":" + strconv.Itoa(p.line+1) + ":" + strconv.Itoa(p.col+1)
	case act.Act == "thinking" && p.ok:
		return m.rel(p.file)
	}
	return act.Detail
}

func (m *Model) drawFiles(c *Canvas, x, y, w, h int) {
	t := &m.theme
	cur := m.curFile()
	rows := m.files
	more := 0
	if len(rows) > h {
		more = len(rows) - h + 1
		rows = rows[:h-1]
	}
	for k, f := range rows {
		yy := y + k
		if f.dir {
			c.text(x+1, yy, x+w-1, clip(f.label, w-2), Style{FG: t.Dim()})
			continue
		}
		if f.path == cur {
			c.fill(x, yy, w, 1, t.Raised())
		}
		stX := x + w - 2
		delX := stX - 1 - 3
		addX := delX - 1 - 4
		code, codeC := "", t.Mod()
		nameC := t.FG()
		switch f.code {
		case 'A':
			code, codeC, nameC = "A", t.Add(), t.Add()
		case 'D':
			code, codeC, nameC = "D", t.Del(), t.Del()
		case 'M', 'R':
			code, nameC = string(f.code), t.Mod()
		}
		c.text(stX, yy, stX+1, code, Style{FG: codeC})
		if f.del > 0 {
			c.text(delX, yy, delX+3, padLeft("−"+strconv.Itoa(f.del), 3), Style{FG: t.Del()})
		}
		if f.add > 0 {
			c.text(addX, yy, addX+4, padLeft("+"+strconv.Itoa(f.add), 4), Style{FG: t.Add()})
		}
		dx := addX - 1
		for i := len(m.snap.Agents) - 1; i >= 0; i-- {
			if m.acts[i].Status != watch.Done && m.poss[i].ok && m.poss[i].file == f.path && dx > x+8 {
				c.text(dx, yy, dx+1, "●", Style{FG: t.Agent(i)})
				dx--
			}
		}
		c.text(x+1, yy, dx, clip(f.label, dx-x-1), Style{FG: nameC})
		p := f.path
		m.addZone(x, yy, w, 1, func() tea.Cmd { m.openFile(p); return nil })
	}
	if more > 0 {
		c.text(x+1, y+h-1, x+w-1, "… "+strconv.Itoa(more)+" more", Style{FG: t.Faint()})
	}
}

func (m *Model) drawKeys(c *Canvas, x, y, w int) {
	t := &m.theme
	c.text(x+1, y, x+w-1, "KEYS", Style{FG: t.Dim(), Bold: true})
	for i, k := range m.side {
		c.text(x+1, y+1+i, x+11, k[0], Style{FG: t.FG()})
		c.text(x+11, y+1+i, x+w-1, k[1], Style{FG: t.Dim()})
	}
}

// termLine is one row of the terminal panel.
type termLine struct {
	pre  string
	text string
	c    Color
}

func (m *Model) drawTerminal(c *Canvas, x, y, w, h, ai int) {
	t := &m.theme
	c.fill(x, y, w, h, t.Sunk())
	cx := c.text(x+1, y, x+w, "TERMINAL", Style{FG: t.Dim(), Bold: true}) + 1
	where := m.repo.Name
	if ai >= 0 {
		cx = c.text(cx, y, x+w, tag(ai), Style{FG: t.Agent(ai), Bold: true}) + 1
		where = m.project(ai)
	}
	c.text(cx, y, x+w, "~/"+where, Style{FG: t.Dim()})
	var lines []termLine
	if ai >= 0 {
		lines = m.termLines(ai)
	}
	if len(lines) == 0 {
		msg := "no agent selected"
		if ai >= 0 {
			msg = tag(ai) + " has not run a command yet"
		}
		lines = []termLine{{text: msg, c: t.Faint()}}
	}
	rows := h - 1
	if len(lines) > rows {
		lines = lines[len(lines)-rows:]
	}
	yy := y + h - len(lines)
	for _, l := range lines {
		cx := c.text(x+1, yy, x+w-1, l.pre, Style{FG: t.Agent(max(0, ai))})
		c.text(cx, yy, x+w-1, l.text, Style{FG: l.c})
		yy++
	}
}

func (m *Model) termLines(ai int) []termLine {
	t := &m.theme
	a := &m.snap.Agents[ai]
	var out []termLine
	prompt := m.project(ai) + " $ "
	for j := range a.Events {
		e := &a.Events[j]
		if e.Cmd == "" {
			continue
		}
		out = append(out, termLine{pre: prompt, text: firstLine(e.Cmd), c: t.FG()})
		for _, l := range e.Output {
			out = append(out, termLine{text: expandTabs(l, 0), c: m.cls(l)})
		}
		if len(out) > 200 {
			out = out[len(out)-200:]
		}
	}
	if n := len(a.Events); n > 0 && m.acts[ai].Act == "running" {
		out = append(out, termLine{text: "█", c: t.Agent(ai)})
	}
	return out
}

// cls colors a line of command output.
func (m *Model) cls(s string) Color {
	t := &m.theme
	l := strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(l, "✖") || strings.Contains(l, "FAILED") || strings.Contains(l, "panicked") || strings.HasPrefix(l, "error") ||
		strings.HasPrefix(l, "FAIL") || strings.HasPrefix(l, "--- FAIL") || strings.Contains(l, "assertion failed"):
		return t.Del()
	case strings.HasPrefix(l, "warning"):
		return t.Syn(rTy)
	case strings.HasPrefix(l, "✔") || strings.HasSuffix(l, "... ok") || strings.HasPrefix(l, "test result: ok") || strings.HasPrefix(l, "Finished") ||
		strings.HasPrefix(l, "ok ") || strings.HasPrefix(l, "PASS") || strings.HasPrefix(l, "--- PASS"):
		return t.Add()
	case strings.HasPrefix(l, "-->"):
		return t.Faint()
	}
	return t.Dim()
}

// logEntry is one row group of the activity log.
type logEntry struct {
	time string
	kind string
	kc   Color
	text string
	tc   Color
}

func (m *Model) logEntry(ai int, e *watch.Event) logEntry {
	t := &m.theme
	le := logEntry{time: e.Time.Local().Format("15:04:05"), kind: string(e.Kind), kc: t.Dim(), tc: t.FG()}
	switch e.Kind {
	case watch.KindRead:
		le.kc = t.Syn(rFn)
		le.text = e.Text
		if e.File != "" {
			le.text = m.rel(e.File)
			if e.Line >= 0 && e.EndLine >= 0 {
				le.text += "  " + strconv.Itoa(e.Line+1) + "–" + strconv.Itoa(e.EndLine+1)
			}
		}
	case watch.KindGrep:
		le.kc = t.Syn(rFn)
		le.text = "\"" + e.Query + "\""
		switch {
		case e.Result == watch.Pending:
			le.text += "  …"
		case e.Hits == 1:
			le.text += "  1 hit"
		case e.Hits >= 0:
			le.text += "  " + strconv.Itoa(e.Hits) + " hits"
		}
	case watch.KindList:
		le.kc = t.Syn(rFn)
		le.text = e.Text
	case watch.KindEdit:
		le.kc = t.Add()
		le.text = e.Text
		if e.File != "" {
			le.text = m.rel(e.File) + "  +" + strconv.Itoa(e.Add)
			if e.Del > 0 {
				le.text += " −" + strconv.Itoa(e.Del)
			}
			switch {
			case e.Deleted:
				le.text += "  deleted"
			case e.Created:
				le.text += "  new"
			}
		}
		if e.Result == watch.Failed {
			le.tc = t.Del()
		}
	case watch.KindRun:
		le.kc = t.Syn(rTy)
		le.text = "$ " + firstLine(e.Cmd) + "  " + e.Summary()
		switch e.Result {
		case watch.Failed:
			le.tc = t.Del()
		case watch.Warned:
			le.tc = t.Syn(rTy)
		}
	case watch.KindWeb:
		le.kc, le.text = t.Syn(rMac), e.Text
	case watch.KindTask:
		le.kc, le.text = t.Syn(rNum), e.Text
	case watch.KindAsk:
		le.kind, le.kc, le.text, le.tc = "wait", t.Dim(), "on you · "+e.Text, t.Dim()
	case watch.KindYou:
		le.kc, le.text = t.Agent(ai), e.Text
	case watch.KindSay:
		le.kind, le.kc, le.text, le.tc = "", t.Dim(), e.Text, t.Dim()
	case watch.KindDone:
		le.kc, le.text, le.tc = t.Add(), e.Text, t.Add()
	default:
		le.text = e.Text
	}
	if e.Kind != watch.KindRun && e.Result == watch.Failed {
		le.tc = t.Del()
	}
	return le
}

func (m *Model) drawActivity(c *Canvas, x, y, w, h, ai int) {
	t := &m.theme
	c.fill(x, y, w, h, t.Sunk())
	cx := c.text(x+1, y, x+w, "ACTIVITY", Style{FG: t.Dim(), Bold: true}) + 1
	if ai < 0 {
		return
	}
	a := &m.snap.Agents[ai]
	cx = c.text(cx, y, x+w, tag(ai), Style{FG: t.Agent(ai), Bold: true}) + 1
	name := a.Name + " · " + a.Source
	if a.Name == a.Source {
		name = a.Name
	}
	c.text(cx, y, x+w-1, clip(name, x+w-1-cx), Style{FG: t.FG()})
	task := wrap(a.Task(), w-2)
	if len(task) > 3 {
		task = task[:3]
		task[2] = clip(task[2]+" …", w-2)
	}
	for i, l := range task {
		c.text(x+1, y+1+i, x+w-1, l, Style{FG: t.Dim()})
	}
	top := y + 1 + len(task) + 1
	tw := w - 2 - 8 - 1 - 4 - 1
	yy := y + h
	for j := len(a.Events) - 1; j >= 0 && yy > top; j-- {
		le := m.logEntry(ai, &a.Events[j])
		lines := wrap(le.text, tw)
		if len(lines) == 0 {
			lines = []string{""}
		}
		if len(lines) > 4 {
			lines = lines[:4]
			lines[3] = clip(lines[3]+" …", tw)
		}
		start := yy - len(lines)
		if start < top && yy < y+h {
			// Show only whole entries, except for the newest one.
			break
		}
		for k := len(lines) - 1; k >= 0; k-- {
			ry := start + k
			if ry < top {
				continue
			}
			if k == 0 {
				c.text(x+1, ry, x+9, le.time, Style{FG: t.Faint()})
				c.text(x+10, ry, x+14, le.kind, Style{FG: le.kc})
			}
			c.text(x+15, ry, x+w-1, lines[k], Style{FG: le.tc})
		}
		yy = start
	}
}

// splitAgents picks up to 9 agents for the split layout: the focused one
// and the most recently active others.
func (m *Model) splitAgents() []int {
	n := len(m.snap.Agents)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	if n <= 9 {
		return idx
	}
	fi := m.focusIndex()
	recent := append([]int(nil), idx...)
	sortBy(recent, func(a, b int) bool {
		if a == fi || b == fi {
			return a == fi
		}
		return m.snap.Agents[a].Updated.After(m.snap.Agents[b].Updated)
	})
	recent = recent[:9]
	sortBy(recent, func(a, b int) bool { return a < b })
	return recent
}

func grid(n int) (cols, rows int) {
	switch {
	case n <= 3:
		cols = max(1, n)
	case n == 4:
		cols = 2
	default:
		cols = 3
	}
	return cols, (n + cols - 1) / cols
}

func (m *Model) drawSplits(c *Canvas, y0, h int) paneInfo {
	idx := m.splitAgents()
	cols, rows := grid(len(idx))
	pw := (m.w - (cols - 1)) / cols
	ph := h / rows
	fi := m.focusIndex()
	var fp paneInfo
	for k, i := range idx {
		cx, cy := k%cols, k/cols
		x := cx * (pw + 1)
		w := pw
		if cx == cols-1 {
			w = m.w - x
		}
		y := y0 + cy*ph
		hh := ph
		if cy == rows-1 {
			hh = y0 + h - y
		}
		info := m.drawSplitPane(c, x, y, w, hh, i)
		if i == fi {
			fp = info
		}
		if cx < cols-1 {
			m.vline(c, x+w, y, hh, none)
		}
	}
	return fp
}

func (m *Model) drawSplitPane(c *Canvas, x, y, w, h, i int) paneInfo {
	t := &m.theme
	fi := m.focusIndex()
	fol := i != fi || m.following()
	logH := min(6, max(2, h/4))
	codeH := max(1, h-2-logH)
	info := m.drawCode(c, "p"+strconv.Itoa(i), i, fol, x, y+1, w, codeH)
	m.drawWinbar(c, x, y, w, info, i, fol, true)
	ay := y + 1 + codeH
	c.fill(x, ay, w, h-1-codeH, t.Sunk())
	cx := c.text(x+1, ay, x+w-1, "ACTIVITY", Style{FG: t.Dim(), Bold: true}) + 1
	c.text(cx, ay, x+w-1, clip(m.snap.Agents[i].Task(), x+w-1-cx), Style{FG: t.Dim()})
	a := &m.snap.Agents[i]
	var rows []logEntry
	for j := len(a.Events) - 1; j >= 0 && len(rows) < logH; j-- {
		e := &a.Events[j]
		for k := len(e.Output) - 1; k >= 0 && len(rows) < logH; k-- {
			rows = append(rows, logEntry{text: expandTabs(e.Output[k], 0), tc: m.cls(e.Output[k])})
		}
		if len(rows) < logH {
			rows = append(rows, m.logEntry(i, e))
		}
	}
	yy := ay + 1 + logH - len(rows)
	for k := len(rows) - 1; k >= 0; k-- {
		le := rows[k]
		c.text(x+1, yy, x+9, le.time, Style{FG: t.Faint()})
		c.text(x+10, yy, x+14, le.kind, Style{FG: le.kc})
		c.text(x+15, yy, x+w-1, clip(le.text, w-16), Style{FG: le.tc})
		yy++
	}
	if i != fi {
		m.addZone(x, y, w, h, func() tea.Cmd { return m.pin(i) })
	}
	return info
}

func (m *Model) drawTabs(c *Canvas, cur string) {
	t := &m.theme
	c.fill(0, 0, m.w, 1, t.Deep())
	clock := m.now.Format("15:04:05")
	where := m.repo.Name
	if b := m.repo.State.Branch; b != "" {
		where += " · " + b
	}
	if m.all {
		where = "all projects"
	}
	right := where + "  " + clock
	rx := m.w - 1 - width(right)
	c.text(rx, 0, m.w, where, Style{FG: t.Dim()})
	c.text(rx+width(where)+2, 0, m.w, clock, Style{FG: t.FG()})

	tabs := m.tabs
	if cur != "" && !contains(tabs, cur) {
		tabs = append(append([]string(nil), tabs...), cur)
	}
	base := map[string]int{}
	for _, p := range tabs {
		base[filepath.Base(p)]++
	}
	type tabView struct {
		path, name string
		mod        bool
		dots       []int
		w          int
	}
	var tv []tabView
	for _, p := range tabs {
		name := filepath.Base(p)
		if base[name] > 1 {
			name = filepath.Join(filepath.Base(filepath.Dir(p)), name)
		}
		f := m.repo.File(p)
		v := tabView{path: p, name: name, mod: f.Changed()}
		for i := range m.snap.Agents {
			if m.acts[i].Status != watch.Done && m.poss[i].ok && m.poss[i].file == p {
				v.dots = append(v.dots, i)
			}
		}
		v.w = 1 + width(name) + 2 + 1
		if v.mod {
			v.w += 2
		}
		v.w += len(v.dots) * 2
		tv = append(tv, v)
	}
	// Drop tabs from the start until the active tab fits.
	limit := rx - 1
	start := 0
	for {
		x, fits := 0, true
		for k := start; k < len(tv); k++ {
			x += tv[k].w
			if tv[k].path == cur && x > limit {
				fits = false
			}
		}
		if fits || start >= len(tv)-1 {
			break
		}
		start++
	}
	x := 0
	for _, v := range tv[start:] {
		if x >= limit {
			break
		}
		act := v.path == cur
		fg := t.Dim()
		if act {
			c.fill(x, 0, min(v.w-1, limit-x), 1, defColor)
			fg = t.FG()
		}
		cx := c.text(x+1, 0, limit, v.name, Style{FG: fg})
		if v.mod {
			cx = c.text(cx+1, 0, limit, "+", Style{FG: t.Mod()})
		}
		for _, i := range v.dots {
			cx = c.text(cx+1, 0, limit, "●", Style{FG: t.Agent(i)})
		}
		end := x + v.w - 1
		if end < limit {
			c.put(end, 0, "│", 1, Style{FG: t.Border()})
		}
		p := v.path
		m.addZone(x, 0, v.w, 1, func() tea.Cmd { m.openFile(p); return nil })
		x += v.w
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (m *Model) drawStatus(c *Canvas, y int, fp paneInfo) {
	t := &m.theme
	c.fill(0, y, m.w, 1, t.Bar())
	fi := m.focusIndex()
	fol := m.following()
	modeC := defColor
	mode := "NORMAL"
	if fol {
		mode = "FOLLOW"
		if fi >= 0 {
			modeC = t.Agent(fi)
		}
	}
	// Right side, from the right edge.
	pct := "All"
	if fp.total > fp.rows {
		switch {
		case fp.top <= 0:
			pct = "Top"
		case fp.top+fp.rows >= fp.total:
			pct = "Bot"
		default:
			pct = strconv.Itoa(fp.top*100/(fp.total-fp.rows)) + "%"
		}
	}
	posTxt := "–"
	switch {
	case !fol && fp.file != "":
		posTxt = strconv.Itoa(fp.ucur+1) + ":1"
	case fi >= 0 && m.poss[fi].ok:
		posTxt = strconv.Itoa(m.poss[fi].line+1) + ":" + strconv.Itoa(m.poss[fi].col+1)
	}
	lang := ""
	if fp.f != nil && fp.file != "" {
		if h := m.hl[fp.file]; h != nil {
			lang = h.lang
		}
	}
	layout := "follow"
	if m.splits {
		layout = "splits"
	}
	diff := "inline diff"
	if !m.inline {
		diff = "signs"
	}
	x := m.w
	seg := func(s string, st Style) {
		x -= width(s) + 2
		c.text(x, y, x+width(s)+2, " "+s+" ", st)
	}
	seg(pct, Style{FG: modeC, Reverse: true})
	seg(posTxt, Style{FG: t.FG(), BG: t.Seg()})
	if lang != "" {
		seg(lang, Style{FG: t.Dim()})
	}
	focus := "auto"
	if !m.auto {
		focus = "pinned"
	}
	seg(focus+" · "+layout+" · "+diff, Style{FG: t.Dim()})
	if f := fp.f; f != nil && f.Changed() {
		x--
		for _, p := range []struct {
			pre string
			n   int
			c   Color
		}{{"−", f.Del, t.Del()}, {"~", f.Mod, t.Mod()}, {"+", f.Add, t.Add()}} {
			if p.n == 0 {
				continue
			}
			s := p.pre + strconv.Itoa(p.n)
			x -= width(s) + 1
			c.text(x, y, x+width(s), s, Style{FG: p.c})
		}
	}
	// Left side.
	lx := c.text(0, y, x, " "+mode+" ", Style{FG: modeC, Reverse: true, Bold: true})
	if fi >= 0 {
		a := " " + tag(fi) + " " + m.snap.Agents[fi].Name + " "
		lx = c.text(lx, y, x, a, Style{FG: t.Agent(fi), BG: t.Seg()})
	}
	file := "[No Name]"
	if fp.file != "" {
		file = m.rel(fp.file)
		if fp.f != nil && fp.f.Changed() {
			file += " [+]"
		}
	}
	c.text(lx+1, y, x-1, clip(file, x-2-lx), Style{FG: t.FG()})
}

func (m *Model) drawMsg(c *Canvas, y int) {
	t := &m.theme
	msg := m.msg
	if m.flash != nil {
		msg = *m.flash
	}
	right := ""
	if k := m.keys.first(actHelp); k != "" {
		right = k + " keys"
	}
	rx := m.w - 1 - width(right)
	c.text(rx, y, m.w, right, Style{FG: t.Faint()})
	if m.pending != "" {
		p := seqLabel(m.pending)
		rx -= 2 + width(p)
		c.text(rx, y, rx+width(p), p, Style{FG: t.FG()})
	}
	mc := msg.c
	if mc == none {
		mc = t.Dim()
	}
	c.text(1, y, rx-2, clip(msg.text, rx-3), Style{FG: mc})
}

func (m *Model) drawHelp(c *Canvas) {
	t := &m.theme
	for i := range c.cells {
		st := &c.cells[i].st
		if bg, ok := t.dimmed(st.BG, true); ok {
			st.BG = bg
		}
		if fg, ok := t.dimmed(st.FG, false); ok {
			st.FG = fg
		} else {
			st.Faint = true
		}
	}
	kw, tw := 12, 0
	for _, k := range m.helpTab {
		kw, tw = max(kw, width(k[0])+2), max(tw, width(k[1]))
	}
	w := min(max(58, 3+kw+tw+3), m.w-4)
	h := len(m.helpTab) + 6
	x := (m.w - w) / 2
	y := max(0, (m.h-h)/2)
	bg := t.Sunk()
	if bg == none {
		bg = defColor
	}
	c.fill(x, y, w, h, bg)
	bc := Style{FG: t.Faint()}
	c.text(x, y, x+w, "┌"+strings.Repeat("─", w-2)+"┐", bc)
	c.text(x, y+h-1, x+w, "└"+strings.Repeat("─", w-2)+"┘", bc)
	for yy := y + 1; yy < y+h-1; yy++ {
		c.text(x, yy, x+1, "│", bc)
		c.text(x+w-1, yy, x+w, "│", bc)
	}
	c.text(x+3, y+1, x+w-2, "Keys · watch only", Style{FG: t.FG(), Bold: true})
	for i, k := range m.helpTab {
		c.text(x+3, y+3+i, x+3+kw, clip(k[0], kw-1), Style{FG: t.Key()})
		c.text(x+3+kw, y+3+i, x+w-2, clip(k[1], w-5-kw), Style{FG: t.Dim()})
	}
	closeKeys := "esc"
	if k := m.keys.label(actHelp); k != "" {
		closeKeys = k + " or esc"
	}
	c.text(x+3, y+h-2, x+w-2, closeKeys+" to close", Style{FG: t.Faint()})
	m.zones = m.zones[:0]
	m.addZone(0, 0, m.w, m.h, func() tea.Cmd { m.help = false; return nil })
}

func sortBy(s []int, less func(a, b int) bool) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && less(s[j], s[j-1]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
