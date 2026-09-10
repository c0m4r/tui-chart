package chart

import (
	"math"
)

// Renderer selects the drawing style used for a chart.
type Renderer uint8

// Available renderers.
const (
	// RendererArea draws a gradient filled area with sub-cell top edges.
	RendererArea Renderer = iota
	// RendererLine draws braille polylines with sub-cell resolution.
	RendererLine
	// RendererBars draws one gradient bar per sample.
	RendererBars
)

// String implements fmt.Stringer.
func (r Renderer) String() string {
	switch r {
	case RendererLine:
		return "line"
	case RendererBars:
		return "bars"
	default:
		return "area"
	}
}

// ParseRenderer maps a user supplied name to a Renderer.
func ParseRenderer(s string) (Renderer, bool) {
	switch s {
	case "area", "filled", "fill":
		return RendererArea, true
	case "line", "lines", "braille", "graph":
		return RendererLine, true
	case "bars", "bar", "histogram":
		return RendererBars, true
	}
	return RendererArea, false
}

var eighthRunes = [9]rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

func eighthRune(idx int) rune {
	if idx < 1 {
		idx = 1
	}
	if idx > 8 {
		idx = 8
	}
	return eighthRunes[idx]
}

// profile holds the per screen column summary of one series.
type profile struct {
	top  []float64 // highest sample covered by the column
	bot  []float64 // lowest sample covered by the column
	mid  []float64 // interpolated value at the column centre
	ok   []bool
	full bool // true when every column has data
}

// buildProfile resamples a series onto w screen columns. The newest sample
// sits at the right edge and the buffer grows leftwards, which gives the
// familiar real-time "chart fills from the right" behaviour.
func buildProfile(s *Series, w, window int, smooth bool) profile {
	p := profile{
		top: make([]float64, w),
		bot: make([]float64, w),
		mid: make([]float64, w),
		ok:  make([]bool, w),
	}
	if s == nil || s.Data == nil || w <= 0 || window <= 0 {
		return p
	}
	r := s.Data
	n := r.Len()
	if n == 0 {
		return p
	}
	step := float64(w) / float64(window)
	all := true
	for x := 0; x < w; x++ {
		pos := float64(n-1) - float64(w-1-x)/step
		if pos < 0 {
			all = false
			continue
		}
		if pos > float64(n-1) {
			pos = float64(n - 1)
		}
		lo, hi := pos-0.5/step, pos+0.5/step
		if lo < 0 {
			lo = 0
		}
		if hi > float64(n-1) {
			hi = float64(n - 1)
		}
		if hi-lo < 1.0 {
			v, ok := interpRing(r, pos, smooth)
			if !ok {
				all = false
				continue
			}
			mn, _ := interpRing(r, lo, smooth)
			mx, _ := interpRing(r, hi, smooth)
			p.top[x], p.bot[x], p.mid[x] = math.Max(mn, mx), math.Min(mn, mx), v
		} else {
			i0, i1 := int(math.Floor(lo)), int(math.Ceil(hi))
			if i0 < 0 {
				i0 = 0
			}
			if i1 > n-1 {
				i1 = n - 1
			}
			mn, mx := math.Inf(1), math.Inf(-1)
			for i := i0; i <= i1; i++ {
				v := r.At(i)
				mn = math.Min(mn, v)
				mx = math.Max(mx, v)
			}
			p.bot[x], p.top[x] = mn, mx
			p.mid[x] = mx // dense data: preserve spikes in the outline
		}
		p.ok[x] = true
	}
	p.full = all
	return p
}

// interpRing samples the ring at a fractional index, optionally with
// Catmull-Rom smoothing for pleasing curves.
func interpRing(r *Ring, pos float64, smooth bool) (float64, bool) {
	n := r.Len()
	if n == 0 || pos < 0 || pos > float64(n-1)+1e-9 {
		return 0, false
	}
	if pos >= float64(n-1) {
		return r.At(n - 1), true
	}
	i := int(math.Floor(pos))
	f := pos - float64(i)
	if !smooth || n < 3 {
		return r.At(i) + (r.At(i+1)-r.At(i))*f, true
	}
	p0 := r.At(clampInt(i-1, 0, n-1))
	p1 := r.At(i)
	p2 := r.At(i + 1)
	p3 := r.At(clampInt(i+2, 0, n-1))
	// Catmull-Rom
	v := 0.5 * ((2 * p1) + (-p0+p2)*f + (2*p0-5*p1+4*p2-p3)*f*f + (-p0+3*p1-3*p2+p3)*f*f*f)
	return v, true
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// columnValue returns the value used for the top edge of the area fill.
func (p profile) columnValue(x int) float64 { return p.mid[x] }

// plot is the drawing context for one chart.
type plot struct {
	c      *Canvas
	x0, y0 int // top left corner of the plot area
	w, h   int // plot area size in cells
	scale  Scale
	series []*Series
	window int
	opt    Options
}

// yOf maps a value to a cell row (absolute canvas row).
func (p *plot) yOf(v float64) int {
	n := p.scale.Norm(v)
	return p.y0 + int(math.Round((1-n)*float64(p.h-1)))
}

// yFloat maps a value to a fractional cell row measured from the plot bottom.
func (p *plot) yFloat(v float64) float64 {
	return p.scale.Norm(v) * float64(p.h)
}

// fillBand paints the vertical band [vlo,vhi] in column x. Full cells become
// solid blocks, the leading edge uses eighth blocks for sub-cell precision.
// bottomEdge marks that the band owns the partially covered cell at its lower
// end; when it is false that cell belongs to the band below (stacked charts),
// so only its background is claimed.
func (p *plot) fillBand(x int, vlo, vhi float64, ramp Ramp, bottomEdge, topEdge bool) {
	if x < p.x0 || x >= p.x0+p.w {
		return
	}
	hl := float64(p.h)
	yLo := p.yFloat(vlo)
	yHi := p.yFloat(vhi)
	if yHi <= 0 || yLo >= hl {
		return
	}
	if yLo < 0 {
		yLo = 0
	}
	if yHi > hl {
		yHi = hl
	}
	start := int(math.Floor(yLo))
	end := int(math.Ceil(yHi))
	if start < 0 {
		start = 0
	}
	if end > p.h {
		end = p.h
	}
	for k := start; k < end; k++ {
		cov := math.Min(float64(k+1), yHi) - math.Max(float64(k), yLo)
		if cov <= 0.02 {
			continue
		}
		bottomPartial := k == start && (yLo-float64(k)) > 0.02
		row := p.y0 + p.h - 1 - k
		t := (float64(k) + 0.5) / hl
		col := ramp.At(t)
		if cov >= 0.985 {
			p.c.Set(x, row, '█', Style{FG: col, BG: p.opt.Palette.BG})
			continue
		}
		if bottomPartial && !bottomEdge {
			// The band starts inside this cell. The series below already
			// painted the glyph, so claim the space above it by painting the
			// cell background: the boundary then lands on a 1/8 cell grid
			// without leaving a gap of empty background behind.
			if cov >= 0.125 {
				p.c.SetBG(x, row, col)
			}
			continue
		}
		idx := int(math.Round(cov * 8))
		if idx < 1 {
			idx = 1
		}
		if idx > 8 {
			idx = 8
		}
		if topEdge && idx < 8 {
			// Brighten the leading edge so the profile stands out.
			col = Lerp(col, p.opt.Palette.FG, 0.28)
		}
		p.c.Set(x, row, eighthRune(idx), Style{FG: col, BG: p.opt.Palette.BG})
	}
}

// drawArea renders gradient filled areas (stacked when opt.Stacked).
func (p *plot) drawArea() {
	visible := visibleSeries(p.series)
	if len(visible) == 0 {
		return
	}
	profiles := make([]profile, len(visible))
	for i, s := range visible {
		profiles[i] = buildProfile(s, p.w, p.window, p.opt.Smooth)
	}
	for x := 0; x < p.w; x++ {
		cum := p.opt.Baseline
		for i, s := range visible {
			pr := &profiles[i]
			if !pr.ok[x] {
				continue
			}
			v := pr.columnValue(x)
			if p.opt.Stacked {
				lo, hi := cum, cum+v
				cum = hi
				p.fillBand(p.x0+x, lo, hi, s.Ramp, i == 0, true)
			} else {
				p.fillBand(p.x0+x, p.scale.Min, v, s.Ramp, true, true)
			}
		}
	}
}

// drawLine renders braille polylines at sub-cell resolution.
func (p *plot) drawLine() {
	visible := visibleSeries(p.series)
	if len(visible) == 0 {
		return
	}
	dotW, dotH := p.w*2, p.h*4
	if dotW <= 0 || dotH <= 0 {
		return
	}
	for _, s := range visible {
		n := s.Len()
		if n == 0 {
			continue
		}
		if p.opt.LineFill {
			pr := buildProfile(s, p.w, p.window, p.opt.Smooth)
			for x := 0; x < p.w; x++ {
				if pr.ok[x] {
					p.fillBand(p.x0+x, p.scale.Min, pr.columnValue(x), dimRamp(s.Ramp), true, false)
				}
			}
		}
		stepDot := float64(dotW) / float64(p.window)
		prevX, prevCenter := -1, 0
		for dx := 0; dx < dotW; dx++ {
			pos := float64(n-1) - float64(dotW-1-dx)/stepDot
			if pos < 0 {
				continue
			}
			var yTop, yBot int
			if stepDot >= 1.0 {
				v, ok := interpRing(s.Data, pos, p.opt.Smooth)
				if !ok {
					continue
				}
				yTop = p.dotY(v, dotH)
				yBot = yTop
			} else {
				lo, hi := pos-0.5/stepDot, pos+0.5/stepDot
				if lo < 0 {
					lo = 0
				}
				if hi > float64(n-1) {
					hi = float64(n - 1)
				}
				i0, i1 := clampInt(int(math.Floor(lo)), 0, n-1), clampInt(int(math.Ceil(hi)), 0, n-1)
				mn, mx := math.Inf(1), math.Inf(-1)
				for i := i0; i <= i1; i++ {
					v := s.Data.At(i)
					mn = math.Min(mn, v)
					mx = math.Max(mx, v)
				}
				yTop = p.dotY(mx, dotH)
				yBot = p.dotY(mn, dotH)
			}
			center := (yTop + yBot) / 2
			x := p.x0*2 + dx
			// Bridge the diagonal from the previous column's centre so the
			// polyline stays continuous even with sparse samples.
			if prevX == x-1 && prevCenter != center {
				lo, hi := prevCenter, center
				if lo > hi {
					lo, hi = hi, lo
				}
				for yy := lo; yy <= hi; yy++ {
					p.c.Dot(x-1, p.y0*4+yy, s.dotStyle(yy, dotH, p.opt.Palette.BG))
				}
			}
			for yy := yTop; yy <= yBot; yy++ {
				p.c.Dot(x, p.y0*4+yy, s.dotStyle(yy, dotH, p.opt.Palette.BG))
			}
			prevX, prevCenter = x, center
		}
	}
	// Cursor markers are drawn on top of every series.
	if p.opt.Cursor {
		for _, s := range visible {
			v, ok := s.Last()
			if !ok {
				continue
			}
			lastX := p.x0*2 + dotW - 1
			y := p.dotY(v, dotH)
			base := s.Ramp.At(p.scale.Norm(v))
			p.c.Dot(lastX, p.y0*4+y, Style{FG: Lerp(base, p.opt.Palette.FG, 0.7), BG: p.opt.Palette.BG})
			p.c.Dot(lastX-1, p.y0*4+y, Style{FG: Lerp(base, p.opt.Palette.FG, 0.35), BG: p.opt.Palette.BG})
		}
	}
}

// dotStyle colours a braille dot by its height inside the plot.
func (s *Series) dotStyle(y, dotH int, bg Color) Style {
	t := 1 - float64(y)/float64(dotH-1)
	return Style{FG: s.Ramp.At(t), BG: bg}
}

// dotY maps a value to a braille dot row inside the plot.
func (p *plot) dotY(v float64, dotH int) int {
	y := int(math.Round((1 - p.scale.Norm(v)) * float64(dotH-1)))
	return clampInt(y, 0, dotH-1)
}

// drawBars renders one vertical bar per sample.
func (p *plot) drawBars() {
	visible := visibleSeries(p.series)
	if len(visible) == 0 {
		return
	}
	slot := float64(p.w) / float64(p.window)
	barW := int(math.Round(slot))
	if barW > 2 {
		barW--
	}
	if barW < 1 {
		barW = 1
	}
	for _, s := range visible {
		n := s.Len()
		if n == 0 {
			continue
		}
		for k := 0; k < n; k++ {
			idx := n - 1 - k // 0 = newest
			xEnd := p.x0 + p.w - 1 - int(math.Round(float64(idx)*slot))
			v := s.Data.At(n - 1 - idx)
			for b := 0; b < barW; b++ {
				x := xEnd - b
				if x < p.x0 {
					continue
				}
				p.fillBand(x, p.scale.Min, v, s.Ramp, true, true)
			}
		}
	}
}

// drawAverage overlays a dotted line at the mean of the first visible series.
func (p *plot) drawAverage() {
	visible := visibleSeries(p.series)
	if len(visible) == 0 {
		return
	}
	st := ComputeStats(visible[0].Data, p.window)
	if st.Count == 0 {
		return
	}
	y := p.yOf(st.Avg)
	if y < p.y0 || y >= p.y0+p.h {
		return
	}
	col := Lerp(p.opt.Palette.BG, p.opt.Palette.FG, 0.30)
	for x := 0; x < p.w; x++ {
		if x%2 == 0 {
			p.c.Set(p.x0+x, y, '·', Style{FG: col, BG: p.opt.Palette.BG})
		}
	}
}

// dimRamp returns the ramp used for the faint fill under a line chart.
func dimRamp(r Ramp) Ramp {
	out := make(Ramp, len(r))
	for i, c := range r {
		out[i] = Mul(c, 0.42)
	}
	return out
}

// visibleSeries filters out hidden series.
func visibleSeries(in []*Series) []*Series {
	out := make([]*Series, 0, len(in))
	for _, s := range in {
		if s == nil || s.Hidden {
			continue
		}
		out = append(out, s)
	}
	return out
}
