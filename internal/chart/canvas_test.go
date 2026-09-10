package chart

import (
	"strings"
	"testing"
)

func TestBrailleDotMapping(t *testing.T) {
	// Standard Unicode braille bit order: dots 1,2,3 down the left column,
	// 4,5,6 down the right, then 7 and 8 along the bottom.
	cases := []struct {
		sx, sy int
		want   uint8
	}{
		{0, 0, 1 << 0}, {0, 1, 1 << 1}, {0, 2, 1 << 2},
		{1, 0, 1 << 3}, {1, 1, 1 << 4}, {1, 2, 1 << 5},
		{0, 3, 1 << 6}, {1, 3, 1 << 7},
	}
	for _, c := range cases {
		if got := brailleBit(c.sx, c.sy); got != c.want {
			t.Errorf("brailleBit(%d,%d) = %#b, want %#b", c.sx, c.sy, got, c.want)
		}
	}
}

func TestCanvasDotAndRune(t *testing.T) {
	c := NewCanvas(2, 1)
	c.Dot(0, 0, Style{})
	if got := c.Rune(0, 0); got != '⠁' {
		t.Errorf("single dot = %q, want ⠁", got)
	}
	c.Dot(1, 3, Style{})
	if got := c.Rune(0, 0); got != '⢁' {
		t.Errorf("dots 1+8 = %q, want ⢁", got)
	}
	// dots outside the canvas are ignored rather than panicking
	c.Dot(-1, 0, Style{})
	c.Dot(100, 100, Style{})
	c.Set(-5, 0, 'x', Style{})
	c.Set(0, 99, 'x', Style{})
}

func TestCanvasWideRunes(t *testing.T) {
	c := NewCanvas(4, 1)
	c.Text(0, 0, "日本", Style{})
	if got := c.Rune(0, 0); got != '日' {
		t.Fatalf("cell 0 = %q", got)
	}
	if got := c.Rune(1, 0); got != 0 {
		t.Fatalf("cell 1 should be a continuation, got %q", got)
	}
	if got := c.Rune(2, 0); got != '本' {
		t.Fatalf("cell 2 = %q", got)
	}
	if line := c.PlainLines()[0]; line != "日本" {
		t.Fatalf("plain line = %q, want 日本", line)
	}
}

func TestCanvasTextAlignment(t *testing.T) {
	c := NewCanvas(10, 1)
	c.TextRight(4, 0, "abc", Style{})
	if got := c.PlainLines()[0]; got != "  abc" {
		t.Errorf("TextRight = %q", got)
	}
	c.Clear(Style{})
	c.TextCenter(0, 0, 11, "hi", Style{})
	if got := c.PlainLines()[0]; got != "    hi" {
		t.Errorf("TextCenter = %q", got)
	}
}

func TestCanvasRenderModes(t *testing.T) {
	c := NewCanvas(3, 1)
	c.Set(0, 0, 'a', Style{FG: RGB(255, 0, 0), BG: RGB(0, 0, 255)})
	c.Set(1, 0, 'b', Style{FG: RGB(0, 255, 0), BG: RGB(0, 0, 255)})

	trueOut := c.Render(ColorTrue)
	if !strings.Contains(trueOut, "38;2;255;0;0") || !strings.Contains(trueOut, "48;2;0;0;255") {
		t.Errorf("truecolor render missing rgb sequences: %q", trueOut)
	}
	two := c.Render(Color256)
	if !strings.Contains(two, "38;5;") || strings.Contains(two, "38;2;") {
		t.Errorf("256 color render wrong: %q", two)
	}
	basic := c.Render(ColorBasic)
	if !strings.Contains(basic, "\x1b[0;31;") && !strings.Contains(basic, "\x1b[0;91;") {
		t.Errorf("basic color render missing 16 color codes: %q", basic)
	}
	none := c.Render(ColorNone)
	if strings.Contains(none, "38;") || strings.Contains(none, "48;") {
		t.Errorf("colorless render should not set colors: %q", none)
	}
	if !strings.HasSuffix(none, "\x1b[0m") {
		t.Errorf("render must end with a reset: %q", none)
	}
	// every line must contain the same number of glyphs
	for _, mode := range []ColorMode{ColorNone, ColorBasic, Color256, ColorTrue} {
		lines := c.Lines(mode)
		if len(lines) != 1 {
			t.Fatalf("mode %v: %d lines", mode, len(lines))
		}
		if got := StripANSI(lines[0]); got != "ab " {
			t.Errorf("mode %v: stripped line = %q", mode, got)
		}
	}
}

func TestColorQuantization(t *testing.T) {
	if got := nearest256(RGB(255, 0, 0)); xterm256(got).R < 200 {
		t.Errorf("nearest256(red) = %d -> %v", got, xterm256(got))
	}
	if got := nearest16(RGB(250, 250, 250)); got != 15 {
		t.Errorf("nearest16(white) = %d, want 15", got)
	}
	// blending in linear light keeps midpoints brighter than naive rgb mixing
	mid := Lerp(RGB(0, 0, 0), RGB(255, 255, 255), 0.5)
	if mid.R < 180 {
		t.Errorf("linear light midpoint too dark: %v", mid)
	}
}

func TestStripANSI(t *testing.T) {
	in := "\x1b[0;38;2;1;2;3mhello\x1b[0m world\x1b]0;title\x07"
	if got := StripANSI(in); got != "hello world" {
		t.Errorf("StripANSI = %q", got)
	}
}
