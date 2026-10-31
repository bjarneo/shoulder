package ui

import (
	"strconv"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	xansi "github.com/charmbracelet/x/ansi"
)

// widthMethod is the way that the renderer of Bubble Tea measures text. It
// is WcWidth until the terminal reports the grapheme mode 2027. The canvas,
// width and clip measure text the same way, so that each row has the width
// of the terminal. Only the UI goroutine reads and sets it.
var widthMethod = xansi.WcWidth

// Style is the look of one cell.
type Style struct {
	FG, BG                               Color
	Bold, Italic, Strike, Faint, Reverse bool
}

type cell struct {
	s  string
	w  uint8 // 0 for the other cells of a wide character
	st Style
}

// Canvas is a grid of cells that renders to ANSI text.
type Canvas struct {
	W, H  int
	cells []cell
}

func newCanvas(w, h int) *Canvas {
	c := &Canvas{W: w, H: h, cells: make([]cell, w*h)}
	for i := range c.cells {
		c.cells[i] = cell{s: " ", w: 1}
	}
	return c
}

func (c *Canvas) at(x, y int) *cell { return &c.cells[y*c.W+x] }

// fill clears a rectangle and sets its background. A wide character that
// the rectangle cuts at an edge becomes spaces.
func (c *Canvas) fill(x, y, w, h int, bg Color) {
	x0, x1 := max(0, x), min(c.W, x+w)
	if x0 >= x1 {
		return
	}
	for yy := max(0, y); yy < min(c.H, y+h); yy++ {
		c.clear(x0, yy)
		c.clear(x1-1, yy)
		for xx := x0; xx < x1; xx++ {
			*c.at(xx, yy) = cell{s: " ", w: 1, st: Style{BG: bg}}
		}
	}
}

// clear makes cell x a plain space. When cell x is a part of a wide
// character, all cells of that character become spaces.
func (c *Canvas) clear(x, y int) {
	head := x
	for head > 0 && c.at(head, y).w == 0 {
		head--
	}
	if c.at(head, y).w == 0 {
		head = x
	}
	end := max(x+1, head+int(c.at(head, y).w))
	for xx := head; xx < min(end, c.W); xx++ {
		cl := c.at(xx, y)
		cl.s, cl.w = " ", 1
	}
}

// next decodes the first escape sequence, control character or grapheme
// cluster of s, the same way as the renderer. It returns the part, its width
// in cells and the rest of s. The width is 0 for a sequence or a control
// character. An 8-bit start of a sequence, such as U+009B, counts as its
// 7-bit form, so that the full sequence goes away.
func next(s string, p *xansi.Parser) (string, int, string) {
	if len(s) >= 2 && s[0] == 0xc2 {
		switch s[1] {
		case 0x90, 0x98, 0x9b, 0x9d, 0x9e, 0x9f:
			s = "\x1b" + string(rune(s[1]-0x40)) + s[2:]
		}
	}
	seq, w, n, _ := widthMethod.DecodeSequenceInString(s, 0, p)
	if n <= 0 {
		return s[:1], 0, s[1:]
	}
	return seq, w, s[n:]
}

// text writes s from column x and stops before column maxX. A tab is one
// space. A style without a background keeps the background of each cell. It
// returns the column after the text.
func (c *Canvas) text(x, y, maxX int, s string, st Style) int {
	return c.write(x, y, maxX, -1, s, st)
}

// tabbed writes s like text, but a tab goes to the next tab stop. The tab
// stops start at column origin.
func (c *Canvas) tabbed(x, y, maxX, origin int, s string, st Style) int {
	return c.write(x, y, maxX, origin, s, st)
}

// write writes s with the tab stops from origin, or with a tab as one space
// when origin is negative. It removes escape sequences and control
// characters.
func (c *Canvas) write(x, y, maxX, origin int, s string, st Style) int {
	if y < 0 || y >= c.H {
		return x
	}
	maxX = min(maxX, c.W)
	for len(s) > 0 && x < maxX {
		seq, w, rest := next(s, nil)
		s = rest
		if w == 0 {
			switch seq {
			case "\t":
				stop := x + 1
				if origin >= 0 && x >= origin {
					stop = x + tabWidth - (x-origin)%tabWidth
				}
				for ; x < min(stop, maxX); x++ {
					c.put(x, y, " ", 1, st)
				}
			case "\n", "\r":
				c.put(x, y, " ", 1, st)
				x++
			}
			continue
		}
		if x+w > maxX {
			for ; x < maxX; x++ {
				c.put(x, y, " ", 1, st)
			}
			break
		}
		c.put(x, y, seq, w, st)
		x += w
	}
	return x
}

// put writes one character of w cells at column x.
func (c *Canvas) put(x, y int, s string, w int, st Style) {
	w = max(1, w)
	if x < 0 || x+w > c.W || y < 0 || y >= c.H {
		return
	}
	bg := c.at(x, y).st.BG
	for xx := x; xx < x+w; xx++ {
		c.clear(xx, y)
	}
	if st.BG == none {
		st.BG = bg
	}
	*c.at(x, y) = cell{s: s, w: uint8(w), st: st}
	for xx := x + 1; xx < x+w; xx++ {
		*c.at(xx, y) = cell{st: st}
	}
}

// paint changes the style of cells without changing their text.
func (c *Canvas) paint(x, y, w int, f func(*Style)) {
	if y < 0 || y >= c.H {
		return
	}
	for xx := max(0, x); xx < min(c.W, x+w); xx++ {
		f(&c.at(xx, y).st)
	}
}

// render returns the canvas as text with SGR sequences.
func (c *Canvas) render() string {
	var b strings.Builder
	b.Grow(c.W * c.H * 4)
	for y := 0; y < c.H; y++ {
		var cur Style
		first := true
		for x := 0; x < c.W; x++ {
			cl := c.at(x, y)
			if cl.w == 0 {
				continue
			}
			if first || cl.st != cur {
				b.WriteString(sgr(cl.st))
				cur, first = cl.st, false
			}
			b.WriteString(cl.s)
		}
		b.WriteString("\x1b[0m")
		if y < c.H-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func sgr(st Style) string {
	b := []byte("\x1b[0")
	if st.Bold {
		b = append(b, ";1"...)
	}
	if st.Faint {
		b = append(b, ";2"...)
	}
	if st.Italic {
		b = append(b, ";3"...)
	}
	if st.Reverse {
		b = append(b, ";7"...)
	}
	if st.Strike {
		b = append(b, ";9"...)
	}
	b = appendColor(b, st.FG, false)
	b = appendColor(b, st.BG, true)
	return string(append(b, 'm'))
}

func appendColor(b []byte, c Color, bg bool) []byte {
	switch c.k {
	case ckIndex:
		n := int(c.v)
		switch {
		case n < 8 && bg:
			return append(b, ";4"+strconv.Itoa(n)...)
		case n < 8:
			return append(b, ";3"+strconv.Itoa(n)...)
		case n < 16 && bg:
			return append(b, ";10"+strconv.Itoa(n-8)...)
		case n < 16:
			return append(b, ";9"+strconv.Itoa(n-8)...)
		case bg:
			return append(b, ";48;5;"+strconv.Itoa(n)...)
		default:
			return append(b, ";38;5;"+strconv.Itoa(n)...)
		}
	case ckRGB:
		p := ";38;2;"
		if bg {
			p = ";48;2;"
		}
		return append(b, p+strconv.Itoa(int(c.v>>16&255))+";"+strconv.Itoa(int(c.v>>8&255))+";"+strconv.Itoa(int(c.v&255))...)
	}
	return b
}

// width returns the display width of s.
func width(s string) int { return widthMethod.StringWidth(s) }

// clip shortens s to w columns and marks the cut with an ellipsis.
func clip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) <= w {
		return s
	}
	return widthMethod.Truncate(s, w, "…")
}

// clipLeft shortens s to w columns from the start and marks the cut with an
// ellipsis. The end of s, such as the name of a file, stays.
func clipLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) <= w {
		return s
	}
	var parts []string
	var ws []int
	for len(s) > 0 {
		seq, cw, rest := next(s, nil)
		s = rest
		if cw > 0 {
			parts, ws = append(parts, seq), append(ws, cw)
		}
	}
	room, i := w-width("…"), len(parts)
	for i > 0 && ws[i-1] <= room {
		i--
		room -= ws[i]
	}
	return "…" + strings.Join(parts[i:], "")
}

// padLeft right-aligns s in w columns.
func padLeft(s string, w int) string {
	if n := w - width(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// wrap breaks s into lines of at most w columns, at spaces when it can.
func wrap(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			for width(word) > w {
				if line != "" {
					out = append(out, line)
					line = ""
				}
				cut := widthMethod.Truncate(word, w, "")
				if cut == "" {
					// One character is wider than the line. It gets its own line.
					cut, _, _ = next(word, nil)
				}
				out = append(out, cut)
				word = word[len(cut):]
			}
			switch {
			case line == "":
				line = word
			case width(line)+1+width(word) <= w:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
		}
		out = append(out, line)
	}
	return out
}

// plainText reports whether s has no escape sequences and no control
// characters other than tabs.
func plainText(s string) bool {
	for i := 0; i < len(s); i++ {
		if b := s[i]; (b < 0x20 && b != '\t') || b == 0x7f || b == 0xc2 {
			return false
		}
	}
	return true
}

// stripEsc removes the escape sequences and the control characters other
// than tabs from s.
func stripEsc(s string) string {
	if plainText(s) {
		return s
	}
	var b strings.Builder
	for len(s) > 0 {
		seq, w, rest := next(s, nil)
		s = rest
		if w > 0 || seq == "\t" {
			b.WriteString(seq)
		}
	}
	return b.String()
}

// seg is a part of a line of command output in one color.
type seg struct {
	text string
	fg   Color // none for the default color of the line
	bold bool
}

// sgrSegs splits a line of command output at its SGR sequences. The parts
// keep the foreground color and the bold text of the sequences. All other
// escape sequences and control characters go away.
func sgrSegs(s string) []seg {
	if plainText(s) {
		return []seg{{text: s}}
	}
	p := xansi.GetParser()
	defer xansi.PutParser(p)
	var out []seg
	var pen uv.Style
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, seg{text: b.String(), fg: colorOf(pen.Fg), bold: pen.Attrs&uv.AttrBold != 0})
			b.Reset()
		}
	}
	for len(s) > 0 {
		seq, w, rest := next(s, p)
		s = rest
		switch {
		case w > 0 || seq == "\t":
			b.WriteString(seq)
		case xansi.HasCsiPrefix(seq) && p.Command() == 'm':
			flush()
			uv.ReadStyle(p.Params(), &pen)
		}
	}
	flush()
	return out
}
