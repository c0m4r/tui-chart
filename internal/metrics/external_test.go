package metrics

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/c0m4r/tui-chart/internal/chart"
)

func visibleSeriesOf(src Source) []*chart.Series {
	var out []*chart.Series
	for _, s := range src.Series() {
		if s != nil && !s.Hidden {
			out = append(out, s)
		}
	}
	return out
}

func TestParseExternalLine(t *testing.T) {
	cases := []struct {
		line string
		want []float64
	}{
		{"42", []float64{42}},
		{"  -3.5  ", []float64{-3.5}},
		{"temp=52.3C", []float64{52.3}},
		{"1 2\t3", []float64{1, 2, 3}},
		{"-1.5e3, 2", []float64{-1500, 2}},
		{"\x1b[31m7\x1b[0m", []float64{7}},
		{"no numbers here", nil},
		{"", nil},
	}
	for _, tc := range cases {
		got := parseExternalLine(tc.line)
		if len(got) != len(tc.want) {
			t.Errorf("%q: got %v, want %v", tc.line, got, tc.want)
			continue
		}
		for i := range got {
			if math.Abs(got[i]-tc.want[i]) > 1e-9 {
				t.Errorf("%q: got %v, want %v", tc.line, got, tc.want)
				break
			}
		}
	}
}

func TestExternalCommandSource(t *testing.T) {
	src := NewExternalSource(ExternalOptions{
		Capacity: 10,
		Commands: []string{"echo '1 5'"},
		Labels:   []string{"one", "two"},
		Unit:     chart.UnitCount,
	})
	if src == nil {
		t.Fatal("source was not created")
	}
	if src.ID() != "command" {
		t.Errorf("id = %q", src.ID())
	}
	if src.Unit() != chart.UnitCount {
		t.Errorf("unit = %v", src.Unit())
	}
	src.Sample()
	series := visibleSeriesOf(src)
	if len(series) != 2 {
		t.Fatalf("visible series = %d, want 2", len(series))
	}
	if got, _ := series[0].Last(); got != 1 {
		t.Errorf("first series = %v", got)
	}
	if got, _ := series[1].Last(); got != 5 {
		t.Errorf("second series = %v", got)
	}
	if series[0].Name != "one" || series[1].Name != "two" {
		t.Errorf("labels = %q, %q", series[0].Name, series[1].Name)
	}
	if got := src.Preferred(); got != chart.RendererLine {
		t.Errorf("multi-series preference = %v, want line", got)
	}
	if got := src.(interface{ Baseline() float64 }).Baseline(); got != 1 {
		t.Errorf("baseline = %v, want 1", got)
	}
}

func TestExternalCommandPreservesNegativeValues(t *testing.T) {
	src := NewExternalSource(ExternalOptions{
		Capacity: 5,
		Commands: []string{"printf '%s\\n' -3.5"},
	})
	if src == nil {
		t.Fatal("source was not created")
	}
	src.Sample()
	series := visibleSeriesOf(src)
	if len(series) != 1 {
		t.Fatalf("visible series = %d", len(series))
	}
	if got, _ := series[0].Last(); got != -3.5 {
		t.Errorf("value = %v, want -3.5", got)
	}
	if got := src.Preferred(); got != chart.RendererArea {
		t.Errorf("single-series preference = %v, want area", got)
	}
}

func TestExternalCommandMultipleCommands(t *testing.T) {
	src := NewExternalSource(ExternalOptions{
		Capacity: 5,
		Commands: []string{"echo 10", "echo 20"},
		Labels:   []string{"a", "b"},
	})
	src.Sample()
	series := visibleSeriesOf(src)
	if len(series) != 2 {
		t.Fatalf("visible series = %d", len(series))
	}
	if a, _ := series[0].Last(); a != 10 {
		t.Errorf("a = %v", a)
	}
	if b, _ := series[1].Last(); b != 20 {
		t.Errorf("b = %v", b)
	}
}

func TestExternalCommandErrors(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		hint string
	}{
		{"exit status", "exit 3", "exit 3"},
		{"no numbers", "echo hello", "no numeric"},
		{"missing command", "definitely-not-a-real-command-xyz", "exit 127"},
	}
	for _, tc := range cases {
		src := NewExternalSource(ExternalOptions{
			Capacity: 5,
			Commands: []string{tc.cmd},
		})
		src.Sample()
		for _, s := range visibleSeriesOf(src) {
			if s.Len() != 0 {
				t.Errorf("%s: error produced %d samples", tc.name, s.Len())
			}
		}
		info := src.Info()
		if len(info) < 2 || !strings.Contains(strings.ToLower(info[1].Value), strings.ToLower(tc.hint)) {
			t.Errorf("%s: info = %+v, want %q", tc.name, info, tc.hint)
		}
	}
}

func TestExternalStreamSource(t *testing.T) {
	src := NewExternalSource(ExternalOptions{
		Capacity: 10,
		Reader:   strings.NewReader("1 5\n2 6\n3 7\n"),
		Labels:   []string{"rx", "tx"},
		Timeout:  time.Second,
	})
	if src == nil {
		t.Fatal("source was not created")
	}
	for i := 0; i < 10; i++ {
		if !src.(interface {
			WaitSample(time.Duration) bool
		}).WaitSample(2 * time.Second) {
			break
		}
		src.Sample()
	}
	series := visibleSeriesOf(src)
	if len(series) != 2 {
		t.Fatalf("visible series = %d, want 2", len(series))
	}
	if n := series[0].Len(); n != 3 {
		t.Errorf("rx samples = %d, want 3", n)
	}
	if n := series[1].Len(); n != 3 {
		t.Errorf("tx samples = %d, want 3", n)
	}
	if got, _ := series[0].Data.Last(); got != 3 {
		t.Errorf("last rx = %v", got)
	}
	if got, _ := series[1].Data.Last(); got != 7 {
		t.Errorf("last tx = %v", got)
	}
	info := src.Info()
	if len(info) == 0 || info[0].Value != "eof" {
		t.Errorf("stream info = %+v, want eof", info)
	}
}

func TestExternalStreamSkipsText(t *testing.T) {
	src := NewExternalSource(ExternalOptions{
		Capacity: 10,
		Reader:   strings.NewReader("label line\n\n1\n2\n"),
		Timeout:  time.Second,
	})
	for {
		if !src.(interface {
			WaitSample(time.Duration) bool
		}).WaitSample(2 * time.Second) {
			break
		}
		src.Sample()
	}
	series := visibleSeriesOf(src)
	if len(series) != 1 {
		t.Fatalf("visible series = %d, want 1", len(series))
	}
	if got, _ := series[0].Data.Last(); got != 2 {
		t.Errorf("last value = %v, want 2", got)
	}
	if n := series[0].Len(); n != 2 {
		t.Errorf("samples = %d, want 2", n)
	}
}

func TestExternalStreamCanBeReset(t *testing.T) {
	src := NewExternalSource(ExternalOptions{
		Capacity: 10,
		Reader:   strings.NewReader("1\n2\n"),
		Timeout:  time.Second,
	})
	for {
		if !src.(interface {
			WaitSample(time.Duration) bool
		}).WaitSample(2 * time.Second) {
			break
		}
		src.Sample()
	}
	src.Reset()
	for _, s := range visibleSeriesOf(src) {
		if s.Len() != 0 {
			t.Errorf("%s still has %d samples", s.Name, s.Len())
		}
	}
}

func TestNewExternalSourceValidation(t *testing.T) {
	if src := NewExternalSource(ExternalOptions{}); src != nil {
		t.Error("empty options should not create a source")
	}
	if src := NewExternalSource(ExternalOptions{
		Commands: []string{"echo 1"},
		Reader:   strings.NewReader("1\n"),
	}); src != nil {
		t.Error("commands and reader are mutually exclusive")
	}
}

func TestExternalCommandTimeoutKillsChildren(t *testing.T) {
	src := NewExternalSource(ExternalOptions{
		Capacity: 5,
		Commands: []string{"sleep 2; echo 1"},
		Timeout:  100 * time.Millisecond,
	})
	start := time.Now()
	src.Sample()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("timeout took %v, want about 100ms", elapsed)
	}
	info := src.Info()
	if len(info) < 2 || !strings.Contains(info[1].Value, "timed out") {
		t.Errorf("info = %+v, want a timeout error", info)
	}
	for _, s := range visibleSeriesOf(src) {
		if s.Len() != 0 {
			t.Errorf("timed out command produced %d samples", s.Len())
		}
	}
}
