package chart

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func synthSeries(name string, n int, lo, hi float64) *Series {
	s := NewSeries(name, n, NewRamp(MustHex("#22c55e"), MustHex("#facc15"), MustHex("#ef4444")))
	for i := 0; i < n; i++ {
		v := lo + (hi-lo)*(0.5+0.5*math.Sin(float64(i)/7))
		s.Push(v)
	}
	return s
}

func TestDrawGeometry(t *testing.T) {
	s := synthSeries("usage", 60, 5, 45)
	opt := Options{
		Mode:     ColorNone,
		Unit:     UnitPercent,
		Window:   60,
		Interval: time.Second,
		Axis:     true,
		TimeAxis: true,
		HasYMin:  true,
		HasYMax:  true,
		YMin:     0,
		YMax:     100,
		Grid:     true,
		Baseline: 0,
		Cursor:   true,
		Renderer: RendererArea,
	}
	c := NewCanvas(100, 20)
	g := Draw(c, 0, 0, 100, 20, []*Series{s}, opt)
	t.Logf("geometry: axisW=%d plot=(%d,%d %dx%d) scale=%.2f..%.2f ticks=%v",
		g.AxisW, g.PlotX, g.PlotY, g.PlotW, g.PlotH, g.Scale.Min, g.Scale.Max, g.Scale.Ticks)
	lines := c.PlainLines()
	for i, l := range lines {
		t.Logf("%2d|%s|", i, l)
	}
	if len(lines) != 20 {
		t.Fatalf("expected 20 lines, got %d", len(lines))
	}
	if g.PlotH != 18 {
		t.Fatalf("expected plot height 18, got %d", g.PlotH)
	}
	found := false
	for _, l := range lines {
		if strings.Contains(l, "100%") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a 100%% axis label, lines were:\n%s", joinLines(lines))
	}
}

func joinLines(lines []string) string {
	out := ""
	for i, l := range lines {
		out += fmt.Sprintf("%2d|%s|\n", i, l)
	}
	return out
}

func TestNiceScale(t *testing.T) {
	cases := []struct {
		lo, hi float64
		ticks  int
		wantLo float64
		wantHi float64
	}{
		{0, 100, 6, 0, 100},
		{0, 0.7, 4, 0, 0.8},
		{0, 37, 5, 0, 40},
		{-5, 5, 5, -5, 5},
		{1000, 1234, 4, 1000, 1300},
	}
	for _, c := range cases {
		s := NewScale(c.lo, c.hi, c.ticks, nil, nil)
		if s.Min > c.lo || s.Max < c.hi {
			t.Errorf("NewScale(%v,%v) = %v..%v does not cover the data", c.lo, c.hi, s.Min, s.Max)
		}
		if len(s.Ticks) < 2 {
			t.Errorf("NewScale(%v,%v) produced %d ticks", c.lo, c.hi, len(s.Ticks))
		}
		t.Logf("NewScale(%g,%g,%d) = %g..%g step %g ticks %v", c.lo, c.hi, c.ticks, s.Min, s.Max, s.Step, s.Ticks)
	}
}
