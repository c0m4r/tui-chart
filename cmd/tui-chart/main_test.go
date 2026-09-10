package main

import (
	"strings"
	"testing"
	"time"

	"github.com/c0m4r/tui-chart/internal/chart"
)

func TestParseFlagsDefaults(t *testing.T) {
	opt, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opt.window != 60 {
		t.Errorf("window = %d, want 60", opt.window)
	}
	if opt.interval != time.Second {
		t.Errorf("interval = %v, want 1s", opt.interval)
	}
	if !opt.sidebar {
		t.Error("the sidebar should be on by default")
	}
	if opt.history != 900 {
		t.Errorf("history = %d, want 900", opt.history)
	}
}

func TestParseFlagsForms(t *testing.T) {
	opt, err := parseFlags([]string{
		"--source", "memory", "-i", "250ms", "--window=120", "-r", "line",
		"--theme", "nord", "--color", "256", "--grid", "--average", "--no-sidebar",
		"--history", "300", "--smooth", "--fill", "--demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opt.source != "memory" || opt.interval != 250*time.Millisecond || opt.window != 120 {
		t.Errorf("basic flags: %+v", opt)
	}
	if !opt.rendererSet || !strings.EqualFold(opt.renderer, "line") {
		t.Errorf("renderer = %q set=%v", opt.renderer, opt.rendererSet)
	}
	if opt.theme != "nord" || opt.color != "256" {
		t.Errorf("theme/color = %q/%q", opt.theme, opt.color)
	}
	for name, got := range map[string]bool{
		"grid": opt.grid, "average": opt.average, "smooth": opt.smooth, "fill": opt.fill, "demo": opt.demo,
	} {
		if !got {
			t.Errorf("%s was not enabled", name)
		}
	}
	if opt.sidebar {
		t.Error("--no-sidebar did not take effect")
	}
	if opt.history != 300 {
		t.Errorf("history = %d", opt.history)
	}
	// boolean flags accept an explicit value
	opt, err = parseFlags([]string{"--grid=false", "--demo=no"})
	if err != nil {
		t.Fatal(err)
	}
	if opt.grid || opt.demo {
		t.Errorf("explicit false values ignored: %+v", opt)
	}
}

func TestParseFlagsErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--nope"},
		{"--window"},
		{"--window", "0"},
		{"--window", "abc"},
		{"--interval", "-3s"},
		{"--renderer", "candles"},
		{"--height", "x"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("parseFlags(%v) should fail", args)
		}
	}
}

func TestParseDurationShorthand(t *testing.T) {
	if d, err := parseDuration("2"); err != nil || d != 2*time.Second {
		t.Errorf("bare seconds: %v %v", d, err)
	}
	if d, err := parseDuration("1500ms"); err != nil || d != 1500*time.Millisecond {
		t.Errorf("milliseconds: %v %v", d, err)
	}
	if _, err := parseDuration("0"); err == nil {
		t.Error("zero interval should fail")
	}
}

func TestColorModeDetection(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want chart.ColorMode
	}{
		{map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, chart.ColorTrue},
		{map[string]string{"TERM": "xterm-256color", "COLORTERM": "24bit"}, chart.ColorTrue},
		{map[string]string{"TERM": "xterm-256color"}, chart.Color256},
		{map[string]string{"TERM": "xterm"}, chart.ColorBasic},
		{map[string]string{"TERM": "dumb"}, chart.ColorNone},
		{map[string]string{"TERM": "xterm-256color", "NO_COLOR": "1"}, chart.ColorNone},
	}
	keys := []string{"TERM", "COLORTERM", "NO_COLOR"}
	for _, tc := range cases {
		for _, k := range keys {
			t.Setenv(k, "")
		}
		for k, v := range tc.env {
			t.Setenv(k, v)
		}
		if got := resolveColor(options{}, true); got != tc.want {
			t.Errorf("env %v: got %v, want %v", tc.env, got, tc.want)
		}
	}
	// an explicit flag always wins
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	if got := resolveColor(options{color: "truecolor"}, true); got != chart.ColorTrue {
		t.Errorf("--color truecolor should override the environment, got %v", got)
	}
	if got := resolveColor(options{color: "16"}, true); got != chart.ColorBasic {
		t.Errorf("--color 16 = %v", got)
	}
	// Piped output is plain unless a color mode is requested explicitly.
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	if got := resolveColor(options{}, false); got != chart.ColorNone {
		t.Errorf("non-tty stdout should be colorless, got %v", got)
	}
	if got := resolveColor(options{color: "256"}, false); got != chart.Color256 {
		t.Errorf("explicit color on a pipe = %v", got)
	}
}

func TestSnapshotRendersPlainChart(t *testing.T) {
	out, err := snapshotText(options{
		demo: true, window: 60, history: 200, interval: time.Second,
		width: 70, height: 12, color: "none", source: "cpu", sidebar: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("plain output must not contain escape sequences")
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 12 {
		t.Fatalf("got %d lines, want 12", len(lines))
	}
	for i, l := range lines {
		if w := len([]rune(l)); w != 70 {
			t.Errorf("line %d is %d runes wide, want 70", i, w)
		}
	}
	plain := strings.Join(lines, "\n")
	if !strings.Contains(plain, "100%") || !strings.Contains(plain, "now") {
		t.Errorf("chart is missing its axis labels:\n%s", plain)
	}
	if !strings.ContainsAny(plain, "█▇▆▅▄▃▂▁") {
		t.Error("chart has no filled area")
	}
}

func TestSnapshotHonoursOptions(t *testing.T) {
	base := options{demo: true, window: 60, history: 200, interval: time.Second,
		width: 60, height: 10, color: "none"}
	area, err := snapshotText(base)
	if err != nil {
		t.Fatal(err)
	}
	base.renderer, base.rendererSet = "line", true
	line, err := snapshotText(base)
	if err != nil {
		t.Fatal(err)
	}
	if area == line {
		t.Error("the line renderer produced an identical chart")
	}
	if !strings.ContainsAny(line, "⠀⠁⠂⠃⠄⠅⠆⠇⠈⠉⠊⠋⠌⠍⠎⠏⠐⠑⠒⠓⠔⠕⠖⠗⠘⠙⠚⠛⠜⠝⠞⠟⠠⠡⠢⠣⠤⠥⠦⠧⠨⠩⠪⠫⠬⠭⠮⠯") {
		t.Error("the line renderer should draw braille glyphs")
	}
	// Unknown source is an error, not a panic.
	if _, err := snapshotText(options{demo: true, source: "nope", width: 40, height: 8}); err == nil {
		t.Error("unknown source should fail")
	}
}

func TestBuildSourcesDemo(t *testing.T) {
	sources := buildSources(options{demo: true, history: 50})
	if len(sources) < 5 {
		t.Fatalf("demo mode gave %d sources", len(sources))
	}
	for _, s := range sources {
		if s.Capacity() != 50 {
			t.Errorf("%s: capacity %d", s.ID(), s.Capacity())
		}
		if s.Title() == "" || s.Unit() == chart.Unit(255) {
			t.Errorf("%s: incomplete metadata", s.ID())
		}
	}
}

func TestBoolValue(t *testing.T) {
	if !boolValue(false, "", true) {
		t.Error("flag without a value should be true")
	}
	if boolValue(true, "off", true) {
		t.Error("--flag=off should be false")
	}
	if !boolValue(true, "yes", false) {
		t.Error("--flag=yes should be true")
	}
}
