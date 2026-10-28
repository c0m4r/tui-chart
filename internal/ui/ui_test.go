package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/c0m4r/tui-chart/internal/chart"
	"github.com/c0m4r/tui-chart/internal/metrics"
	"github.com/mattn/go-runewidth"
)

// testModel builds a model backed by synthetic sources, so the tests do not
// depend on the host.
func testModel(t *testing.T, w, h int) Model {
	t.Helper()
	oldProc, oldSys := metrics.ProcRoot, metrics.SysRoot
	metrics.ProcRoot, metrics.SysRoot = "nonexistent", "nonexistent"
	t.Cleanup(func() { metrics.ProcRoot, metrics.SysRoot = oldProc, oldSys })

	m := New(Config{
		Sources:  metrics.All(metrics.Options{Capacity: 120, Demo: true}),
		Interval: time.Second,
		Window:   60,
		Grid:     true,
		Sidebar:  true,
		Mode:     chart.ColorNone,
		Theme:    Themes["midnight"],
		Host:     metrics.HostInfo{Hostname: "testhost", Cores: 8, MemTotal: 16 << 30, Uptime: 3 * time.Hour},
		Version:  "test",
	})
	m = m.WithSize(w, h)
	return m.SeedHistory(90)
}

func TestFrameGeometry(t *testing.T) {
	for _, size := range [][2]int{{140, 38}, {120, 34}, {100, 30}, {80, 24}, {60, 20}, {60, 16}, {59, 15}, {40, 12}, {20, 5}, {5, 3}, {2, 2}} {
		m := testModel(t, size[0], size[1])
		lines := m.Frame(size[0], size[1])
		if len(lines) != size[1] {
			t.Errorf("%dx%d: got %d lines", size[0], size[1], len(lines))
			continue
		}
		for i, l := range lines {
			if w := runewidth.StringWidth(chart.StripANSI(l)); w != size[0] {
				t.Errorf("%dx%d: line %d is %d cells wide", size[0], size[1], i, w)
				break
			}
		}
	}
}

// TestTooSmallTerminalShowsNotice covers the sizes below MinWidth x MinHeight:
// a chart would be clipped there, so the dashboard prints a notice instead.
// Tiny terminals only have room for the truncated start of the first line.
func TestTooSmallTerminalShowsNotice(t *testing.T) {
	const notice = "terminal too small"
	for _, size := range [][2]int{{59, 15}, {60, 15}, {59, 16}, {40, 12}, {100, 5}, {20, 5}, {5, 3}, {2, 2}, {1, 1}} {
		m := testModel(t, size[0], size[1])
		lines := m.Frame(size[0], size[1])
		plain := chart.StripANSI(strings.Join(lines, "\n"))
		if strings.Contains(plain, "CPU · total") {
			t.Errorf("%dx%d: the chart should not be drawn", size[0], size[1])
		}
		shown := false
		for _, ln := range strings.Split(plain, "\n") {
			if txt := strings.TrimSpace(ln); txt != "" && strings.HasPrefix(notice, txt) {
				shown = true
				break
			}
		}
		if !shown {
			t.Errorf("%dx%d: expected the too-small notice, got %q", size[0], size[1], plain)
		}
	}
}

// TestTooSmallNoticeReportsTheRequiredSize checks that terminals wide enough
// for the full sentence get the required and the actual size.
func TestTooSmallNoticeReportsTheRequiredSize(t *testing.T) {
	m := testModel(t, 59, 15)
	plain := chart.StripANSI(strings.Join(m.Frame(59, 15), "\n"))
	for _, want := range []string{"60×16", "59×15", "resize"} {
		if !strings.Contains(plain, want) {
			t.Errorf("notice is missing %q:\n%s", want, plain)
		}
	}
}

// TestMinimumSizeDrawsChart pins the boundary: at exactly MinWidth x MinHeight
// the chart is drawn, one cell below in either direction it is not.
func TestMinimumSizeDrawsChart(t *testing.T) {
	for _, size := range [][2]int{{MinWidth, MinHeight}, {MinWidth + 1, MinHeight}, {MinWidth, MinHeight + 1}, {120, 34}} {
		m := testModel(t, size[0], size[1])
		plain := chart.StripANSI(strings.Join(m.Frame(size[0], size[1]), "\n"))
		if strings.Contains(plain, "terminal too small") {
			t.Errorf("%dx%d: the chart should be drawn", size[0], size[1])
		}
		if !strings.Contains(plain, "CPU · total") {
			t.Errorf("%dx%d: missing the chart panel title:\n%s", size[0], size[1], plain)
		}
	}
}

// TestTooSmallIgnoresMouse: with no chart on screen a click must not change
// the selection behind the notice.
func TestTooSmallIgnoresMouse(t *testing.T) {
	m := testModel(t, 50, 14)
	m.hoverTab = -1
	next, _ := m.Update(mouseMsg(4, m.computeLayout(50, 14).tabsY))
	got, ok := next.(Model)
	if !ok {
		t.Fatal("update did not return a model")
	}
	if got.active != m.active {
		t.Errorf("a click changed the source from %d to %d", m.active, got.active)
	}
	if got.hoverTab != -1 {
		t.Errorf("a click set the hover tab to %d", got.hoverTab)
	}
}

func TestFrameHasHeaderAndSources(t *testing.T) {
	m := testModel(t, 120, 34)
	frame := strings.Join(m.Frame(120, 34), "\n")
	plain := chart.StripANSI(frame)
	for _, want := range []string{"tui-chart", "testhost", "CPU", "Memory", "sources", "window", "help", "quit"} {
		if !strings.Contains(plain, want) {
			t.Errorf("frame is missing %q", want)
		}
	}
}

func TestArrowKeysSwitchSources(t *testing.T) {
	m := testModel(t, 120, 34)
	if got := m.ActiveSource().ID(); got != "cpu" {
		t.Fatalf("initial source = %s", got)
	}
	m = m.WithKey("right")
	if got := m.ActiveSource().ID(); got != "memory" {
		t.Errorf("right did not move to memory (got %s)", got)
	}
	m = m.WithKey("down")
	if got := m.ActiveSource().ID(); got != "swap" {
		t.Errorf("down did not advance (got %s)", got)
	}
	m = m.WithKey("left")
	if got := m.ActiveSource().ID(); got != "memory" {
		t.Errorf("left did not move back (got %s)", got)
	}
	m = m.WithKey("up")
	if got := m.ActiveSource().ID(); got != "cpu" {
		t.Errorf("up did not move back to cpu (got %s)", got)
	}
	// left from the first source wraps to the last one
	m = m.WithKey("left")
	if got := m.ActiveSource().ID(); got != "temperature" {
		t.Errorf("wrap-around failed (got %s)", got)
	}
	// numeric jump
	m = m.WithKey("4")
	if got := m.ActiveSource().ID(); got != "load" {
		t.Errorf("digit jump failed (got %s)", got)
	}
	// tab cycles
	m = m.WithKey("tab")
	if got := m.ActiveSource().ID(); got != "network" {
		t.Errorf("tab failed (got %s)", got)
	}
}

func TestBreakdownToggle(t *testing.T) {
	m := testModel(t, 120, 34)
	src := m.ActiveSource()
	if src.Mode() != metrics.ModeCombined {
		t.Fatal("should start in the summary view")
	}
	m = m.WithKey("d")
	if src.Mode() != metrics.ModeDetailed {
		t.Error("d did not switch to the breakdown")
	}
	if !strings.Contains(chart.StripANSI(strings.Join(m.Frame(120, 34), "\n")), "breakdown") {
		t.Error("the panel title should mention the breakdown")
	}
	m = m.WithKey("d")
	if src.Mode() != metrics.ModeCombined {
		t.Error("d did not switch back")
	}
}

func TestRenderersAndToggles(t *testing.T) {
	m := testModel(t, 120, 34)
	m = m.WithKey("v")
	if !m.rendererSet || m.renderer != chart.RendererLine {
		t.Errorf("v did not switch to lines (renderer=%v set=%v)", m.renderer, m.rendererSet)
	}
	m = m.WithKey("v")
	if m.renderer != chart.RendererBars {
		t.Errorf("second v should give bars, got %v", m.renderer)
	}
	m = m.WithKey("v")
	if m.renderer != chart.RendererArea {
		t.Errorf("third v should wrap to area, got %v", m.renderer)
	}
	grid := m.grid
	m = m.WithKey("g")
	if m.grid == grid {
		t.Error("g did not toggle the grid")
	}
	avg := m.average
	m = m.WithKey("a")
	if m.average == avg {
		t.Error("a did not toggle the average")
	}
	side := m.showSidebar
	m = m.WithKey("b")
	if m.showSidebar == side {
		t.Error("b did not toggle the sidebar")
	}
	theme := m.theme.Name
	m = m.WithKey("t")
	if m.theme.Name == theme {
		t.Error("t did not change the theme")
	}
}

func TestWindowZoom(t *testing.T) {
	m := testModel(t, 120, 34)
	start := m.windowSamples
	m = m.WithKey("+")
	if m.windowSamples <= start {
		t.Errorf("+ should widen the window (%d -> %d)", start, m.windowSamples)
	}
	wide := m.windowSamples
	m = m.WithKey("-")
	if m.windowSamples >= wide {
		t.Errorf("- should narrow the window (%d -> %d)", wide, m.windowSamples)
	}
	// The window can never exceed the history capacity.
	for i := 0; i < 20; i++ {
		m = m.WithKey("+")
	}
	if cap := m.sources[0].Capacity(); m.windowSamples > cap {
		t.Errorf("window %d exceeds capacity %d", m.windowSamples, cap)
	}
	for i := 0; i < 40; i++ {
		m = m.WithKey("-")
	}
	if m.windowSamples < 2 {
		t.Errorf("window shrank to %d", m.windowSamples)
	}
}

func TestPauseStopsSampling(t *testing.T) {
	m := testModel(t, 120, 34)
	src := m.ActiveSource()
	series := src.Series()[0]
	// The ring is already at capacity, so compare the newest value instead of
	// the sample count.
	before, _ := series.Last()
	m = m.WithKey("space")
	if !m.paused {
		t.Fatal("space did not pause")
	}
	m = m.WithTick(time.Now())
	if after, _ := series.Last(); after != before {
		t.Errorf("paused sampling still pushed data (%v -> %v)", before, after)
	}
	if !strings.Contains(chart.StripANSI(strings.Join(m.Frame(120, 34), "\n")), "PAUSED") {
		t.Error("the header should show PAUSED")
	}
	m = m.WithKey("space")
	if m.paused {
		t.Fatal("space did not resume")
	}
	m = m.WithTick(time.Now())
	if after, _ := series.Last(); after == before {
		t.Error("resuming did not collect a new sample")
	}
}

func TestResetClearsHistory(t *testing.T) {
	m := testModel(t, 120, 34)
	m = m.WithKey("r")
	for _, s := range m.sources {
		if n := s.Series()[0].Len(); n != 0 {
			t.Errorf("%s still has %d samples after reset", s.ID(), n)
		}
	}
	// A cleared chart still renders.
	lines := m.Frame(120, 34)
	if len(lines) != 34 {
		t.Errorf("frame after reset has %d lines", len(lines))
	}
}

func TestHelpAndStatsOverlays(t *testing.T) {
	m := testModel(t, 120, 34)
	m = m.WithKey("?")
	if !m.showHelp {
		t.Fatal("? did not open the help")
	}
	plain := chart.StripANSI(strings.Join(m.Frame(120, 34), "\n"))
	for _, want := range []string{"NAVIGATION", "DISPLAY", "DATA", "switch between chart sources"} {
		if !strings.Contains(plain, want) {
			t.Errorf("help panel is missing %q", want)
		}
	}
	m = m.WithKey("esc")
	if m.showHelp {
		t.Error("esc did not close the help")
	}
	m = m.WithKey("s")
	if !m.showStats {
		t.Fatal("s did not open the statistics table")
	}
	plain = chart.StripANSI(strings.Join(m.Frame(120, 34), "\n"))
	if !strings.Contains(plain, "statistics") {
		t.Error("the statistics table is missing its title")
	}
	m = m.WithKey("s")
	if m.showStats {
		t.Error("s did not close the statistics table")
	}
}

func TestQuitKeys(t *testing.T) {
	m := testModel(t, 120, 34)
	if _, cmd := m.Update(keyMsg("q")); cmd == nil {
		t.Error("q should quit")
	}
	if _, cmd := m.Update(keyMsg("ctrl+c")); cmd == nil {
		t.Error("ctrl+c should quit")
	}
}

func TestMouseSelectsSource(t *testing.T) {
	m := testModel(t, 140, 38)
	// click the third sidebar card
	l := m.computeLayout(140, 38)
	if !l.showSidebar {
		t.Fatal("sidebar should be visible at this size")
	}
	msg := mouseMsg(l.sideX+3, l.sideY+1+l.cardH*2)
	next, _ := m.Update(msg)
	mm := next.(Model)
	if got := mm.ActiveSource().ID(); got == "cpu" {
		t.Errorf("clicking the third card should change the source, still %s", got)
	}
	// wheel zooms the window
	before := m.windowSamples
	next, _ = m.Update(wheelMsg(true))
	if got := next.(Model).windowSamples; got == before {
		t.Error("wheel up should zoom the window")
	}
}

func TestIntervalStepping(t *testing.T) {
	m := testModel(t, 120, 34)
	start := m.interval
	m = m.WithKey("o")
	if m.interval <= start {
		t.Errorf("o should slow sampling down (%v -> %v)", start, m.interval)
	}
	m = m.WithKey("i")
	if m.interval != start {
		t.Errorf("i should speed sampling back up (%v -> %v)", start, m.interval)
	}
	for i := 0; i < 20; i++ {
		m = m.WithKey("i")
	}
	if m.interval != intervalPresets[0] {
		t.Errorf("interval should clamp at the fastest preset, got %v", m.interval)
	}
}

func TestSidebarShowsEverySource(t *testing.T) {
	m := testModel(t, 140, 44)
	plain := chart.StripANSI(strings.Join(m.Frame(140, 44), "\n"))
	for _, s := range m.sources {
		if !strings.Contains(plain, s.Title()) {
			t.Errorf("sidebar is missing %s", s.ID())
		}
	}
}

func TestNarrowTerminalHidesSidebar(t *testing.T) {
	m := testModel(t, 70, 24)
	l := m.computeLayout(70, 24)
	if l.showSidebar {
		t.Error("the sidebar should be hidden on a narrow terminal")
	}
	lines := m.Frame(70, 24)
	if len(lines) != 24 {
		t.Fatalf("frame has %d lines", len(lines))
	}
}

func TestThemeRegistry(t *testing.T) {
	names := ThemeNames()
	if len(names) < 4 {
		t.Fatalf("expected several themes, got %v", names)
	}
	for _, n := range names {
		th := Themes[n]
		if th.Name != n {
			t.Errorf("theme %s reports name %s", n, th.Name)
		}
		if th.Bg == th.Fg {
			t.Errorf("theme %s has identical foreground and background", n)
		}
		if th.Faint == (chart.Color{}) || th.Accent == (chart.Color{}) {
			t.Errorf("theme %s is missing chrome colors", n)
		}
	}
	if _, ok := LookupTheme("NORD"); !ok {
		t.Error("theme lookup should be case insensitive")
	}
	if _, ok := LookupTheme("does-not-exist"); ok {
		t.Error("unknown theme should report false")
	}
}

// mouseMsg builds a left click at x,y.
func mouseMsg(x, y int) tea.MouseMsg {
	return tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y}
}

// wheelMsg builds a wheel event.
func wheelMsg(up bool) tea.MouseMsg {
	b := tea.MouseButtonWheelDown
	if up {
		b = tea.MouseButtonWheelUp
	}
	return tea.MouseMsg{Action: tea.MouseActionPress, Button: b, X: 10, Y: 10}
}

// TestHiddenExternalSeriesStayOutOfStats guards the dynamic external source:
// it preallocates columns so the series slice is stable, but only the columns
// that actually carry data may show up in legends and tables.
func TestHiddenExternalSeriesStayOutOfStats(t *testing.T) {
	src := metrics.NewExternalSource(metrics.ExternalOptions{
		Capacity: 20,
		Commands: []string{"echo 42"},
	})
	src.Sample()
	m := New(Config{
		Sources:  []metrics.Source{src},
		Interval: time.Second,
		Window:   10,
		Sidebar:  true,
		Mode:     chart.ColorNone,
		Theme:    Themes["midnight"],
		Version:  "test",
	}).WithSize(100, 30)
	m = m.WithKey("s")
	plain := chart.StripANSI(strings.Join(m.Frame(100, 30), "\n"))
	if !strings.Contains(plain, "statistics") {
		t.Fatalf("statistics panel is missing:\n%s", plain)
	}
	if strings.Contains(plain, "value 2") {
		t.Errorf("unused series leaked into the statistics table:\n%s", plain)
	}
}
