package ui

import (
	"image/color"
	"regexp"
	"strconv"
	"strings"

	xansi "github.com/charmbracelet/x/ansi"
)

// Color is a terminal color. The zero value means "keep the color that is
// already in the cell".
type Color struct {
	k uint8
	v uint32
}

const (
	ckNone uint8 = iota
	ckDefault
	ckIndex
	ckRGB
)

var (
	none     = Color{}
	defColor = Color{k: ckDefault}
)

func ansi(i int) Color { return Color{k: ckIndex, v: uint32(i)} }

// colorOf converts the color of an SGR sequence. The 16 basic colors stay
// the colors of the terminal theme.
func colorOf(c color.Color) Color {
	switch c := c.(type) {
	case nil:
		return none
	case xansi.BasicColor:
		return ansi(int(c))
	case xansi.IndexedColor:
		return ansi(int(c))
	}
	r, g, b, _ := c.RGBA()
	return Color{k: ckRGB, v: r>>8<<16 | g>>8<<8 | b>>8}
}

type rgb struct{ r, g, b float64 }

func (c rgb) color() Color {
	q := func(f float64) uint32 { return uint32(min(255, max(0, f*255+0.5))) }
	return Color{k: ckRGB, v: q(c.r)<<16 | q(c.g)<<8 | q(c.b)}
}

func mixRGB(a, b rgb, f float64) rgb {
	return rgb{a.r + (b.r-a.r)*f, a.g + (b.g-a.g)*f, a.b + (b.b-a.b)*f}
}

// Theme follows the colors of the terminal. Text uses the default colors and
// the 16 ANSI colors, so it changes with the terminal theme. Panel shades and
// tints are mixed from the colors that the terminal reports. When the
// terminal does not report them, panels use the default background.
type Theme struct {
	fg, bg       rgb
	hasFG, hasBG bool
	pal          [16]rgb
	hasPal       [16]bool
}

func toRGB(c color.Color) rgb {
	r, g, b, _ := c.RGBA()
	return rgb{float64(r) / 0xffff, float64(g) / 0xffff, float64(b) / 0xffff}
}

func (t *Theme) setFG(c color.Color) bool {
	v := toRGB(c)
	ch := !t.hasFG || v != t.fg
	t.fg, t.hasFG = v, true
	return ch
}

func (t *Theme) setBG(c color.Color) bool {
	v := toRGB(c)
	ch := !t.hasBG || v != t.bg
	t.bg, t.hasBG = v, true
	return ch
}

var oscPalette = regexp.MustCompile(`4;(\d+);rgb:([0-9a-fA-F]+)/([0-9a-fA-F]+)/([0-9a-fA-F]+)`)

// paletteQuery asks the terminal for the 16 ANSI colors (OSC 4).
func paletteQuery() string {
	var b strings.Builder
	for i := 0; i < 16; i++ {
		b.WriteString("\x1b]4;" + strconv.Itoa(i) + ";?\x07")
	}
	return b.String()
}

// setPalette reads an OSC 4 reply. It reports whether a color changed.
func (t *Theme) setPalette(s string) bool {
	ch := false
	for _, m := range oscPalette.FindAllStringSubmatch(s, -1) {
		i, _ := strconv.Atoi(m[1])
		if i < 0 || i > 15 {
			continue
		}
		v := rgb{hexPart(m[2]), hexPart(m[3]), hexPart(m[4])}
		if !t.hasPal[i] || t.pal[i] != v {
			t.pal[i], t.hasPal[i], ch = v, true, true
		}
	}
	return ch
}

func hexPart(s string) float64 {
	n, _ := strconv.ParseUint(s, 16, 32)
	return float64(n) / float64(uint64(1)<<(4*len(s))-1)
}

// resolve returns the RGB value of a color when the terminal reported it.
func (t *Theme) resolve(c Color, bg bool) (rgb, bool) {
	switch c.k {
	case ckRGB:
		return rgb{float64(c.v>>16&255) / 255, float64(c.v>>8&255) / 255, float64(c.v&255) / 255}, true
	case ckIndex:
		if c.v < 16 && t.hasPal[c.v] {
			return t.pal[c.v], true
		}
	case ckDefault, ckNone:
		if bg {
			return t.bg, t.hasBG
		}
		return t.fg, t.hasFG
	}
	return rgb{}, false
}

func (t *Theme) dark() bool {
	if !t.hasBG {
		return true
	}
	return 0.2126*t.bg.r+0.7152*t.bg.g+0.0722*t.bg.b < 0.5
}

// shade returns the background mixed toward the foreground by f. A negative
// f makes a recessed shade: darker on a dark theme, a little darker on a
// light theme.
func (t *Theme) shade(f float64) Color {
	if !t.hasBG {
		return none
	}
	if f >= 0 {
		if !t.hasFG {
			return none
		}
		return mixRGB(t.bg, t.fg, f).color()
	}
	if t.dark() {
		return mixRGB(t.bg, rgb{}, -f).color()
	}
	if !t.hasFG {
		return none
	}
	return mixRGB(t.bg, t.fg, -f*0.3).color()
}

// tint mixes a color into the background, for line highlights.
func (t *Theme) tint(c Color, f float64) Color {
	v, ok := t.resolve(c, false)
	if !ok || !t.hasBG {
		return none
	}
	return mixRGB(t.bg, v, f).color()
}

// The roles of the design, mapped to terminal colors.
func (t *Theme) FG() Color { return defColor }

func (t *Theme) Dim() Color {
	if t.hasFG && t.hasBG {
		return mixRGB(t.fg, t.bg, 0.3).color()
	}
	return ansi(7)
}

func (t *Theme) Faint() Color { return ansi(8) }

func (t *Theme) Border() Color {
	if c := t.shade(0.16); c != none {
		return c
	}
	return ansi(8)
}

func (t *Theme) BarOff() Color {
	if c := t.shade(0.2); c != none {
		return c
	}
	return ansi(8)
}

func (t *Theme) Deep() Color   { return t.shade(-0.3) }  // tab bar
func (t *Theme) Sunk() Color   { return t.shade(-0.16) } // side panels and terminal
func (t *Theme) Raised() Color { return t.shade(0.055) } // cursor line, focused rows
func (t *Theme) Bar() Color    { return t.shade(0.09) }  // status line
func (t *Theme) Seg() Color    { return t.shade(0.16) }  // status line segments

func (t *Theme) Add() Color { return ansi(2) }
func (t *Theme) Del() Color { return ansi(1) }
func (t *Theme) Mod() Color { return ansi(4) }
func (t *Theme) Key() Color { return ansi(3) }

func (t *Theme) LineAdd() Color { return t.tint(ansi(2), 0.1) }
func (t *Theme) LineDel() Color { return t.tint(ansi(1), 0.15) }

func (t *Theme) Ghost() Color {
	if v, ok := t.resolve(ansi(1), false); ok && t.hasFG {
		return mixRGB(v, t.fg, 0.35).color()
	}
	return ansi(1)
}

// agentColors follows the hues of the design: amber, purple, cyan, blue,
// pink and green, then the bright variants.
var agentColors = []int{3, 5, 6, 4, 1, 2, 11, 13, 14, 12, 9, 10}

func (t *Theme) Agent(i int) Color { return ansi(agentColors[i%len(agentColors)]) }

// Syntax roles.
type role uint8

const (
	rFG role = iota
	rKw
	rStr
	rFn
	rTy
	rNum
	rMac
	rCom
	rPun
	rNs
)

func (t *Theme) Syn(r role) Color {
	switch r {
	case rKw:
		return ansi(1)
	case rStr:
		return ansi(2)
	case rFn:
		return ansi(4)
	case rTy:
		return ansi(3)
	case rNum:
		return ansi(5)
	case rMac:
		return ansi(6)
	case rCom:
		return ansi(8)
	case rPun:
		return t.Dim()
	}
	return defColor
}

// dimmed returns a color behind a modal: mixed toward black when the
// terminal reported its colors.
func (t *Theme) dimmed(c Color, bg bool) (Color, bool) {
	v, ok := t.resolve(c, bg)
	if !ok {
		return c, false
	}
	target := rgb{}
	if !t.dark() {
		target = rgb{1, 1, 1}
	}
	return mixRGB(v, target, 0.55).color(), true
}
