// Package chart implements a high quality terminal charting engine.
//
// The engine renders into a cell canvas that understands sub-cell geometry
// (braille dots, eighth blocks, quadrants) so charts can use far more
// resolution than the character grid alone would allow.
package chart

import (
	"math"
	"strconv"
	"strings"
)

// Color is a 24 bit RGB color.
type Color struct {
	R, G, B uint8
}

// RGB builds a Color.
func RGB(r, g, b uint8) Color { return Color{r, g, b} }

// Hex parses "#rrggbb", "rrggbb", "#rgb" or "rgb". The bool reports success.
func Hex(s string) (Color, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return Color{}, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return Color{}, false
	}
	return Color{uint8(v >> 16), uint8(v >> 8), uint8(v)}, true
}

// MustHex is Hex but panics on malformed input. For compile time constants.
func MustHex(s string) Color {
	c, ok := Hex(s)
	if !ok {
		panic("chart: invalid color " + strconv.Quote(s))
	}
	return c
}

// srgbToLinear and linearToSrgb are per channel transfer functions.
func srgbToLinear(v uint8) float64 {
	f := float64(v) / 255
	if f <= 0.04045 {
		return f / 12.92
	}
	return math.Pow((f+0.055)/1.055, 2.4)
}

func linearToSrgb(f float64) uint8 {
	if f <= 0.0031308 {
		f *= 12.92
	} else {
		f = 1.055*math.Pow(f, 1/2.4) - 0.055
	}
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	return uint8(math.Round(f * 255))
}

// Lerp blends a and b by t in [0,1]. Blending happens in linear light so
// gradients keep their luminance and do not turn muddy in the midtones.
func Lerp(a, b Color, t float64) Color {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		return b
	}
	return Color{
		R: linearToSrgb(srgbToLinear(a.R)*(1-t) + srgbToLinear(b.R)*t),
		G: linearToSrgb(srgbToLinear(a.G)*(1-t) + srgbToLinear(b.G)*t),
		B: linearToSrgb(srgbToLinear(a.B)*(1-t) + srgbToLinear(b.B)*t),
	}
}

// Mul scales a color's channels, useful for quick shading.
func Mul(c Color, f float64) Color {
	return Color{
		R: uint8(math.Min(255, float64(c.R)*f)),
		G: uint8(math.Min(255, float64(c.G)*f)),
		B: uint8(math.Min(255, float64(c.B)*f)),
	}
}

// Ramp is a multi stop gradient.
type Ramp []Color

// NewRamp builds a ramp from at least two stops.
func NewRamp(stops ...Color) Ramp {
	if len(stops) == 1 {
		stops = append(stops, stops[0])
	}
	return Ramp(stops)
}

// At samples the ramp at t in [0,1] with linear interpolation between stops.
func (r Ramp) At(t float64) Color {
	if len(r) == 0 {
		return Color{}
	}
	if len(r) == 1 {
		return r[0]
	}
	if t <= 0 {
		return r[0]
	}
	if t >= 1 {
		return r[len(r)-1]
	}
	pos := t * float64(len(r)-1)
	i := int(pos)
	if i >= len(r)-1 {
		return r[len(r)-1]
	}
	return Lerp(r[i], r[i+1], pos-float64(i))
}

// ColorMode describes how many colors the terminal can show.
type ColorMode uint8

const (
	// ColorNone disables color entirely (attributes and glyph density only).
	ColorNone ColorMode = iota
	// ColorBasic is the classic 16 color ANSI palette.
	ColorBasic
	// Color256 is the xterm 256 color palette.
	Color256
	// ColorTrue is 24 bit direct color.
	ColorTrue
)

// String implements fmt.Stringer.
func (m ColorMode) String() string {
	switch m {
	case ColorTrue:
		return "truecolor"
	case Color256:
		return "256"
	case ColorBasic:
		return "16"
	default:
		return "none"
	}
}

// ParseColorMode maps a user supplied name to a ColorMode.
func ParseColorMode(s string) (ColorMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "truecolor", "true", "24", "24bit", "direct":
		return ColorTrue, true
	case "256", "256color":
		return Color256, true
	case "16", "basic", "ansi", "8":
		return ColorBasic, true
	case "none", "off", "mono", "no":
		return ColorNone, true
	}
	return ColorTrue, false
}

// ansi16 is the conventional xterm rendering of the 16 ANSI colors.
var ansi16 = [16]Color{
	{0x00, 0x00, 0x00}, {0xcd, 0x00, 0x00}, {0x00, 0xcd, 0x00}, {0xcd, 0xcd, 0x00},
	{0x00, 0x00, 0xee}, {0xcd, 0x00, 0xcd}, {0x00, 0xcd, 0xcd}, {0xe5, 0xe5, 0xe5},
	{0x7f, 0x7f, 0x7f}, {0xff, 0x00, 0x00}, {0x00, 0xff, 0x00}, {0xff, 0xff, 0x00},
	{0x5c, 0x5c, 0xff}, {0xff, 0x00, 0xff}, {0x00, 0xff, 0xff}, {0xff, 0xff, 0xff},
}

var cubeSteps = [6]uint8{0, 95, 135, 175, 215, 255}

// xterm256 resolves palette index to RGB.
func xterm256(i int) Color {
	switch {
	case i < 16:
		return ansi16[i]
	case i < 232:
		i -= 16
		return Color{cubeSteps[i/36], cubeSteps[(i/6)%6], cubeSteps[i%6]}
	default:
		v := uint8(8 + (i-232)*10)
		return Color{v, v, v}
	}
}

// nearest256 finds the closest xterm256 index for c (16..255, skipping the
// system colors so that themes stay stable across terminal palettes).
func nearest256(c Color) int {
	best, bestD := 16, math.MaxFloat64
	for i := 16; i < 256; i++ {
		p := xterm256(i)
		d := colorDist(c, p)
		if d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

func nearest16(c Color) int {
	best, bestD := 7, math.MaxFloat64
	for i := 0; i < 16; i++ {
		d := colorDist(c, ansi16[i])
		if d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

// colorDist is a perceptual-ish distance (weighted euclidean in sRGB).
func colorDist(a, b Color) float64 {
	dr := float64(a.R) - float64(b.R)
	dg := float64(a.G) - float64(b.G)
	db := float64(a.B) - float64(b.B)
	return 0.30*dr*dr + 0.59*dg*dg + 0.11*db*db
}
