package chart

import (
	"flag"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// testRamp is a three stop gradient used by the tests.
var testRamp = NewRamp(MustHex("#22c55e"), MustHex("#facc15"), MustHex("#ef4444"))

func TestRingBuffer(t *testing.T) {
	r := NewRing(4)
	if r.Len() != 0 || r.Full() {
		t.Fatalf("fresh ring: len=%d full=%v", r.Len(), r.Full())
	}
	for i := 1; i <= 6; i++ {
		r.Push(float64(i))
	}
	if r.Len() != 4 {
		t.Fatalf("len = %d, want 4", r.Len())
	}
	want := []float64{3, 4, 5, 6}
	got := r.Slice(nil)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("slice = %v, want %v", got, want)
		}
	}
	if v, _ := r.Last(); v != 6 {
		t.Errorf("last = %v", v)
	}
	if v, _ := r.Oldest(); v != 3 {
		t.Errorf("oldest = %v", v)
	}
	r.Resize(2)
	if got := r.Slice(nil); len(got) != 2 || got[0] != 5 || got[1] != 6 {
		t.Errorf("after resize = %v, want [5 6]", got)
	}
	r.Resize(8)
	if got := r.Slice(nil); len(got) != 2 {
		t.Errorf("growing must keep the samples, got %v", got)
	}
}

func TestComputeStats(t *testing.T) {
	r := NewRing(5)
	for _, v := range []float64{2, 4, 4, 4, 10} {
		r.Push(v)
	}
	st := ComputeStats(r, 0)
	if st.Count != 5 || st.Last != 10 || st.Min != 2 || st.Max != 10 {
		t.Fatalf("stats = %+v", st)
	}
	if math.Abs(st.Avg-4.8) > 1e-9 {
		t.Errorf("avg = %v, want 4.8", st.Avg)
	}
	if st.Median != 4 {
		t.Errorf("median = %v, want 4", st.Median)
	}
	// p95 uses linear interpolation between the two nearest samples.
	if math.Abs(st.P95-8.8) > 1e-9 {
		t.Errorf("p95 = %v, want 8.8", st.P95)
	}
	// A window smaller than the buffer only looks at the newest samples.
	st = ComputeStats(r, 2)
	if st.Count != 2 || st.Min != 4 || st.Max != 10 {
		t.Errorf("windowed stats = %+v", st)
	}
}

// synthetic builds a deterministic series with a known shape.
func synthetic(name string, n int) *Series {
	s := NewSeries(name, n, testRamp)
	for i := 0; i < n; i++ {
		v := 30 + 20*math.Sin(float64(i)/6) + 5*math.Sin(float64(i)/2.5)
		s.Push(v)
	}
	return s
}

func TestBuildProfileGrowsFromTheRight(t *testing.T) {
	s := NewSeries("x", 60, testRamp)
	for i := 0; i < 10; i++ {
		s.Push(float64(i))
	}
	p := buildProfile(s, 40, 60, false)
	// The newest sample sits at the last column and older ones fill leftwards.
	if !p.ok[39] {
		t.Fatal("rightmost column must have data")
	}
	empty := 0
	for x := 0; x < 40; x++ {
		if !p.ok[x] {
			empty++
		}
	}
	if empty == 0 {
		t.Error("a partially filled history should leave the left part empty")
	}
	if got := p.columnValue(39); math.Abs(got-9) > 1e-9 {
		t.Errorf("newest column value = %v, want 9", got)
	}
}

func TestBuildProfileDownsamplesWithEnvelope(t *testing.T) {
	s := NewSeries("x", 400, testRamp)
	for i := 0; i < 400; i++ {
		v := 10.0
		if i%20 == 0 {
			v = 90 // spikes must survive downsampling
		}
		s.Push(v)
	}
	p := buildProfile(s, 20, 400, false)
	maxTop := 0.0
	for x := 0; x < 20; x++ {
		maxTop = math.Max(maxTop, p.top[x])
	}
	if maxTop < 90 {
		t.Errorf("downsampling lost the spikes: max = %v", maxTop)
	}
	if p.bot[0] > 10.001 {
		t.Errorf("envelope minimum = %v, want 10", p.bot[0])
	}
}

func TestDrawAreaProducesFilledColumns(t *testing.T) {
	s := synthetic("usage", 60)
	opt := Options{
		Mode: ColorNone, Unit: UnitPercent, Window: 60, Interval: time.Second,
		Axis: true, TimeAxis: true, HasYMin: true, HasYMax: true, YMin: 0, YMax: 100,
	}
	c := NewCanvas(80, 20)
	g := Draw(c, 0, 0, 80, 20, []*Series{s}, opt)
	if g.PlotH != 18 || g.PlotW < 60 {
		t.Fatalf("unexpected geometry %+v", g)
	}
	lines := c.PlainLines()
	if len(lines) != 20 {
		t.Fatalf("got %d lines", len(lines))
	}
	// The bottom row of the plot must be almost entirely filled.
	bottom := lines[g.PlotY+g.PlotH-1]
	filled := strings.Count(bottom, "█")
	if filled < g.PlotW-4 {
		t.Errorf("bottom row not filled: %d of %d cells: %q", filled, g.PlotW, bottom)
	}
	// A high value must produce a taller fill than a low one.
	low := &Series{Name: "low", Data: NewRing(60), Ramp: testRamp}
	high := &Series{Name: "high", Data: NewRing(60), Ramp: testRamp}
	for i := 0; i < 60; i++ {
		low.Push(10)
		high.Push(90)
	}
	colHeight := func(series *Series) int {
		c := NewCanvas(40, 12)
		Draw(c, 0, 0, 40, 12, []*Series{series}, Options{
			Mode: ColorNone, Window: 60, HasYMin: true, HasYMax: true, YMin: 0, YMax: 100,
		})
		h := 0
		for _, l := range c.PlainLines() {
			if strings.ContainsAny(l, "█▇▆▅▄▃▂▁") {
				h++
			}
		}
		return h
	}
	if colHeight(high) <= colHeight(low) {
		t.Errorf("high series (%d rows) should be taller than low (%d rows)", colHeight(high), colHeight(low))
	}
}

func TestDrawStackedBandsCoverTheStack(t *testing.T) {
	mk := func(name string, v float64) *Series {
		s := NewSeries(name, 60, testRamp)
		for i := 0; i < 60; i++ {
			s.Push(v)
		}
		return s
	}
	// Deliberately not aligned with the cell grid so sub-cell edges are used.
	series := []*Series{mk("a", 17), mk("b", 13), mk("c", 11)}
	const height = 20
	c := NewCanvas(40, height)
	Draw(c, 0, 0, 40, height, series, Options{
		Mode: ColorNone, Window: 60, Stacked: true, HasYMin: true, HasYMax: true, YMin: 0, YMax: 100,
	})
	lines := c.PlainLines()
	isFill := func(l string) bool { return strings.ContainsAny(l, "█▇▆▅▄▃▂▁") }
	filled := 0
	for _, l := range lines {
		if isFill(l) {
			filled++
		}
	}
	want := int(math.Round(0.41 * height))
	if filled < want-1 || filled > want+1 {
		t.Errorf("stack covers %d rows, want about %d", filled, want)
	}
	// The stack must be a solid block anchored at the bottom: no holes.
	for i, l := range lines {
		shouldFill := i >= height-filled
		if has := isFill(l); has != shouldFill {
			t.Fatalf("row %d filled=%v, want %v:\n%s", i, has, shouldFill, strings.Join(lines, "\n"))
		}
	}
	glyphs := map[rune]bool{}
	for _, l := range lines {
		for _, r := range l {
			if strings.ContainsRune("█▇▆▅▄▃▂▁", r) {
				glyphs[r] = true
			}
		}
	}
	if len(glyphs) < 2 {
		t.Errorf("expected sub-cell glyphs at the band edges, got %v", glyphs)
	}
}

func TestRendererSelection(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    Renderer
		want string
	}{
		{"area", RendererArea, "area"},
		{"line", RendererLine, "line"},
		{"bars", RendererBars, "bars"},
	} {
		if got := tc.r.String(); got != tc.want {
			t.Errorf("%s: %q", tc.name, got)
		}
		got, ok := ParseRenderer(tc.want)
		if !ok || got != tc.r {
			t.Errorf("ParseRenderer(%q) = %v,%v", tc.want, got, ok)
		}
	}
	if _, ok := ParseRenderer("nope"); ok {
		t.Error("ParseRenderer accepted garbage")
	}
}

// TestGoldenFrames locks down the exact rendering of every renderer. Run with
// -update to rewrite the files after an intentional visual change.
func TestGoldenFrames(t *testing.T) {
	series := synthetic("usage", 60)
	cases := []struct {
		name string
		opt  Options
	}{
		{"area", Options{Renderer: RendererArea, Grid: true, Axis: true, TimeAxis: true, Cursor: true}},
		{"line", Options{Renderer: RendererLine, Grid: true, Axis: true, TimeAxis: true, Cursor: true}},
		{"bars", Options{Renderer: RendererBars, Grid: true, Axis: true, TimeAxis: true, Cursor: true}},
		{"area_average", Options{Renderer: RendererArea, Axis: true, TimeAxis: true, Average: true}},
		{"line_fill", Options{Renderer: RendererLine, Axis: true, TimeAxis: true, LineFill: true}},
		{"smooth", Options{Renderer: RendererArea, Axis: true, Smooth: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opt := tc.opt
			opt.Mode = ColorNone
			opt.Unit = UnitPercent
			opt.Window = 60
			opt.Interval = time.Second
			opt.HasYMin, opt.HasYMax = true, true
			opt.YMin, opt.YMax = 0, 100
			opt.Baseline = 0
			c := NewCanvas(72, 16)
			c.Clear(Style{})
			Draw(c, 0, 0, 72, 16, []*Series{series}, opt)
			got := strings.Join(c.PlainLines(), "\n")
			path := filepath.Join("testdata", "golden_"+tc.name+".txt")
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("missing golden file (run go test -update): %v", err)
			}
			if strings.TrimRight(string(want), "\n") != got {
				t.Errorf("rendering changed for %s:\n--- got ---\n%s\n--- want ---\n%s",
					tc.name, got, strings.TrimRight(string(want), "\n"))
			}
		})
	}
}
