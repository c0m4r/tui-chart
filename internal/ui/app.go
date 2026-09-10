package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/c0m4r/tui-chart/internal/chart"
	"github.com/c0m4r/tui-chart/internal/metrics"
	tea "github.com/charmbracelet/bubbletea"
)

// Config configures the dashboard model.
type Config struct {
	Sources     []metrics.Source
	Interval    time.Duration
	Window      int
	Renderer    chart.Renderer
	RendererSet bool
	Grid        bool
	Average     bool
	Smooth      bool
	LineFill    bool
	Sidebar     bool
	Mode        chart.ColorMode
	Theme       Theme
	Host        metrics.HostInfo
	Version     string
}

// windowPresets are the selectable time windows in samples.
var windowPresets = []int{30, 60, 120, 300, 600, 900, 1800}

// intervalPresets are the selectable sampling intervals.
var intervalPresets = []time.Duration{
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
	time.Minute,
}

// tickMsg drives sampling.
type tickMsg time.Time

// Model is the bubbletea model of the dashboard.
type Model struct {
	sources []metrics.Source
	active  int

	width, height int
	interval      time.Duration
	windowSamples int

	renderer    chart.Renderer
	rendererSet bool
	grid        bool
	average     bool
	smooth      bool
	lineFill    bool
	showSidebar bool
	showHelp    bool
	showStats   bool
	mode        chart.ColorMode
	theme       Theme
	version     string
	host        metrics.HostInfo

	paused    bool
	clock     time.Time
	scaleHint *chart.Scale

	status   string
	statusAt time.Time

	hoverTab int
}

// New builds the dashboard model.
func New(cfg Config) Model {
	m := Model{
		sources:       cfg.Sources,
		interval:      cfg.Interval,
		windowSamples: cfg.Window,
		renderer:      cfg.Renderer,
		rendererSet:   cfg.RendererSet,
		grid:          cfg.Grid,
		average:       cfg.Average,
		smooth:        cfg.Smooth,
		lineFill:      cfg.LineFill,
		showSidebar:   cfg.Sidebar,
		mode:          cfg.Mode,
		theme:         cfg.Theme,
		version:       cfg.Version,
		host:          cfg.Host,
		clock:         time.Now(),
		hoverTab:      -1,
		width:         80,
		height:        24,
	}
	if m.interval <= 0 {
		m.interval = time.Second
	}
	if m.windowSamples < 1 {
		m.windowSamples = 60
	}
	m.clampWindow()
	return m
}

// clampWindow keeps the window inside the history capacity of the sources.
func (m *Model) clampWindow() {
	if len(m.sources) == 0 {
		return
	}
	cap := m.sources[0].Capacity()
	if m.windowSamples > cap {
		m.windowSamples = cap
	}
	if m.windowSamples < 2 {
		m.windowSamples = 2
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(tickCmd(m.interval), tea.HideCursor)
}

func tickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// ActiveSource returns the currently displayed source.
func (m Model) ActiveSource() metrics.Source {
	if len(m.sources) == 0 {
		return nil
	}
	return m.sources[m.active]
}

// Frame renders the dashboard to a slice of styled lines without touching the
// terminal. It is used by tests, snapshots and the --once mode.
func (m *Model) Frame(w, h int) []string {
	m.width, m.height = w, h
	return m.draw().Lines(m.mode)
}

// FrameCanvas renders the dashboard and returns the raw cell canvas.
func (m *Model) FrameCanvas(w, h int) *chart.Canvas {
	m.width, m.height = w, h
	return m.draw()
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		m.clock = time.Time(msg)
		if !m.paused {
			m.sample()
		}
		return m, tickCmd(m.interval)

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// sample reads every source once. Sampling all sources keeps the sidebar
// sparklines and the tab values live.
func (m *Model) sample() {
	for _, s := range m.sources {
		s.Sample()
	}
}

func (m *Model) setStatus(format string, args ...any) {
	m.status = fmt.Sprintf(format, args...)
	m.statusAt = time.Now()
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.showHelp {
		switch key {
		case "?", "esc", "q", "enter":
			m.showHelp = false
			return m, nil
		}
		m.showHelp = false
		return m, nil
	}
	if m.showStats {
		switch key {
		case "s", "esc", "q", "enter":
			m.showStats = false
			return m, nil
		}
		m.showStats = false
		return m, nil
	}
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?", "f1":
		m.showHelp = true
	case "left", "up", "shift+tab":
		m.cycleSource(-1)
	case "right", "down", "tab":
		m.cycleSource(1)
	case "home":
		m.active = 0
	case "end":
		m.active = len(m.sources) - 1
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		idx := int(key[0] - '1')
		if idx < len(m.sources) {
			m.active = idx
			m.scaleHint = nil
		}
	case "-", "_", "[", ",":
		m.zoomWindow(-1)
	case "+", "=", "]", ".":
		m.zoomWindow(1)
	case "d":
		if s := m.ActiveSource(); s != nil && s.HasDetail() {
			if s.Mode() == metrics.ModeCombined {
				s.SetMode(metrics.ModeDetailed)
				m.setStatus("%s: breakdown view", s.Title())
			} else {
				s.SetMode(metrics.ModeCombined)
				m.setStatus("%s: summary view", s.Title())
			}
			m.scaleHint = nil
		} else {
			m.setStatus("no breakdown available")
		}
	case "v":
		m.rendererSet = true
		m.renderer = (m.renderer + 1) % 3
		m.setStatus("view: %s", m.renderer)
	case "g":
		m.grid = !m.grid
	case "a":
		m.average = !m.average
	case "m":
		m.smooth = !m.smooth
		m.setStatus("smoothing %s", onOff(m.smooth))
	case "f":
		m.lineFill = !m.lineFill
		m.setStatus("line fill %s", onOff(m.lineFill))
	case "b":
		m.showSidebar = !m.showSidebar
	case "s":
		m.showStats = true
	case "t":
		m.cycleTheme()
	case " ":
		m.paused = !m.paused
		if m.paused {
			m.setStatus("sampling paused")
		} else {
			m.setStatus("sampling resumed")
		}
	case "r":
		for _, s := range m.sources {
			s.Reset()
		}
		m.scaleHint = nil
		m.setStatus("history cleared")
	case "i":
		m.stepInterval(-1)
	case "o":
		m.stepInterval(1)
	}
	return m, nil
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func (m *Model) cycleSource(delta int) {
	if len(m.sources) == 0 {
		return
	}
	m.active = (m.active + delta + len(m.sources)) % len(m.sources)
	m.scaleHint = nil
}

func (m *Model) zoomWindow(delta int) {
	idx := 0
	for i, v := range windowPresets {
		if v == m.windowSamples {
			idx = i
			break
		}
		if v < m.windowSamples {
			idx = i
		}
	}
	idx += delta
	if idx < 0 {
		idx = 0
	}
	if idx >= len(windowPresets) {
		idx = len(windowPresets) - 1
	}
	cap := 0
	if len(m.sources) > 0 {
		cap = m.sources[0].Capacity()
	}
	w := windowPresets[idx]
	if cap > 0 && w > cap {
		w = cap
	}
	if w == m.windowSamples {
		if delta > 0 && cap > 0 && m.windowSamples < cap {
			m.windowSamples = cap
		} else if delta < 0 && m.windowSamples > 2 {
			m.windowSamples = maxInt(2, m.windowSamples/2)
		}
	} else {
		m.windowSamples = w
	}
	m.clampWindow()
	m.scaleHint = nil
	m.setStatus("window %s", formatWindow(m.windowSamples, m.interval))
}

func (m *Model) stepInterval(delta int) {
	idx := len(intervalPresets) - 1
	for i, v := range intervalPresets {
		if v >= m.interval {
			idx = i
			break
		}
	}
	idx += delta
	if idx < 0 {
		idx = 0
	}
	if idx >= len(intervalPresets) {
		idx = len(intervalPresets) - 1
	}
	m.interval = intervalPresets[idx]
	m.setStatus("interval %s", formatDurationShort(m.interval))
}

func formatDurationShort(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%gs", d.Seconds())
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

func (m *Model) cycleTheme() {
	names := ThemeNames()
	idx := 0
	for i, n := range names {
		if n == m.theme.Name {
			idx = i
			break
		}
	}
	next := names[(idx+1)%len(names)]
	m.theme = Themes[next]
	m.setStatus("theme: %s", next)
}

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	l := m.computeLayout(m.width, m.height)
	if l.tooSmall {
		// Nothing is drawn to aim at; keep the notice steady.
		return m, nil
	}
	if msg.Action == tea.MouseActionMotion {
		// Highlight the tab under the pointer so it is obvious that the
		// selector responds to clicks.
		hover := -1
		if msg.Y == l.tabsY {
			if idx, ok := m.tabAt(msg.X); ok {
				hover = idx
			}
		}
		m.hoverTab = hover
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.zoomWindow(1)
		return m, nil
	case tea.MouseButtonWheelDown:
		m.zoomWindow(-1)
		return m, nil
	}
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return m, nil
	}
	// sidebar cards select a source
	if l.showSidebar && msg.X >= l.sideX && msg.X < l.sideX+l.sideW {
		rel := msg.Y - l.sideY - 1
		if rel >= 0 && l.cardH > 0 {
			slot := rel / l.cardH
			visible := (l.sideH - 2) / l.cardH
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
			if idx := startIdx + slot; idx >= 0 && idx < len(m.sources) {
				m.active = idx
				m.scaleHint = nil
			}
			return m, nil
		}
	}
	// tabs select a source
	if msg.Y == l.tabsY {
		if idx, ok := m.tabAt(msg.X); ok {
			m.active = idx
			m.scaleHint = nil
		}
	}
	return m, nil
}

// tabAt maps a column on the tab row to a source index.
func (m Model) tabAt(x int) (int, bool) {
	cx := 1
	for i, s := range m.sources {
		label := " " + s.Title() + " "
		wid := len([]rune(label))
		if x >= cx && x < cx+wid {
			return i, true
		}
		cx += wid + 1
	}
	return 0, false
}

// View implements tea.Model.
func (m Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	c := m.draw()
	full := c.Render(m.mode)
	return strings.ReplaceAll(full, "\r\n", "\n")
}

// StatusText exposes the footer status for tests.
func (m Model) StatusText() string { return m.statusText() }

// SetActive selects a source by index (used by the command line).
func (m *Model) SetActive(idx int) {
	if idx >= 0 && idx < len(m.sources) {
		m.active = idx
		m.scaleHint = nil
	}
}

// keyMsg maps a key name such as "right" or "d" to a bubbletea key message.
// It lets tests and the frame dumper drive the model without a terminal.
func keyMsg(name string) tea.KeyMsg {
	switch name {
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)}
	}
}

// WithSize sets the viewport size.
func (m Model) WithSize(w, h int) Model {
	m.width, m.height = w, h
	return m
}

// WithKey applies one key press and returns the updated model.
func (m Model) WithKey(name string) Model {
	next, _ := m.Update(keyMsg(name))
	if mm, ok := next.(Model); ok {
		return mm
	}
	return m
}

// WithKeys applies a sequence of key presses.
func (m Model) WithKeys(names ...string) Model {
	for _, n := range names {
		m = m.WithKey(n)
	}
	return m
}

// WithTick advances the clock and samples once.
func (m Model) WithTick(at time.Time) Model {
	m.clock = at
	if !m.paused {
		m.sample()
	}
	return m
}

// SeedHistory samples the sources n times with a synthetic one second cadence
// so a frame rendered immediately still shows a populated chart.
func (m Model) SeedHistory(n int) Model {
	for i := n; i > 0; i-- {
		m = m.WithTick(time.Now().Add(-time.Duration(i) * m.interval))
	}
	return m
}
