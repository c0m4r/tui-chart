package chart

import (
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"
)

// Attr is a set of text attributes.
type Attr uint8

// Attributes understood by the canvas and emitted as SGR sequences.
const (
	AttrBold Attr = 1 << iota
	AttrDim
	AttrItalic
	AttrUnderline
	AttrReverse
)

// Style describes how a cell is painted.
type Style struct {
	FG   Color
	BG   Color
	Attr Attr
}

// WithFG returns the style with a new foreground color.
func (s Style) WithFG(c Color) Style { s.FG = c; return s }

// WithBG returns the style with a new background color.
func (s Style) WithBG(c Color) Style { s.BG = c; return s }

// WithAttr returns the style with additional attributes.
func (s Style) WithAttr(a Attr) Style { s.Attr |= a; return s }

// WithoutAttr returns the style with attributes removed.
func (s Style) WithoutAttr(a Attr) Style { s.Attr &^= a; return s }

// Cell is a single character cell. When Dots is non zero the cell renders as a
// braille pattern and Ch is ignored.
type Cell struct {
	Ch   rune
	Dots uint8
	Style
	cont bool // right half of a wide rune: skipped on output
}

// Canvas is a grid of cells with sub-cell drawing primitives.
type Canvas struct {
	w, h  int
	cells []Cell
}

// NewCanvas allocates a canvas of w x h cells.
func NewCanvas(w, h int) *Canvas {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	c := &Canvas{w: w, h: h, cells: make([]Cell, w*h)}
	for i := range c.cells {
		c.cells[i].Ch = ' '
	}
	return c
}

// W returns the canvas width in cells.
func (c *Canvas) W() int { return c.w }

// H returns the canvas height in cells.
func (c *Canvas) H() int { return c.h }

// DotW returns the horizontal resolution in braille dots.
func (c *Canvas) DotW() int { return c.w * 2 }

// DotH returns the vertical resolution in braille dots.
func (c *Canvas) DotH() int { return c.h * 4 }

// Resize reallocates the canvas, clearing it.
func (c *Canvas) Resize(w, h int) {
	*c = *NewCanvas(w, h)
}

// Clear fills the whole canvas with a style.
func (c *Canvas) Clear(st Style) {
	for i := range c.cells {
		c.cells[i] = Cell{Ch: ' ', Style: st}
	}
}

// InBounds reports whether x,y address a cell.
func (c *Canvas) InBounds(x, y int) bool {
	return x >= 0 && y >= 0 && x < c.w && y < c.h
}

// At returns a pointer to a cell, or nil when out of bounds.
func (c *Canvas) At(x, y int) *Cell {
	if !c.InBounds(x, y) {
		return nil
	}
	return &c.cells[y*c.w+x]
}

// Rune returns the printable rune at x,y (' ' when out of bounds).
func (c *Canvas) Rune(x, y int) rune {
	if cell := c.At(x, y); cell != nil {
		if cell.Dots != 0 {
			return rune(0x2800 + int(cell.Dots))
		}
		if cell.cont {
			return 0
		}
		return cell.Ch
	}
	return ' '
}

// Set paints one character cell. Wide runes claim the following cell.
func (c *Canvas) Set(x, y int, ch rune, st Style) {
	cell := c.At(x, y)
	if cell == nil {
		return
	}
	w := runewidth.RuneWidth(ch)
	if w == 0 {
		w = 1
	}
	if w == 2 {
		if nx := c.At(x+1, y); nx != nil {
			*nx = Cell{Ch: ' ', Style: st, cont: true}
		}
	}
	*cell = Cell{Ch: ch, Style: st}
}

// SetStyle repaints the background (and foreground) of a cell, keeping glyph.
func (c *Canvas) SetStyle(x, y int, st Style) {
	if cell := c.At(x, y); cell != nil {
		cell.Style = st
	}
}

// SetBG repaints only the background of a cell, keeping its glyph. Stacked
// charts use this to claim the space above a partially filled neighbour cell.
func (c *Canvas) SetBG(x, y int, bg Color) {
	if cell := c.At(x, y); cell != nil {
		cell.BG = bg
	}
}

// FillRect paints a rectangle with a rune.
func (c *Canvas) FillRect(x, y, w, h int, ch rune, st Style) {
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			c.Set(x+i, y+j, ch, st)
		}
	}
}

// ClearRect blanks a rectangle, replacing both glyphs and styles. Panels and
// modals use it so nothing from the layer below shows through.
func (c *Canvas) ClearRect(x, y, w, h int, st Style) {
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			c.Set(x+i, y+j, ' ', st)
		}
	}
}

// FillStyle recolors a rectangle without changing glyphs.
func (c *Canvas) FillStyle(x, y, w, h int, st Style) {
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			c.SetStyle(x+i, y+j, st)
		}
	}
}

// brailleBit maps a sub-cell coordinate to its braille dot bit.
//
//	sx: 0 = left column, 1 = right column
//	sy: 0..3 top to bottom
func brailleBit(sx, sy int) uint8 {
	if sy == 3 {
		if sx == 0 {
			return 1 << 6
		}
		return 1 << 7
	}
	return 1 << uint(sy+3*sx)
}

// Dot raises one braille dot. Coordinates are in dot space: x in [0,2W),
// y in [0,4H).
func (c *Canvas) Dot(x, y int, st Style) {
	if x < 0 || y < 0 || x >= c.w*2 || y >= c.h*4 {
		return
	}
	cell := &c.cells[(y/4)*c.w+x/2]
	cell.Dots |= brailleBit(x%2, y%4)
	cell.Style = st
}

// DotLine draws a 1px line between two dot space points (Bresenham).
func (c *Canvas) DotLine(x0, y0, x1, y1 int, st Style) {
	dx := abs(x1 - x0)
	dy := -abs(y1 - y0)
	sx, sy := -1, -1
	if x0 < x1 {
		sx = 1
	}
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		c.Dot(x0, y0, st)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

// VDot paints a vertical run of dots at dot column x from y0 to y1 inclusive.
func (c *Canvas) VDot(x, y0, y1 int, st Style) {
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	for y := y0; y <= y1; y++ {
		c.Dot(x, y, st)
	}
}

// Text writes a string starting at x,y and returns the next free x.
func (c *Canvas) Text(x, y int, s string, st Style) int {
	for _, r := range s {
		w := runewidth.RuneWidth(r)
		if w == 0 {
			w = 1
		}
		if r == '\t' {
			w = 1
		}
		if x >= c.w {
			return x
		}
		if x+w > 0 {
			c.Set(x, y, r, st)
		}
		x += w
	}
	return x
}

// TextRight writes a string ending at column x (inclusive).
func (c *Canvas) TextRight(x, y int, s string, st Style) int {
	w := runewidth.StringWidth(s)
	return c.Text(x-w+1, y, s, st)
}

// TextCenter writes a string centered in [x, x+w).
func (c *Canvas) TextCenter(x, y, w int, s string, st Style) {
	sw := runewidth.StringWidth(s)
	if sw > w {
		s = runewidth.Truncate(s, w, "")
		sw = runewidth.StringWidth(s)
	}
	c.Text(x+(w-sw)/2, y, s, st)
}

// HLine draws a horizontal line of ch.
func (c *Canvas) HLine(x0, x1, y int, ch rune, st Style) {
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	for x := x0; x <= x1; x++ {
		c.Set(x, y, ch, st)
	}
}

// VLine draws a vertical line of ch.
func (c *Canvas) VLine(x, y0, y1 int, ch rune, st Style) {
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	for y := y0; y <= y1; y++ {
		c.Set(x, y, ch, st)
	}
}

// Frame draws a box border with the given style.
func (c *Canvas) Frame(x, y, w, h int, st Style) {
	if w <= 0 || h <= 0 {
		return
	}
	c.HLine(x, x+w-1, y, '─', st)
	c.HLine(x, x+w-1, y+h-1, '─', st)
	c.VLine(x, y, y+h-1, '│', st)
	c.VLine(x+w-1, y, y+h-1, '│', st)
	c.Set(x, y, '╭', st)
	c.Set(x+w-1, y, '╮', st)
	c.Set(x, y+h-1, '╰', st)
	c.Set(x+w-1, y+h-1, '╯', st)
}

// Resolve returns the ColorMode this canvas should emit for.
type emitter struct {
	c       *Canvas
	mode    ColorMode
	buf     strings.Builder
	cache   map[Style]string
	current string
}

// Render serializes the canvas to an ANSI escaped string, one line per row.
// Every logical line ends with a reset so styling cannot bleed into
// neighbouring widgets, and rows are padded to the full canvas width so panel
// backgrounds stay rectangular.
func (c *Canvas) Render(mode ColorMode) string {
	e := &emitter{c: c, mode: mode, cache: make(map[Style]string, 64)}
	for y := 0; y < c.h; y++ {
		if y > 0 {
			e.buf.WriteString("\x1b[0m\r\n")
			e.current = ""
		}
		for x := 0; x < c.w; x++ {
			e.emit(&c.cells[y*c.w+x])
		}
	}
	e.buf.WriteString("\x1b[0m")
	return e.buf.String()
}

// Lines renders the canvas as one styled string per row.
func (c *Canvas) Lines(mode ColorMode) []string {
	full := c.Render(mode)
	full = strings.TrimSuffix(full, "\x1b[0m")
	parts := strings.Split(full, "\r\n")
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = p + "\x1b[0m"
	}
	return out
}

// PlainLines renders the canvas as uncolored text (debug and golden tests).
func (c *Canvas) PlainLines() []string {
	out := make([]string, c.h)
	for y := 0; y < c.h; y++ {
		var b strings.Builder
		for x := 0; x < c.w; x++ {
			cell := &c.cells[y*c.w+x]
			switch {
			case cell.Dots != 0:
				b.WriteRune(rune(0x2800 + int(cell.Dots)))
			case cell.cont:
			default:
				b.WriteRune(cell.Ch)
			}
		}
		out[y] = strings.TrimRight(b.String(), " ")
	}
	return out
}

func (e *emitter) emit(cell *Cell) {
	if cell.cont {
		return
	}
	ch := cell.Ch
	if cell.Dots != 0 {
		ch = rune(0x2800 + int(cell.Dots))
	}
	if ch == 0 {
		ch = ' '
	}
	sgr := e.sgr(cell.Style)
	if sgr != e.current {
		e.buf.WriteString(sgr)
		e.current = sgr
	}
	e.buf.WriteRune(ch)
}

// sgr builds the escape sequence for a style, memoized.
func (e *emitter) sgr(st Style) string {
	if e.mode == ColorNone {
		if st.Attr == 0 {
			return "\x1b[0m"
		}
		return "\x1b[0;" + attrString(st.Attr) + "m"
	}
	if s, ok := e.cache[st]; ok {
		return s
	}
	var b strings.Builder
	b.WriteString("\x1b[0")
	if st.Attr != 0 {
		b.WriteByte(';')
		b.WriteString(attrString(st.Attr))
	}
	b.WriteByte(';')
	b.WriteString(e.fgSeq(st.FG))
	b.WriteByte(';')
	b.WriteString(e.bgSeq(st.BG))
	b.WriteByte('m')
	out := b.String()
	if len(e.cache) < 4096 {
		e.cache[st] = out
	}
	return out
}

func attrString(a Attr) string {
	parts := make([]string, 0, 5)
	if a&AttrBold != 0 {
		parts = append(parts, "1")
	}
	if a&AttrDim != 0 {
		parts = append(parts, "2")
	}
	if a&AttrItalic != 0 {
		parts = append(parts, "3")
	}
	if a&AttrUnderline != 0 {
		parts = append(parts, "4")
	}
	if a&AttrReverse != 0 {
		parts = append(parts, "7")
	}
	return strings.Join(parts, ";")
}

func (e *emitter) fgSeq(c Color) string {
	switch e.mode {
	case ColorBasic:
		i := nearest16(c)
		if i < 8 {
			return strconv.Itoa(30 + i)
		}
		return strconv.Itoa(90 + i - 8)
	case Color256:
		return "38;5;" + strconv.Itoa(nearest256(c))
	default:
		return "38;2;" + strconv.Itoa(int(c.R)) + ";" + strconv.Itoa(int(c.G)) + ";" + strconv.Itoa(int(c.B))
	}
}

func (e *emitter) bgSeq(c Color) string {
	switch e.mode {
	case ColorBasic:
		i := nearest16(c)
		if i < 8 {
			return strconv.Itoa(40 + i)
		}
		return strconv.Itoa(100 + i - 8)
	case Color256:
		return "48;5;" + strconv.Itoa(nearest256(c))
	default:
		return "48;2;" + strconv.Itoa(int(c.R)) + ";" + strconv.Itoa(int(c.G)) + ";" + strconv.Itoa(int(c.B))
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
