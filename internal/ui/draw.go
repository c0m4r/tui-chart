package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/c0m4r/tui-chart/internal/chart"
	"github.com/c0m4r/tui-chart/internal/metrics"
	"github.com/mattn/go-runewidth"
)

// layout holds the resolved geometry of one frame.
type layout struct {
	w, h int

	headerY int
	tabsY   int
	rule1Y  int
	rule2Y  int

	mainY, mainH int
	footerY      int

	chartX, chartY, chartW, chartH int
	sideX, sideY, sideW, sideH     int
	showSidebar                    bool
	tooSmall                       bool
	cardH                          int
}

// MinWidth and MinHeight are the smallest terminal that can hold the
// dashboard: a framed panel with a y axis, a time axis and a statistics
// strip. Below either of them the chart would be clipped into nonsense, so
// the dashboard prints a notice instead.
const (
	MinWidth  = 60
	MinHeight = 16
)

const sidebarWidth = 30

func (m *Model) computeLayout(w, h int) layout {
	l := layout{w: w, h: h}
	l.tooSmall = w < MinWidth || h < MinHeight
	l.headerY = 0
	l.tabsY = 1
	l.rule1Y = 2
	l.footerY = h - 1
	l.rule2Y = h - 2
	l.mainY = 3
	l.mainH = l.rule2Y - l.mainY
	if l.mainH < 1 {
		l.mainH = 1
	}
	l.showSidebar = !l.tooSmall && m.showSidebar && w >= 96 && l.mainH >= 8 && len(m.sources) > 1
	if l.showSidebar {
		l.sideW = sidebarWidth
		if w < 120 {
			l.sideW = 26
		}
		l.sideX = w - l.sideW
		l.sideY = l.mainY
		l.sideH = l.mainH
		l.chartX = 0
		l.chartY = l.mainY
		l.chartW = l.sideX - 1
		l.chartH = l.mainH
	} else {
		l.chartX = 0
		l.chartY = l.mainY
		l.chartW = w
		l.chartH = l.mainH
	}
	if l.tooSmall {
		l.cardH = 0
	} else if n := len(m.sources); n > 0 {
		avail := l.sideH - 2 // interior of the sidebar frame
		l.cardH = clampInt(avail/n, 2, 6)
	}
	return l
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

// draw renders the whole dashboard onto a fresh canvas.
func (m *Model) draw() *chart.Canvas {
	l := m.computeLayout(m.width, m.height)
	c := chart.NewCanvas(l.w, l.h)
	c.Clear(m.st(m.theme.Fg, m.theme.Bg))
	if l.tooSmall {
		m.drawTooSmall(c, l)
		return c
	}
	m.drawHeader(c, l)
	m.drawTabs(c, l)
	m.hrule(c, l.rule1Y, 0, l.w)
	m.hrule(c, l.rule2Y, 0, l.w)
	m.drawChartPanel(c, l)
	if l.showSidebar {
		m.drawSidebar(c, l)
	}
	m.drawFooter(c, l)
	if m.showHelp {
		m.drawHelp(c, l)
	} else if m.showStats {
		m.drawStatsPanel(c, l)
	}
	return c
}

// drawTooSmall explains why there is no chart. The terminal is below
// MinWidth x MinHeight, where a chart would be clipped into nonsense, so the
// dashboard shows a notice instead. The chrome stays when there is room for
// it, which keeps the clock, the pause marker and the quit hint visible.
func (m *Model) drawTooSmall(c *chart.Canvas, l layout) {
	top, bottom := 0, l.h
	if l.h >= 4 {
		m.drawHeader(c, l)
		m.drawFooter(c, l)
		top, bottom = 1, l.h-1
	}
	bg := m.theme.Bg
	// Ordered by importance: the shortest terminals keep the first lines.
	lines := []struct {
		text string
		st   chart.Style
	}{
		{"terminal too small", m.st(m.theme.Warn, bg).WithAttr(chart.AttrBold)},
		{fmt.Sprintf("need at least %d×%d (this one is %d×%d)", MinWidth, MinHeight, l.w, l.h), m.st(m.theme.Muted, bg)},
		{"resize the window to see the chart", m.st(m.theme.Muted, bg)},
	}
	if n := bottom - top; n < len(lines) {
		lines = lines[:maxInt(0, n)]
	}
	y := top + (bottom-top-len(lines))/2
	for _, ln := range lines {
		c.TextCenter(0, y, l.w, ln.text, ln.st)
		y++
	}
}

func (m *Model) hrule(c *chart.Canvas, y, x0, w int) {
	if y < 0 || y >= c.H() {
		return
	}
	c.HLine(x0, x0+w-1, y, '─', m.st(m.theme.Border, m.theme.Bg))
}

// st builds a style from theme colors.
func (m *Model) st(fg, bg chart.Color) chart.Style {
	return chart.Style{FG: fg, BG: bg}
}

// drawHeader renders the title bar: application identity on the left, host
// facts in the middle and the clock on the right. On narrow terminals the
// clock wins and the rest is truncated.
func (m *Model) drawHeader(c *chart.Canvas, l layout) {
	bg := m.theme.HeaderBg
	c.FillStyle(0, l.headerY, l.w, 1, m.st(m.theme.Fg, bg))
	if l.w < 4 {
		return
	}
	right := m.clock.Format("15:04:05")
	if m.paused {
		right = "PAUSED  " + right
	}
	rightW := runewidth.StringWidth(right)
	if rightW+2 > l.w {
		right = runewidth.Truncate(right, maxInt(1, l.w-2), "")
		rightW = runewidth.StringWidth(right)
	}
	fg := m.theme.Fg
	if m.paused {
		fg = m.theme.Warn
	}
	c.TextRight(l.w-2, l.headerY, right, m.st(fg, bg).WithAttr(chart.AttrBold))

	limit := l.w - rightW - 3 // columns available to the left of the clock
	x := 1
	title := "▁▂▃ tui-chart"
	if limit > runewidth.StringWidth(title) {
		x = c.Text(x, l.headerY, title, m.st(m.theme.Accent, bg).WithAttr(chart.AttrBold))
		ver := "v" + m.version
		if limit > x+1+runewidth.StringWidth(ver) {
			x = c.Text(x+1, l.headerY, ver, m.st(m.theme.Muted, bg))
		} else {
			x--
		}
	}
	facts := m.hostFacts()
	avail := limit - x
	if len(facts) == 0 || avail < 12 {
		return
	}
	txt := strings.Join(facts, "  ·  ")
	if runewidth.StringWidth(txt) > avail {
		txt = runewidth.Truncate(txt, avail, "…")
	}
	c.Text(x+1+(avail-runewidth.StringWidth(txt))/2, l.headerY, txt, m.st(m.theme.Muted, bg))
}

func (m *Model) hostFacts() []string {
	var facts []string
	if m.host.Hostname != "" {
		facts = append(facts, m.host.Hostname)
	}
	if m.host.Cores > 0 {
		facts = append(facts, fmt.Sprintf("%d cores", m.host.Cores))
	}
	if m.host.MemTotal > 0 {
		facts = append(facts, chart.UnitBytes.Format(m.host.MemTotal)+" RAM")
	}
	if m.host.Uptime > 0 {
		facts = append(facts, "up "+formatUptime(m.host.Uptime))
	}
	if m.host.Kernel != "" {
		facts = append(facts, m.host.Kernel)
	}
	return facts
}

func formatUptime(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// drawTabs renders the source selector, leaving the right edge for the
// window and interval readout. The tab under the pointer is highlighted.
func (m *Model) drawTabs(c *chart.Canvas, l layout) {
	bg := m.theme.Bg
	c.FillStyle(0, l.tabsY, l.w, 1, m.st(m.theme.Fg, bg))
	info := m.windowLabel(false)
	infoW := runewidth.StringWidth(info)
	limit := l.w - 2
	if infoW+10 <= l.w {
		limit = l.w - infoW - 3
	} else {
		info = m.windowLabel(true)
		infoW = runewidth.StringWidth(info)
		if infoW+10 <= l.w {
			limit = l.w - infoW - 3
		} else {
			info = ""
			infoW = 0
		}
	}
	x := 1
	shown := 0
	for i, s := range m.sources {
		label := " " + s.Title() + " "
		if s.HasDetail() && i == m.active && s.Mode() == metrics.ModeDetailed {
			label = " " + s.Title() + " ▸ " + s.ModeLabel() + " "
		}
		wid := runewidth.StringWidth(label)
		if x+wid > limit {
			break
		}
		fg, tbg := m.theme.TabInactiveFg, bg
		attr := chart.Attr(0)
		if i == m.active {
			fg, tbg = m.theme.TabActiveFg, m.theme.TabActiveBg
			attr = chart.AttrBold
		} else if m.hoverTab == i {
			tbg = m.theme.TabHoverBg
		}
		st := m.st(fg, tbg).WithAttr(attr)
		c.Text(x, l.tabsY, label, st)
		x += wid
		c.Set(x, l.tabsY, ' ', m.st(fg, bg))
		x++
		shown++
	}
	if shown < len(m.sources) && x+2 <= limit {
		c.Text(x, l.tabsY, "›", m.st(m.theme.Muted, bg))
	}
	if info != "" {
		c.TextRight(l.w-2, l.tabsY, info, m.st(m.theme.Muted, bg))
	}
}

// windowLabel describes the active time window, in a compact form when the
// terminal is narrow.
func (m *Model) windowLabel(compact bool) string {
	w := formatWindow(m.windowSamples, m.interval)
	iv := formatDurationShort(m.interval)
	if compact {
		return w + " · " + iv
	}
	return w + " window · " + iv + " interval"
}

func formatWindow(samples int, interval time.Duration) string {
	d := time.Duration(samples) * interval
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// panelTitle describes the active source for the panel border.
func (m *Model) panelTitle() (string, string) {
	if len(m.sources) == 0 {
		return "no sources", ""
	}
	s := m.sources[m.active]
	left := s.Title()
	if s.HasDetail() {
		left += " · " + s.ModeLabel()
	}
	if _, _, fixed := s.Range(); !fixed {
		left += " (auto)"
	}
	return left, s.Unit().Suffix()
}

// drawChartPanel renders the framed chart with its legend and statistics.
func (m *Model) drawChartPanel(c *chart.Canvas, l layout) {
	if len(m.sources) == 0 || l.chartH < 3 || l.chartW < 12 {
		return
	}
	src := m.sources[m.active]
	title, unit := m.panelTitle()
	if unit != "" {
		title += "  [" + unit + "]"
	}
	m.frame(c, l.chartX, l.chartY, l.chartW, l.chartH, title, m.legendRight(src))
	innerX, innerY := l.chartX+1, l.chartY+1
	innerW, innerH := l.chartW-2, l.chartH-2
	if innerW < 8 || innerH < 3 {
		return
	}
	statsH := 0
	if innerH >= 7 {
		statsH = 1
	}
	chartH := innerH - statsH
	series := src.Series()
	renderer := m.renderer
	if !m.rendererSet {
		renderer = src.Preferred()
	}
	lo, hi, fixed := src.Range()
	opt := chart.Options{
		Renderer: renderer,
		Mode:     m.mode,
		Palette:  m.theme.ChartPalette(),
		Unit:     src.Unit(),
		Window:   m.windowSamples,
		Interval: m.interval,
		Now:      m.clock,
		Stacked:  src.Stacked(),
		Smooth:   m.smooth,
		LineFill: m.lineFill && renderer == chart.RendererLine && len(series) == 1,
		Grid:     m.grid,
		Axis:     true,
		TimeAxis: chartH >= 4,
		HasYMin:  fixed,
		HasYMax:  fixed,
		YMin:     lo,
		YMax:     hi,
		Baseline: lo,
		Cursor:   true,
		Average:  m.average,
		Hint:     m.scaleHint,
	}
	if !fixed {
		opt.YMin, opt.YMax, opt.HasYMin, opt.HasYMax = 0, 0, false, false
		if b, ok := src.(interface{ Baseline() float64 }); ok {
			opt.Baseline = b.Baseline()
			opt.BaselineZero = false
		} else {
			opt.Baseline = 0
			opt.BaselineZero = true
		}
	}
	geom := chart.Draw(c, innerX, innerY, innerW, chartH, series, opt)
	m.scaleHint = &geom.Scale
	if statsH > 0 {
		m.drawStatsStrip(c, innerX, innerY+chartH, innerW, series, src.Unit())
	}
	if len(series) == 0 || series[0].Len() < 2 {
		// A single point cannot describe a trend yet; say so instead of
		// showing an empty grid.
		msg := "collecting…"
		c.TextCenter(innerX, innerY+chartH/2, innerW, msg, m.st(m.theme.Muted, m.theme.PanelBg).WithAttr(chart.AttrDim))
	}
}

// legendRight builds the legend shown inside the panel's top border.
func (m *Model) legendRight(src metrics.Source) []legendEntry {
	items := chart.LegendItems(src.Series(), src.Unit(), m.mode)
	out := make([]legendEntry, 0, len(items))
	for _, it := range items {
		out = append(out, legendEntry{Text: " " + it.Name + " " + it.Value + " ", Color: it.Color})
	}
	return out
}

type legendEntry struct {
	Text  string
	Color chart.Color
}

// frame draws a rounded border with an inline title and optional right hand
// legend entries.
func (m *Model) frame(c *chart.Canvas, x, y, w, h int, title string, right []legendEntry) {
	if w < 4 || h < 2 {
		return
	}
	bg := m.theme.Bg
	border := m.theme.BorderHi
	c.HLine(x+1, x+w-2, y, '─', m.st(border, bg))
	c.HLine(x+1, x+w-2, y+h-1, '─', m.st(border, bg))
	c.VLine(x, y+1, y+h-2, '│', m.st(border, bg))
	c.VLine(x+w-1, y+1, y+h-2, '│', m.st(border, bg))
	c.Set(x, y, '╭', m.st(border, bg))
	c.Set(x+w-1, y, '╮', m.st(border, bg))
	c.Set(x, y+h-1, '╰', m.st(border, bg))
	c.Set(x+w-1, y+h-1, '╯', m.st(border, bg))
	// interior background: blank whatever was underneath
	c.ClearRect(x+1, y+1, w-2, h-2, m.st(m.theme.Fg, m.theme.PanelBg))
	c.Set(x, y, '╭', m.st(border, bg))
	if title != "" {
		label := " " + title + " "
		c.Text(x+2, y, label, m.st(m.theme.Fg, bg).WithAttr(chart.AttrBold))
	}
	if len(right) > 0 {
		total := 0
		for _, e := range right {
			total += runewidth.StringWidth(e.Text)
		}
		start := x + w - 2 - total
		if start > x+4+runewidth.StringWidth(title) {
			cx := start
			for _, e := range right {
				c.Text(cx, y, e.Text, m.st(e.Color, bg))
				cx += runewidth.StringWidth(e.Text)
			}
		}
	}
}

// drawStatsStrip renders current, average, min, max and p95 for the primary
// series underneath the chart.
func (m *Model) drawStatsStrip(c *chart.Canvas, x, y, w int, series []*chart.Series, unit chart.Unit) {
	if len(series) == 0 || y < 0 || y >= c.H() {
		return
	}
	bg := m.theme.PanelBg
	c.FillStyle(x, y, w, 1, m.st(m.theme.Fg, bg))
	s := series[0]
	st := chart.ComputeStats(s.Data, m.windowSamples)
	items := []struct {
		key string
		val float64
	}{
		{"cur", st.Last},
		{"avg", st.Avg},
		{"min", st.Min},
		{"max", st.Max},
		{"p95", st.P95},
	}
	cx := x + 1
	for _, it := range items {
		if cx+10 > x+w {
			break
		}
		c.Text(cx, y, it.key, m.st(m.theme.Muted, bg))
		cx += runewidth.StringWidth(it.key) + 1
		val := unit.Format(it.val)
		fg := m.theme.Fg
		switch it.key {
		case "max":
			fg = m.theme.Warn
		case "cur":
			fg = m.theme.Accent
		}
		c.Text(cx, y, val, m.st(fg, bg).WithAttr(chart.AttrBold))
		cx += runewidth.StringWidth(val) + 3
	}
	// right aligned: samples collected and the series count
	if n := s.Len(); n > 0 {
		txt := fmt.Sprintf("%d samples · %d/%d pts", n, minInt(n, m.windowSamples), m.windowSamples)
		if cx+runewidth.StringWidth(txt)+2 < x+w {
			c.TextRight(x+w-1, y, txt, m.st(m.theme.Muted, bg))
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// drawSidebar renders the source navigator with live sparklines.
func (m *Model) drawSidebar(c *chart.Canvas, l layout) {
	x, y, w, h := l.sideX, l.sideY, l.sideW, l.sideH
	m.frame(c, x, y, w, h, "sources", nil)
	innerX, innerY, innerW, innerH := x+1, y+1, w-2, h-2
	if innerW < 6 || innerH < 2 {
		return
	}
	cardH := l.cardH
	if cardH < 2 {
		cardH = 2
	}
	visible := innerH / cardH
	if visible < 1 {
		visible = 1
	}
	// keep the active card in view
	startIdx := 0
	if m.active >= visible {
		startIdx = m.active - visible + 1
	}
	if startIdx > len(m.sources)-visible {
		startIdx = len(m.sources) - visible
	}
	if startIdx < 0 {
		startIdx = 0
	}
	for i := startIdx; i < len(m.sources) && i-startIdx < visible; i++ {
		cy := innerY + (i-startIdx)*cardH
		m.drawSourceCard(c, innerX, cy, innerW, cardH, i, i == m.active)
	}
	if len(m.sources) > visible {
		txt := fmt.Sprintf("%d/%d", m.active+1, len(m.sources))
		c.TextRight(x+w-2, y, " "+txt+" ", m.st(m.theme.Muted, m.theme.Bg))
	}
}

// drawSourceCard renders one sidebar entry.
func (m *Model) drawSourceCard(c *chart.Canvas, x, y, w, h, idx int, active bool) {
	src := m.sources[idx]
	bg := m.theme.PanelBg
	if active {
		bg = m.theme.SelectBg
	}
	c.FillStyle(x, y, w, h, m.st(m.theme.Fg, bg))
	if active {
		// left selection bar plus a bright title
		c.VLine(x, y, y+h-1, '▌', m.st(m.theme.Accent, bg))
	}
	series := src.Series()
	summary := series
	if a, ok := src.(interface{ AllSeries() [][]*chart.Series }); ok {
		if sets := a.AllSeries(); len(sets) > 0 && len(sets[0]) > 0 && src.Mode() == metrics.ModeDetailed {
			summary = sets[0]
		}
	}
	titleFg := m.theme.Muted
	if active {
		titleFg = m.theme.Fg
	}
	tx := x + 1
	c.Text(tx, y, src.Title(), m.st(titleFg, bg).WithAttr(chart.AttrBold))
	val := "--"
	valFg := m.theme.Fg
	if len(summary) > 0 {
		if v, ok := summary[0].Last(); ok {
			val = src.Unit().Format(v)
			if _, _, fixed := src.Range(); fixed {
				if lo, hi, _ := src.Range(); hi > lo {
					frac := (v - lo) / (hi - lo)
					switch {
					case frac > 0.9:
						valFg = m.theme.Bad
					case frac > 0.75:
						valFg = m.theme.Warn
					default:
						valFg = m.theme.Good
					}
				}
			}
		}
	}
	if runewidth.StringWidth(val)+runewidth.StringWidth(src.Title())+3 > w {
		val = runewidth.Truncate(val, maxInt(1, w-runewidth.StringWidth(src.Title())-3), "")
	}
	c.TextRight(x+w-2, y, val, m.st(valFg, bg).WithAttr(chart.AttrBold))
	if h >= 2 && len(summary) > 0 {
		sparkH := h - 1
		if sparkH > 2 {
			sparkH = 2
		}
		chart.DrawSpark(c, x+1, y+1, w-2, sparkH, summary[0], chart.Options{
			Renderer: chart.RendererArea,
			Mode:     m.mode,
			Palette:  chart.Palette{BG: bg, FG: m.theme.Fg, Muted: m.theme.Muted, Grid: m.theme.Faint},
			Unit:     src.Unit(),
			Window:   m.windowSamples,
			Interval: m.interval,
			Baseline: 0,
		})
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// drawFooter renders the key hints and the status line.
func (m *Model) drawFooter(c *chart.Canvas, l layout) {
	bg := m.theme.FooterBg
	c.FillStyle(0, l.footerY, l.w, 1, m.st(m.theme.Fg, bg))
	// Ordered by importance: when the terminal is narrow the tail is dropped
	// first, so help and quit always stay visible.
	hints := []struct{ key, label string }{
		{"◀▶", "source"},
		{"-+", "window"},
		{"?", "help"},
		{"q", "quit"},
		{"d", "breakdown"},
		{"v", "view"},
		{"space", "pause"},
		{"a", "avg"},
		{"g", "grid"},
		{"r", "reset"},
	}
	status := m.statusText()
	limit := l.w - 2
	if w := runewidth.StringWidth(status); l.w-w > 30 {
		limit = l.w - w - 3
	} else {
		// Not enough room for both: keep the hints, drop the status.
		status = ""
	}
	x := 1
	for _, hnt := range hints {
		need := runewidth.StringWidth(hnt.key) + runewidth.StringWidth(hnt.label) + 4
		if x+need > limit {
			break
		}
		x = c.Text(x, l.footerY, hnt.key, m.st(m.theme.Accent, bg).WithAttr(chart.AttrBold))
		x = c.Text(x+1, l.footerY, hnt.label, m.st(m.theme.Muted, bg))
		x += 2
	}
	if status != "" {
		c.TextRight(l.w-2, l.footerY, status, m.st(m.theme.Fg, bg))
	}
}

func (m *Model) statusText() string {
	if m.status != "" && time.Since(m.statusAt) < 4*time.Second {
		return m.status
	}
	parts := []string{}
	if m.paused {
		parts = append(parts, "PAUSED")
	}
	if len(m.sources) > 0 {
		src := m.sources[m.active]
		for _, kv := range src.Info() {
			parts = append(parts, kv.Key+" "+kv.Value)
		}
	}
	parts = append(parts, m.theme.Name, m.rendererLabel())
	if m.mode != chart.ColorTrue {
		parts = append(parts, m.mode.String()+" color")
	}
	return strings.Join(parts, "  ·  ")
}

func (m *Model) rendererLabel() string {
	if len(m.sources) > 0 && !m.rendererSet {
		return "auto"
	}
	return m.renderer.String()
}

// drawStatsPanel overlays a table of per-series statistics.
func (m *Model) drawStatsPanel(c *chart.Canvas, l layout) {
	if len(m.sources) == 0 {
		return
	}
	src := m.sources[m.active]
	series := src.Series()
	rows := len(series) + 3
	w := 52
	if w > l.w-4 {
		w = l.w - 4
	}
	h := rows + 2
	if h > l.mainH {
		h = l.mainH
	}
	x := (l.w - w) / 2
	y := l.mainY + (l.mainH-h)/2
	dimAll(c, x, y, w, h)
	m.frame(c, x, y, w, h, src.Title()+" statistics", nil)
	bg := m.theme.PanelBg
	head := fmt.Sprintf(" %-10s %10s %10s %10s %10s", "series", "cur", "avg", "min", "max")
	c.Text(x+1, y+2, head, m.st(m.theme.Muted, bg).WithAttr(chart.AttrBold))
	for i, s := range series {
		st := chart.ComputeStats(s.Data, m.windowSamples)
		row := y + 3 + i
		if row >= y+h-1 {
			break
		}
		if i%2 == 1 {
			c.ClearRect(x+1, row, w-2, 1, m.st(m.theme.Fg, m.theme.Stripe))
		}
		c.Text(x+1, row, " "+shortenName(s.Name, 10), m.st(s.Ramp.At(0.85), bg))
		cols := []string{
			src.Unit().Format(st.Last),
			src.Unit().Format(st.Avg),
			src.Unit().Format(st.Min),
			src.Unit().Format(st.Max),
		}
		cx := x + 12
		for _, v := range cols {
			c.TextRight(cx+9, row, v, m.st(m.theme.Fg, bg))
			cx += 10
		}
	}
	foot := " any key to close "
	c.TextRight(x+w-2, y+h-1, foot, m.st(m.theme.Muted, m.theme.Bg))
}

// drawHelp overlays the keyboard reference.
func (m *Model) drawHelp(c *chart.Canvas, l layout) {
	type row struct{ key, text string }
	sections := []struct {
		title string
		rows  []row
	}{
		{"navigation", []row{
			{"← →  ↑ ↓", "switch between chart sources"},
			{"1 … 9", "jump straight to a source"},
			{"tab / ⇧tab", "next / previous source"},
			{"- +", "zoom the time window out / in"},
			{"click", "select a source in the sidebar"},
			{"wheel", "zoom the time window"},
		}},
		{"display", []row{
			{"d", "toggle summary / breakdown"},
			{"v", "cycle area → line → bars"},
			{"g", "toggle grid lines"},
			{"a", "toggle the average marker"},
			{"m", "toggle curve smoothing"},
			{"f", "toggle the fill under line charts"},
			{"s", "toggle this statistics table"},
			{"b", "toggle the source sidebar"},
			{"t", "cycle the color theme"},
		}},
		{"data", []row{
			{"space", "pause / resume sampling"},
			{"r", "clear the collected history"},
			{"i / o", "slower / faster sampling interval"},
			{"q  ctrl+c", "quit"},
		}},
	}
	w := 62
	if w > l.w-2 {
		w = l.w - 2
	}
	h := 4
	for _, s := range sections {
		h += len(s.rows) + 2
	}
	if h > l.h-2 {
		h = l.h - 2
	}
	x := (l.w - w) / 2
	y := (l.h - h) / 2
	if y < 1 {
		y = 1
	}
	dimCanvas(c, x, y, w, h)
	m.frame(c, x, y, w, h, "keyboard & mouse", nil)
	bg := m.theme.PanelBg
	cy := y + 2
	for _, sec := range sections {
		if cy >= y+h-1 {
			break
		}
		c.Text(x+2, cy, strings.ToUpper(sec.title), m.st(m.theme.Accent, bg).WithAttr(chart.AttrBold))
		cy++
		for _, r := range sec.rows {
			if cy >= y+h-1 {
				break
			}
			c.Text(x+4, cy, r.key, m.st(m.theme.Fg, bg).WithAttr(chart.AttrBold))
			c.Text(x+18, cy, r.text, m.st(m.theme.Muted, bg))
			cy++
		}
		cy++
	}
	c.TextRight(x+w-2, y+h-1, " press ? or esc to close ", m.st(m.theme.Muted, m.theme.Bg))
}

// dimCanvas darkens everything outside the given rectangle to focus attention.
func dimCanvas(c *chart.Canvas, rx, ry, rw, rh int) {
	for y := 0; y < c.H(); y++ {
		for x := 0; x < c.W(); x++ {
			if x >= rx && x < rx+rw && y >= ry && y < ry+rh {
				continue
			}
			if cell := c.At(x, y); cell != nil {
				cell.FG = chart.Mul(cell.FG, 0.35)
				cell.BG = chart.Mul(cell.BG, 0.35)
			}
		}
	}
}

// dimAll darkens a rectangle (used behind modal panels).
func dimAll(c *chart.Canvas, rx, ry, rw, rh int) {
	for y := ry; y < ry+rh; y++ {
		for x := rx; x < rx+rw; x++ {
			if cell := c.At(x, y); cell != nil {
				cell.FG = chart.Mul(cell.FG, 0.35)
				cell.BG = chart.Mul(cell.BG, 0.35)
			}
		}
	}
}
