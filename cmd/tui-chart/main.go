// Command tui-chart draws high resolution live charts of system metrics in the
// terminal, with a keyboard driven dashboard for switching between sources.
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/c0m4r/tui-chart/internal/chart"
	"github.com/c0m4r/tui-chart/internal/metrics"
	"github.com/c0m4r/tui-chart/internal/ui"
	tea "github.com/charmbracelet/bubbletea"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "0.2.0"

var usage = `tui-chart — high resolution live charts for the terminal

usage:
  tui-chart [flags]            start the interactive dashboard
  tui-chart snapshot [flags]   print one chart to stdout and exit
  tui-chart sources            list the available metric sources
  tui-chart frame [flags]      render one dashboard frame to stdout

flags:
  -s, --source NAME      source to display first (default: cpu)
  -i, --interval DUR     sampling interval, e.g. 250ms, 1s, 5s (default: 1s)
  -w, --window N         samples shown on the chart (default: 60)
      --history N        samples kept in memory (default: 900)
  -r, --renderer NAME    area | line | bars (default: per source)
      --theme NAME       ` + themeList() + ` (default: midnight)
      --color MODE       truecolor | 256 | 16 | none (default: auto)
      --grid             draw horizontal grid lines
      --average          draw the mean marker
      --smooth           smooth curves between samples
      --fill             shade the area under line charts
      --no-sidebar       hide the source sidebar
      --demo             use synthetic data instead of live metrics
      --list             list sources and exit
  -h, --help             show this help
  -v, --version          show the version

external data:
      --stdin            chart samples read from stdin, one line per sample
  -c, --command CMD      run CMD every interval and chart its output
                         (repeat -c for several series)
      --label LIST       comma separated series names, e.g. rx,tx
      --unit UNIT        none | percent | bytes | bytes/s | count | rate |
                         celsius | watts | load | seconds
      --timeout DUR      command and stream read timeout (default: 30s)
      -- CMD             shorthand for --command CMD

A sample may contain several numbers; each becomes a separate series. Text
around the numbers is ignored, so "temp=52.3C" works as-is. When a pipe is
connected to stdin, the dashboard reads it automatically and takes the
keyboard from the controlling terminal.

the dashboard needs a terminal of at least 60x16 (columns x rows); smaller
windows print a resize notice instead of a chart that would be clipped.

frame flags:
      --keys LIST        comma separated keys to apply, e.g. right,right,d
      --ticks N          samples to collect before rendering (default: 90)
      --width N          frame width in columns (default: 120)
      --height N         frame height in rows (default: 34)

snapshot flags:
      --width N          canvas width in columns (default: 100)
      --height N         canvas height in rows (default: 22)
      --samples N        samples of history to draw (default: window)
      --no-color         disable colors (same as --color none)

examples:
  tui-chart                                live dashboard, cpu first
  tui-chart -s memory                      start on the memory chart
  tui-chart -i 500ms -w 120                twice a second, two minute window
  tui-chart --demo --theme nord            explore the ui with synthetic data
  tui-chart --command 'ps aux | wc -l'     chart a command's output
  tui-chart -c 'pgrep -c bash' -c 'pgrep -c sshd'
                                             two commands as two series
  printf '1\n2\n3\n' | tui-chart --stdin   chart data from a pipe
  seq 1 120 | tui-chart snapshot --stdin   render a stream as one chart
`

func themeList() string {
	return strings.Join(ui.ThemeNames(), " | ")
}

type options struct {
	source      string
	interval    time.Duration
	window      int
	history     int
	renderer    string
	rendererSet bool
	theme       string
	color       string
	grid        bool
	average     bool
	smooth      bool
	fill        bool
	sidebar     bool
	demo        bool
	width       int
	height      int
	samples     int
	help        bool
	showVersion bool
	list        bool
	keys        string
	ticks       int

	// external data sources
	stdin    bool
	commands []string
	labels   []string
	unit     chart.Unit
	timeout  time.Duration
	input    io.Reader
}

func main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}
	opt, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tui-chart: "+err.Error())
		fmt.Fprintln(os.Stderr, "try 'tui-chart --help'")
		os.Exit(2)
	}
	if opt.help {
		fmt.Print(usage)
		return
	}
	if opt.showVersion {
		fmt.Println("tui-chart " + version)
		return
	}
	// A dashboard started with a pipe on stdin reads that pipe as chart data.
	// The keyboard is then taken from the controlling terminal, so normal
	// navigation still works. This makes `producer | tui-chart` just work.
	if cmd == "run" && !opt.demo && !opt.stdin && len(opt.commands) == 0 && !isTerminal(os.Stdin) {
		opt.stdin = true
	}
	if err := validateOptions(opt); err != nil {
		fmt.Fprintln(os.Stderr, "tui-chart: "+err.Error())
		fmt.Fprintln(os.Stderr, "try 'tui-chart --help'")
		os.Exit(2)
	}
	if cmd == "run" && opt.stdin && opt.input == os.Stdin && isTerminal(os.Stdin) {
		fmt.Fprintln(os.Stderr, "tui-chart: --stdin needs a pipe or a file, not a terminal")
		fmt.Fprintln(os.Stderr, "try 'tui-chart --command CMD' to sample a command instead")
		os.Exit(2)
	}
	if cmd == "sources" || opt.list {
		listSources(opt)
		return
	}
	switch cmd {
	case "run":
		if err := runDashboard(opt); err != nil {
			fatal(err)
		}
	case "snapshot":
		if err := runSnapshot(opt); err != nil {
			fatal(err)
		}
	case "frame":
		if err := runFrame(opt); err != nil {
			fatal(err)
		}
	default:
		fmt.Fprintf(os.Stderr, "tui-chart: unknown command %q\n", cmd)
		os.Exit(2)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "tui-chart: "+err.Error())
	os.Exit(1)
}

// parseFlags implements a small flag parser so both -x and --long forms work
// with values given as "--flag=value" or "--flag value".
func parseFlags(args []string) (options, error) {
	opt := options{
		source:   "",
		interval: time.Second,
		window:   60,
		history:  900,
		sidebar:  true,
		width:    100,
		height:   22,
		ticks:    90,
		timeout:  30 * time.Second,
		input:    os.Stdin,
	}
	need := func(i *int, name string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("flag %s needs a value", name)
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value := a, ""
		hasValue := false
		if strings.HasPrefix(a, "--") {
			if eq := strings.IndexByte(a, '='); eq >= 0 {
				name, value, hasValue = a[:eq], a[eq+1:], true
			}
		}
		getValue := func() (string, error) {
			if hasValue {
				return value, nil
			}
			return need(&i, name)
		}
		var err error
		switch name {
		case "-h", "--help":
			opt.help = true
		case "-v", "--version":
			opt.showVersion = true
		case "-s", "--source":
			opt.source, err = getValue()
		case "-i", "--interval":
			var s string
			if s, err = getValue(); err == nil {
				opt.interval, err = parseDuration(s)
			}
		case "-w", "--window":
			var s string
			if s, err = getValue(); err == nil {
				opt.window, err = positiveInt(s, "window")
			}
		case "--history":
			var s string
			if s, err = getValue(); err == nil {
				opt.history, err = positiveInt(s, "history")
			}
		case "-r", "--renderer":
			var s string
			if s, err = getValue(); err == nil {
				var r chart.Renderer
				if r, opt.rendererSet = chart.ParseRenderer(strings.ToLower(s)); !opt.rendererSet {
					err = fmt.Errorf("unknown renderer %q", s)
				}
				opt.renderer = r.String()
			}
		case "--theme":
			opt.theme, err = getValue()
		case "--color", "--colour":
			opt.color, err = getValue()
		case "--grid":
			opt.grid = boolValue(hasValue, value, true)
		case "--average", "--avg":
			opt.average = boolValue(hasValue, value, true)
		case "--smooth":
			opt.smooth = boolValue(hasValue, value, true)
		case "--fill":
			opt.fill = boolValue(hasValue, value, true)
		case "--sidebar":
			opt.sidebar = boolValue(hasValue, value, true)
		case "--no-sidebar":
			opt.sidebar = false
		case "--demo":
			opt.demo = boolValue(hasValue, value, true)
		case "--list":
			opt.list = true
		case "--width":
			var s string
			if s, err = getValue(); err == nil {
				opt.width, err = positiveInt(s, "width")
			}
		case "--height":
			var s string
			if s, err = getValue(); err == nil {
				opt.height, err = positiveInt(s, "height")
			}
		case "--samples":
			var s string
			if s, err = getValue(); err == nil {
				opt.samples, err = positiveInt(s, "samples")
			}
		case "--keys":
			opt.keys, err = getValue()
		case "--ticks":
			var s string
			if s, err = getValue(); err == nil {
				opt.ticks, err = positiveInt(s, "ticks")
			}
		case "--no-color", "--no-colour":
			opt.color = "none"
		case "--stdin":
			opt.stdin = boolValue(hasValue, value, true)
		case "-c", "--command":
			var s string
			if s, err = getValue(); err == nil {
				if strings.TrimSpace(s) == "" {
					err = fmt.Errorf("--command needs a command")
				} else {
					opt.commands = append(opt.commands, s)
				}
			}
		case "--label", "--labels":
			var s string
			if s, err = getValue(); err == nil {
				opt.labels = append(opt.labels, splitLabels(s)...)
			}
		case "--unit":
			var s string
			if s, err = getValue(); err == nil {
				opt.unit, err = parseUnit(s)
			}
		case "--timeout":
			var s string
			if s, err = getValue(); err == nil {
				opt.timeout, err = parseTimeout(s)
			}
		case "--":
			rest := strings.TrimSpace(strings.Join(args[i+1:], " "))
			if rest == "" {
				err = fmt.Errorf("-- needs a command")
			} else {
				opt.commands = append(opt.commands, rest)
			}
			i = len(args)
		default:
			return opt, fmt.Errorf("unknown flag %q", name)
		}
		if err != nil {
			return opt, err
		}
	}
	return opt, nil
}

func boolValue(hasValue bool, value string, def bool) bool {
	if !hasValue {
		return def
	}
	switch strings.ToLower(value) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

func positiveInt(s, name string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return n, nil
}

func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		if n <= 0 {
			return 0, fmt.Errorf("interval must be positive")
		}
		return time.Duration(n * float64(time.Second)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid interval %q", s)
	}
	return d, nil
}

// parseTimeout is parseDuration with an error message that names the flag.
func parseTimeout(s string) (time.Duration, error) {
	d, err := parseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid timeout %q", s)
	}
	return d, nil
}

// splitLabels parses a comma separated series name list.
func splitLabels(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if name := strings.TrimSpace(part); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// parseUnit maps a human readable unit name to the chart unit used for axis
// and legend formatting.
func parseUnit(s string) (chart.Unit, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none", "number", "raw":
		return chart.UnitNone, nil
	case "%", "percent", "pct":
		return chart.UnitPercent, nil
	case "b", "byte", "bytes":
		return chart.UnitBytes, nil
	case "b/s", "byte/s", "bytes/s":
		return chart.UnitBytesRate, nil
	case "count", "counts":
		return chart.UnitCount, nil
	case "/s", "rate":
		return chart.UnitRate, nil
	case "c", "°c", "celsius":
		return chart.UnitCelsius, nil
	case "w", "watt", "watts":
		return chart.UnitWatts, nil
	case "load":
		return chart.UnitLoad, nil
	case "s", "second", "seconds", "time":
		return chart.UnitSeconds, nil
	}
	return chart.UnitNone, fmt.Errorf("unknown unit %q", s)
}

// validateOptions rejects combinations that cannot be charted.
func validateOptions(opt options) error {
	switch {
	case opt.stdin && len(opt.commands) > 0:
		return fmt.Errorf("--stdin and --command cannot be combined")
	case opt.demo && (opt.stdin || len(opt.commands) > 0):
		return fmt.Errorf("--demo cannot be combined with --stdin or --command")
	}
	return nil
}

// colorMode resolves the color capability for standard output.
func colorMode(opt options) chart.ColorMode {
	return resolveColor(opt, isTerminal(os.Stdout))
}

// resolveColor decides how much color to emit. An explicit --color always
// wins, NO_COLOR is honoured, and colored output is suppressed when stdout is
// not a terminal (pipes and files stay clean).
func resolveColor(opt options, tty bool) chart.ColorMode {
	if opt.color != "" {
		if m, ok := chart.ParseColorMode(opt.color); ok {
			return m
		}
	}
	if os.Getenv("NO_COLOR") != "" {
		return chart.ColorNone
	}
	if !tty {
		return chart.ColorNone
	}
	ct := strings.ToLower(os.Getenv("COLORTERM"))
	term := strings.ToLower(os.Getenv("TERM"))
	switch {
	case strings.Contains(ct, "truecolor") || strings.Contains(ct, "24bit"):
		return chart.ColorTrue
	case strings.Contains(term, "256"):
		return chart.Color256
	case term == "dumb":
		return chart.ColorNone
	case strings.Contains(term, "color"), strings.Contains(term, "xterm"),
		strings.Contains(term, "screen"), strings.Contains(term, "tmux"),
		strings.Contains(term, "kitty"), strings.Contains(term, "alacritty"),
		strings.Contains(term, "wezterm"), strings.Contains(term, "foot"):
		return chart.ColorBasic
	}
	return chart.ColorBasic
}

func themeFor(opt options) ui.Theme {
	t, ok := ui.LookupTheme(opt.theme)
	if !ok {
		fmt.Fprintf(os.Stderr, "tui-chart: unknown theme %q, using midnight\n", opt.theme)
	}
	return t
}

func buildSources(opt options) []metrics.Source {
	sources := metrics.All(metrics.Options{
		Capacity: opt.history,
		Demo:     opt.demo,
	})
	if opt.demo {
		return sources
	}
	var ext metrics.Source
	if opt.stdin {
		reader := opt.input
		if reader == nil {
			reader = os.Stdin
		}
		ext = metrics.NewExternalSource(metrics.ExternalOptions{
			Capacity: opt.history,
			Reader:   reader,
			Labels:   opt.labels,
			Unit:     opt.unit,
			Timeout:  opt.timeout,
		})
	} else if len(opt.commands) > 0 {
		ext = metrics.NewExternalSource(metrics.ExternalOptions{
			Capacity: opt.history,
			Commands: opt.commands,
			Labels:   opt.labels,
			Unit:     opt.unit,
			Timeout:  opt.timeout,
		})
	}
	if ext != nil {
		// External data is the reason the user started the program, so show it
		// first instead of making them hunt for a tab.
		sources = append([]metrics.Source{ext}, sources...)
	}
	return sources
}

func listSources(opt options) {
	sources := buildSources(opt)
	if len(sources) == 0 {
		fmt.Println("no metric sources available on this system")
		return
	}
	fmt.Printf("%-12s %-8s %s\n", "ID", "UNIT", "DESCRIPTION")
	for _, s := range sources {
		mode := "summary"
		if s.HasDetail() {
			mode = "summary + breakdown"
		}
		fmt.Printf("%-12s %-8s %-22s %s\n", s.ID(), s.Unit().Suffix(), s.Title(), mode)
	}
}

func runDashboard(opt options) error {
	if err := validateOptions(opt); err != nil {
		return err
	}
	sources := buildSources(opt)
	if len(sources) == 0 {
		return fmt.Errorf("no metric sources available (is /proc mounted?)")
	}
	active := 0
	if opt.source != "" {
		found := false
		for i, s := range sources {
			if s.ID() == strings.ToLower(opt.source) {
				active, found = i, true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown source %q (try 'tui-chart sources')", opt.source)
		}
	}
	theme := themeFor(opt)
	model := ui.New(ui.Config{
		Sources:     sources,
		Interval:    opt.interval,
		Window:      opt.window,
		Renderer:    rendererFrom(opt),
		RendererSet: opt.rendererSet,
		Grid:        opt.grid,
		Average:     opt.average,
		Smooth:      opt.smooth,
		LineFill:    opt.fill,
		Sidebar:     opt.sidebar,
		Mode:        colorMode(opt),
		Theme:       theme,
		Host:        metrics.ReadHost(),
		Version:     version,
	})
	if active != 0 {
		model = withActive(model, active)
	}
	programOpts := []tea.ProgramOption{
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
		tea.WithoutSignalHandler(),
	}
	if opt.stdin {
		// stdin carries data, so the keyboard has to come from the
		// controlling terminal instead.
		programOpts = append(programOpts, tea.WithInputTTY())
	}
	p := tea.NewProgram(model, programOpts...)
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("%w\n"+
			"       the dashboard needs an interactive terminal: "+
			"'tui-chart snapshot' prints a single chart to stdout instead", err)
	}
	return nil
}

// withActive selects the initial source through a synthetic key press.
func withActive(m ui.Model, idx int) ui.Model {
	m.SetActive(idx)
	return m
}

func rendererFrom(opt options) chart.Renderer {
	r, _ := chart.ParseRenderer(opt.renderer)
	return r
}

// runSnapshot renders a single chart to stdout, for scripts and screenshots.
func runSnapshot(opt options) error {
	out, err := snapshotText(opt)
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

// snapshotText renders one chart and returns it as text. When colors are
// disabled the result is plain text, which makes it easy to assert on.
func snapshotText(opt options) (string, error) {
	if err := validateOptions(opt); err != nil {
		return "", err
	}
	sources := buildSources(opt)
	if len(sources) == 0 {
		return "", fmt.Errorf("no metric sources available")
	}
	src := sources[0]
	if opt.source != "" {
		s := metrics.Find(sources, opt.source)
		if s == nil {
			return "", fmt.Errorf("unknown source %q", opt.source)
		}
		src = s
	}
	// Collect history. In demo mode the generators already pre-fill the rings.
	rounds := opt.samples
	if rounds <= 0 {
		rounds = opt.window
	}
	if !opt.demo {
		stream := false
		for i := 0; i < rounds; i++ {
			if s, ok := src.(interface {
				WaitSample(time.Duration) bool
				Stream() bool
			}); ok && s.Stream() {
				stream = true
				// Data-driven samples arrive on their own schedule; wait for
				// the next row instead of sleeping for a fixed interval.
				if !s.WaitSample(opt.timeout) {
					break
				}
			}
			src.Sample()
			if i < rounds-1 && !stream {
				time.Sleep(opt.interval)
			}
		}
	}
	if opt.width < 8 {
		opt.width = 8
	}
	if opt.height < 4 {
		opt.height = 4
	}
	canvas := chart.NewCanvas(opt.width, opt.height)
	pal := themeFor(opt).ChartPalette()
	canvas.Clear(chart.Style{FG: pal.FG, BG: pal.BG})
	lo, hi, fixed := src.Range()
	renderer := src.Preferred()
	if opt.rendererSet {
		renderer = rendererFrom(opt)
	}
	o := chart.Options{
		Renderer: renderer,
		Mode:     colorMode(opt),
		Palette:  pal,
		Unit:     src.Unit(),
		Window:   opt.window,
		Interval: opt.interval,
		Now:      time.Now(),
		Stacked:  src.Stacked(),
		Smooth:   opt.smooth,
		LineFill: opt.fill && renderer == chart.RendererLine,
		Grid:     opt.grid,
		Axis:     true,
		TimeAxis: true,
		Cursor:   true,
		Average:  opt.average,
		Baseline: lo,
		HasYMin:  fixed,
		HasYMax:  fixed,
		YMin:     lo,
		YMax:     hi,
	}
	if !fixed {
		o.HasYMin, o.HasYMax = false, false
		o.BaselineZero = true
		if b, ok := src.(interface{ Baseline() float64 }); ok {
			o.Baseline, o.BaselineZero = b.Baseline(), false
		}
	}
	chart.Draw(canvas, 0, 0, opt.width, opt.height, src.Series(), o)
	out := canvas.Render(colorMode(opt))
	if colorMode(opt) == chart.ColorNone {
		out = stripANSI(out)
	}
	// No trailing newline: the chart is exactly height rows and a final line
	// feed would scroll the screen and clip the top.
	return strings.ReplaceAll(out, "\r\n", "\n"), nil
}

// runFrame renders a single dashboard frame to stdout. It drives the same
// model the interactive program uses, so it doubles as a screenshot tool and a
// smoke test that needs no pty.
func runFrame(opt options) error {
	if err := validateOptions(opt); err != nil {
		return err
	}
	sources := buildSources(opt)
	if len(sources) == 0 {
		return fmt.Errorf("no metric sources available")
	}
	model := ui.New(ui.Config{
		Sources:     sources,
		Interval:    opt.interval,
		Window:      opt.window,
		Renderer:    rendererFrom(opt),
		RendererSet: opt.rendererSet,
		Grid:        opt.grid,
		Average:     opt.average,
		Smooth:      opt.smooth,
		LineFill:    opt.fill,
		Sidebar:     opt.sidebar,
		Mode:        colorMode(opt),
		Theme:       themeFor(opt),
		Host:        metrics.ReadHost(),
		Version:     version,
	}).WithSize(opt.width, opt.height)
	if opt.source != "" {
		for i, s := range sources {
			if s.ID() == strings.ToLower(opt.source) {
				model.SetActive(i)
			}
		}
	}
	if opt.demo {
		model = model.SeedHistory(opt.ticks)
	} else {
		// Live counters need real elapsed time between reads, otherwise every
		// rate is zero. A stream, on the other hand, waits for its own rows.
		stream := false
		for i := 0; i < opt.ticks; i++ {
			if s, ok := model.ActiveSource().(interface {
				WaitSample(time.Duration) bool
				Stream() bool
			}); ok && s.Stream() {
				stream = true
				if !s.WaitSample(opt.timeout) {
					break
				}
			}
			model = model.WithTick(time.Now())
			if i < opt.ticks-1 && !stream {
				time.Sleep(opt.interval)
			}
		}
	}
	for _, k := range strings.Split(opt.keys, ",") {
		if k = strings.TrimSpace(k); k != "" {
			model = model.WithKey(k)
		}
	}
	lines := model.Frame(opt.width, opt.height)
	out := strings.Join(lines, "\r\n")
	if colorMode(opt) == chart.ColorNone {
		// Plain text is easier to diff, assert on and read in a pipe; pass an
		// explicit --color to keep the styling (the screenshot tools do).
		out = strings.ReplaceAll(stripANSI(out), "\r\n", "\n")
	}
	// No trailing newline: the frame is exactly height rows and a final line
	// feed would scroll the screen and clip the header.
	fmt.Print(out)
	return nil
}

func stripANSI(s string) string { return chart.StripANSI(s) }

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
