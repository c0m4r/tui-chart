package chart

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// Palette holds the neutral colors a chart needs. Series colors come from
// each series' own ramp.
type Palette struct {
	BG    Color
	FG    Color
	Muted Color
	Grid  Color
}

// Options configures a chart drawing pass.
type Options struct {
	// Renderer selects the drawing style.
	Renderer Renderer
	// Mode is the terminal color capability.
	Mode ColorMode
	// Palette provides neutral colors.
	Palette Palette
	// Unit formats values on the axis and in legends.
	Unit Unit
	// Window is the number of samples the horizontal axis spans.
	Window int
	// Interval is the sampling period, used for time axis labels.
	Interval time.Duration
	// Now is the timestamp of the newest sample.
	Now time.Time
	// Stacked sums the series instead of overlaying them.
	Stacked bool
	// Smooth applies Catmull-Rom interpolation between samples.
	Smooth bool
	// LineFill shades the area under line charts.
	LineFill bool
	// Grid draws horizontal grid lines at each tick.
	Grid bool
	// Axis draws the vertical axis with value labels.
	Axis bool
	// TimeAxis draws relative time labels under the plot.
	TimeAxis bool
	// YMin/YMax pin the vertical range when HasYMin/HasYMax is set.
	YMin, YMax       float64
	HasYMin, HasYMax bool
	// Baseline is the value the area fill starts from.
	Baseline float64
	// Cursor marks the newest sample.
	Cursor bool
	// Average draws a dotted mean line.
	Average bool
	// Hint stabilizes the vertical range between frames.
	Hint *Scale
	// BaselineZero forces the vertical range to include zero.
	BaselineZero bool
}

// defaultOptions fills in sane defaults for unset fields.
func (o Options) withDefaults() Options {
	if o.Window < 1 {
		o.Window = 60
	}
	if o.Interval <= 0 {
		o.Interval = time.Second
	}
	if o.Palette.BG == (Color{}) && o.Palette.FG == (Color{}) {
		o.Palette = DefaultPalette()
	}
	return o
}

// DefaultPalette is a neutral dark palette.
func DefaultPalette() Palette {
	return Palette{
		BG:    MustHex("#101216"),
		FG:    MustHex("#c8ccd4"),
		Muted: MustHex("#6b7280"),
		Grid:  MustHex("#2a2f3a"),
	}
}

// DataRange returns the value range of the visible window across all series.
func DataRange(series []*Series, window int, stacked bool) (float64, float64, bool) {
	lo, hi := math.Inf(1), math.Inf(-1)
	found := false
	for _, s := range visibleSeries(series) {
		r := s.Data
		if r == nil || r.Len() == 0 {
			continue
		}
		n := r.Len()
		if window > 0 && n > window {
			n = window
		}
		sum := 0.0
		cnt := 0
		for i := r.Len() - n; i < r.Len(); i++ {
			v := r.At(i)
			if math.IsNaN(v) {
				continue
			}
			found = true
			if stacked {
				sum += v
				cnt++
				continue
			}
			lo = math.Min(lo, v)
			hi = math.Max(hi, v)
		}
		if stacked && cnt > 0 {
			lo = math.Min(lo, sum)
			hi = math.Max(hi, sum)
		}
	}
	if !found {
		return 0, 0, false
	}
	if lo > hi {
		lo, hi = hi, lo
	}
	return lo, hi, true
}

// stabilizeUnit is Stabilize with unit aware tick steps.
func stabilizeUnit(u Unit, prev *Scale, lo, hi float64, minTicks, maxTicks int, hardLo, hardHi *float64, baselineZero bool) Scale {
	if baselineZero {
		if lo > 0 {
			lo = 0
		}
		if hi < 0 {
			hi = 0
		}
	}
	sc := NewScaleUnit(u, lo, hi, maxTicks, hardLo, hardHi)
	if prev == nil || len(prev.Ticks) < 2 {
		return sc
	}
	if prev.Max >= hi && prev.Min <= lo {
		slack := (prev.Max - prev.Min) * 0.45
		if prev.Max-hi <= slack {
			sc2 := Scale{Min: prev.Min, Max: prev.Max, Step: prev.Step}
			sc2.rebuildTicks(maxTicks, u)
			return sc2
		}
	}
	return sc
}

// Stabilize computes a vertical scale, reusing the previous one while it still
// contains the data so that the axis does not jump every frame.
func Stabilize(prev *Scale, lo, hi float64, minTicks, maxTicks int, hardLo, hardHi *float64, baselineZero bool) Scale {
	return stabilizeUnit(UnitNone, prev, lo, hi, minTicks, maxTicks, hardLo, hardHi, baselineZero)
}

// rebuildTicks recomputes tick marks for an existing range.
func (s *Scale) rebuildTicks(maxTicks int, unit Unit) {
	if maxTicks < 2 {
		maxTicks = 2
	}
	step := niceStepFor(unit, s.Max-s.Min, maxTicks)
	if step <= 0 {
		step = 1
	}
	s.Step = step
	s.Ticks = s.Ticks[:0]
	for v := math.Ceil(s.Min/step) * step; v <= s.Max+step*1e-9; v += step {
		s.Ticks = append(s.Ticks, roundTick(v, step))
		if len(s.Ticks) > 64 {
			break
		}
	}
}

// Geometry describes the resolved layout of a chart inside its canvas region.
type Geometry struct {
	AxisW int
	PlotX int
	PlotY int
	PlotW int
	PlotH int
	Scale Scale
}

// Layout resolves the plot geometry without drawing. Useful for hit testing.
func Layout(x, y, w, h int, series []*Series, opt Options) Geometry {
	opt = opt.withDefaults()
	g := Geometry{}
	if w <= 0 || h <= 0 {
		return g
	}
	plotH := h
	if opt.TimeAxis {
		if h >= 8 {
			plotH = h - 2
		} else {
			plotH = h - 1
		}
	}
	if plotH < 1 {
		plotH = 1
	}
	lo, hi, ok := DataRange(series, opt.Window, opt.Stacked)
	if !ok {
		lo, hi = 0, 1
	}
	var hardLo, hardHi *float64
	if opt.HasYMin {
		v := opt.YMin
		hardLo = &v
	}
	if opt.HasYMax {
		v := opt.YMax
		hardHi = &v
	}
	ticks := tickCount(plotH)
	var sc Scale
	switch {
	case opt.HasYMin && opt.HasYMax && opt.YMax > opt.YMin:
		// A pinned range: the axis must not move at all.
		sc = Scale{Min: opt.YMin, Max: opt.YMax}
		sc.rebuildTicks(ticks, opt.Unit)
		if len(sc.Ticks) < 2 {
			sc = NewScaleUnit(opt.Unit, opt.YMin, opt.YMax, ticks, hardLo, hardHi)
		}
	default:
		sc = stabilizeUnit(opt.Unit, opt.Hint, lo, hi, 2, ticks, hardLo, hardHi, opt.BaselineZero)
	}
	g.Scale = sc
	axisW := 0
	if opt.Axis {
		axisW = axisWidth(sc, opt.Unit) + 1
	}
	if axisW > w-4 {
		axisW = 0
	}
	g.AxisW = axisW
	g.PlotX = x + axisW
	g.PlotY = y
	g.PlotW = w - axisW
	g.PlotH = plotH
	return g
}

func tickCount(plotH int) int {
	n := plotH/2 + 1
	if n < 2 {
		n = 2
	}
	if n > 8 {
		n = 8
	}
	return n
}

func axisWidth(sc Scale, u Unit) int {
	max := 0
	for _, t := range sc.Ticks {
		if l := len([]rune(u.FormatShort(t))); l > max {
			max = l
		}
	}
	if max == 0 {
		max = 3
	}
	return max
}

// Draw renders a complete chart with axes into the canvas region.
func Draw(c *Canvas, x, y, w, h int, series []*Series, opt Options) Geometry {
	opt = opt.withDefaults()
	if c == nil || w <= 0 || h <= 0 {
		return Geometry{}
	}
	g := Layout(x, y, w, h, series, opt)
	if g.PlotW <= 0 || g.PlotH <= 0 {
		return g
	}
	p := &plot{
		c:      c,
		x0:     g.PlotX,
		y0:     g.PlotY,
		w:      g.PlotW,
		h:      g.PlotH,
		scale:  g.Scale,
		series: series,
		window: opt.Window,
		opt:    opt,
	}
	if p.opt.Baseline < g.Scale.Min {
		p.opt.Baseline = g.Scale.Min
	}
	if opt.Grid {
		// The grid belongs behind the data: it gives a scale reference in the
		// empty part of the plot without speckling the filled area.
		p.drawGrid()
	}
	switch opt.Renderer {
	case RendererLine:
		p.drawLine()
	case RendererBars:
		p.drawBars()
	default:
		p.drawArea()
	}
	if opt.Average {
		p.drawAverage()
	}
	if opt.Axis {
		p.drawYAxis(g)
	}
	if opt.TimeAxis {
		p.drawTimeAxis(g)
	}
	return g
}

// drawGrid paints horizontal grid lines over the drawn data.
func (p *plot) drawGrid() {
	col := Lerp(p.opt.Palette.BG, p.opt.Palette.FG, 0.20)
	for _, t := range p.scale.Ticks {
		row := p.yOf(t)
		if row < p.y0 || row >= p.y0+p.h {
			continue
		}
		for x := 0; x < p.w; x++ {
			if x%3 == 0 {
				p.c.Set(p.x0+x, row, '·', Style{FG: col, BG: p.opt.Palette.BG})
			}
		}
	}
}

// drawYAxis writes the value labels in the left gutter.
func (p *plot) drawYAxis(g Geometry) {
	if g.AxisW <= 0 {
		return
	}
	muted := p.opt.Palette.Muted
	strong := Lerp(p.opt.Palette.Muted, p.opt.Palette.FG, 0.55)
	lastRow := -2
	// Walk the ticks from the top of the plot downwards so the highest value
	// gets the label slot it deserves.
	for i := len(p.scale.Ticks) - 1; i >= 0; i-- {
		t := p.scale.Ticks[i]
		row := p.yOf(t)
		if row < p.y0 || row >= p.y0+p.h {
			continue
		}
		if row-lastRow < 1 {
			continue
		}
		lastRow = row
		label := p.opt.Unit.FormatShort(t)
		fg := muted
		if t == p.scale.Max || t == p.scale.Min {
			fg = strong
		}
		p.c.TextRight(p.x0-1, row, label, Style{FG: fg, BG: p.opt.Palette.BG})
	}
}

// drawTimeAxis writes the axis rule and relative time labels.
func (p *plot) drawTimeAxis(g Geometry) {
	axisRow := p.y0 + g.PlotH
	muted := p.opt.Palette.Muted
	if g.PlotH < p.h || p.h > 2 {
		// axis rule sits directly under the plot
		lineCol := Lerp(p.opt.Palette.BG, p.opt.Palette.Muted, 0.55)
		p.c.HLine(p.x0, p.x0+p.w-1, axisRow, '─', Style{FG: lineCol, BG: p.opt.Palette.BG})
		p.c.Set(p.x0-1, axisRow, '└', Style{FG: lineCol, BG: p.opt.Palette.BG})
	}
	labelRow := axisRow + 1
	if labelRow >= p.y0+p.h+2 {
		return
	}
	span := time.Duration(p.opt.Window) * p.opt.Interval
	n := p.w / 14
	if n < 2 {
		n = 2
	}
	if n > 5 {
		n = 5
	}
	type label struct {
		text string
		col  int
	}
	labels := make([]label, 0, n)
	for i := 0; i < n; i++ {
		frac := float64(i) / float64(n-1)
		txt := ""
		if i == n-1 {
			txt = "now"
		} else {
			d := time.Duration(float64(span) * (1 - frac))
			txt = "-" + relativeLabel(d)
		}
		col := int(math.Round(frac * float64(p.w-1)))
		if i == n-1 {
			col = p.w - len([]rune(txt))
		}
		if col < 0 {
			col = 0
		}
		if i > 0 && i < n-1 {
			col -= len([]rune(txt)) / 2
		}
		if i > 0 && col <= labels[len(labels)-1].col+len([]rune(labels[len(labels)-1].text)) {
			continue
		}
		labels = append(labels, label{text: txt, col: col})
	}
	for i, l := range labels {
		fg := muted
		if i == len(labels)-1 {
			fg = Lerp(muted, p.opt.Palette.FG, 0.7)
		}
		p.c.Text(p.x0+l.col, labelRow, l.text, Style{FG: fg, BG: p.opt.Palette.BG})
	}
}

// relativeLabel formats an age for the time axis, preferring seconds for the
// short windows used in live monitoring.
func relativeLabel(d time.Duration) string {
	secs := d.Seconds()
	switch {
	case secs < 180:
		return strconv.Itoa(int(math.Round(secs))) + "s"
	case secs < 5400:
		return strconv.Itoa(int(math.Round(secs/60))) + "m"
	case secs < 172800:
		return strconv.Itoa(int(math.Round(secs/3600))) + "h"
	default:
		return strconv.Itoa(int(math.Round(secs/86400))) + "d"
	}
}

// LegendItem is one entry of a chart legend.
type LegendItem struct {
	Name  string
	Value string
	Color Color
}

// LegendItems builds legend entries with the newest value of each series.
func LegendItems(series []*Series, unit Unit, mode ColorMode) []LegendItem {
	out := make([]LegendItem, 0, len(series))
	for _, s := range visibleSeries(series) {
		it := LegendItem{Name: s.Name}
		if s.Ramp != nil {
			it.Color = s.Ramp.At(0.85)
		}
		if v, ok := s.Last(); ok {
			it.Value = unit.Format(v)
		} else {
			it.Value = "--"
		}
		out = append(out, it)
	}
	return out
}

// DrawSpark renders a compact chart with no axes, for sidebars and headers.
func DrawSpark(c *Canvas, x, y, w, h int, s *Series, opt Options) {
	opt = opt.withDefaults()
	if c == nil || w <= 0 || h <= 0 || s == nil || s.Len() == 0 {
		return
	}
	lo, hi, ok := DataRange([]*Series{s}, opt.Window, false)
	if !ok {
		return
	}
	pad := (hi - lo) * 0.12
	if pad == 0 {
		pad = math.Max(math.Abs(hi)*0.05, 0.5)
	}
	sc := Stabilize(opt.Hint, lo-pad, hi+pad, 2, 3, nil, nil, opt.BaselineZero)
	p := &plot{c: c, x0: x, y0: y, w: w, h: h, scale: sc, series: []*Series{s}, window: opt.Window, opt: opt}
	switch opt.Renderer {
	case RendererLine:
		p.drawLine()
	default:
		pr := buildProfile(s, w, opt.Window, false)
		for col := 0; col < w; col++ {
			if !pr.ok[col] {
				continue
			}
			p.fillBand(x+col, sc.Min, pr.columnValue(col), s.Ramp, true, true)
		}
	}
}

// StripANSI removes escape sequences, used by tests and plain output modes.
func StripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			continue
		}
		j := i + 1
		if j >= len(s) {
			break
		}
		switch s[j] {
		case '[': // CSI ... final byte in @..~
			j++
			for j < len(s) && !(s[j] >= '@' && s[j] <= '~') {
				j++
			}
			i = j
		case ']': // OSC ... BEL or ST
			j++
			for j < len(s) && s[j] != 0x07 {
				if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
					j++
					break
				}
				j++
			}
			i = j
		default: // two byte escape
			i = j
		}
	}
	return b.String()
}
