// Package metrics collects live system measurements from /proc and /sys and
// exposes them as chartable series.
package metrics

import (
	"math"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/c0m4r/tui-chart/internal/chart"
)

// Mode selects between a compact summary and a detailed breakdown.
type Mode uint8

// Available view modes.
const (
	ModeCombined Mode = iota
	ModeDetailed
)

// String implements fmt.Stringer.
func (m Mode) String() string {
	if m == ModeDetailed {
		return "detail"
	}
	return "summary"
}

// KV is a labelled value shown in the header or sidebar.
type KV struct {
	Key   string
	Value string
}

// Source is a live metric provider.
type Source interface {
	// ID is the stable identifier used on the command line.
	ID() string
	// Title is the human readable name shown in tabs.
	Title() string
	// Unit formats the values of this source.
	Unit() chart.Unit
	// Series returns the series for the active mode. The slice and the series
	// pointers are stable for the lifetime of the source.
	Series() []*chart.Series
	// Sample takes one measurement and appends it to the series. Sources that
	// need a baseline silently skip the very first call.
	Sample()
	// Mode returns the active view mode.
	Mode() Mode
	// SetMode switches the view mode.
	SetMode(Mode)
	// HasDetail reports whether a detailed breakdown exists.
	HasDetail() bool
	// ModeLabel describes the active mode, for example "total" or "breakdown".
	ModeLabel() string
	// Range returns a fixed vertical range when the metric has natural bounds.
	Range() (lo, hi float64, fixed bool)
	// Preferred suggests a renderer that suits the active mode.
	Preferred() chart.Renderer
	// Stacked reports that the series must be stacked rather than overlaid.
	Stacked() bool
	// Info returns short status items for the header.
	Info() []KV
	// Reset drops all collected samples.
	Reset()
	// Available reports whether the metric can be read on this machine.
	Available() bool
	// Capacity returns the history length in samples.
	Capacity() int
	// SetCapacity resizes the history buffer.
	SetCapacity(int)
}

// base implements the bookkeeping shared by all sources.
type base struct {
	id, title  string
	unit       chart.Unit
	mode       Mode
	capacity   int
	combined   []*chart.Series
	detailed   []*chart.Series
	detailName string
	stacked    bool
}

func (b *base) ID() string       { return b.id }
func (b *base) Title() string    { return b.title }
func (b *base) Unit() chart.Unit { return b.unit }
func (b *base) Capacity() int    { return b.capacity }
func (b *base) Mode() Mode       { return b.mode }
func (b *base) HasDetail() bool  { return len(b.detailed) > 0 }
func (b *base) Stacked() bool    { return b.mode == ModeDetailed && b.stacked }

// Reset drops every collected sample, keeping the configured capacity.
func (b *base) Reset() {
	for _, set := range b.AllSeries() {
		for _, s := range set {
			if s.Data != nil {
				s.Data.Reset()
				s.Data.Resize(b.capacity)
			}
		}
	}
}

func (b *base) Preferred() chart.Renderer { return chart.RendererArea }

// Range reports no fixed bounds unless a source overrides it.
func (b *base) Range() (float64, float64, bool) { return 0, 0, false }
func (b *base) SetCapacity(n int) {
	if n < 1 {
		n = 1
	}
	b.capacity = n
	for _, s := range append(append([]*chart.Series{}, b.combined...), b.detailed...) {
		if s.Data != nil {
			s.Data.Resize(n)
		}
	}
}

// SetMode switches between the summary and the breakdown view.
func (b *base) SetMode(m Mode) {
	if m == ModeDetailed && !b.HasDetail() {
		m = ModeCombined
	}
	b.mode = m
}

// ModeLabel names the active view.
func (b *base) ModeLabel() string {
	if b.mode == ModeDetailed {
		if b.detailName != "" {
			return b.detailName
		}
		return "breakdown"
	}
	return "total"
}

// Series returns the series of the active mode.
func (b *base) Series() []*chart.Series {
	if b.mode == ModeDetailed && len(b.detailed) > 0 {
		return b.detailed
	}
	return b.combined
}

// push appends one value per series, holding the previous value on NaN so the
// series always stay aligned.
func (b *base) push(vals ...float64) {
	s := b.Series()
	n := len(s)
	if len(vals) < n {
		n = len(vals)
	}
	for i := 0; i < n; i++ {
		v := vals[i]
		if math.IsNaN(v) || math.IsInf(v, 0) {
			if last, ok := s[i].Last(); ok {
				v = last
			} else {
				v = 0
			}
		}
		if v < 0 {
			v = 0
		}
		s[i].Push(v)
	}
}

// pushCombined appends to the summary series regardless of the active mode.
func (b *base) pushCombined(vals ...float64) {
	n := len(vals)
	if len(b.combined) < n {
		n = len(b.combined)
	}
	for i := 0; i < n; i++ {
		b.combined[i].Push(vals[i])
	}
}

// pushDetailed appends to the breakdown series regardless of the active mode.
func (b *base) pushDetailed(vals ...float64) {
	n := len(vals)
	if len(b.detailed) < n {
		n = len(b.detailed)
	}
	for i := 0; i < n; i++ {
		b.detailed[i].Push(vals[i])
	}
}

// rateTracker computes per second rates from cumulative counters.
type rateTracker struct {
	last   map[string]float64
	lastT  time.Time
	primed bool
}

// rates returns the per second delta of each counter since the previous call.
func (r *rateTracker) rates(cur map[string]float64) (map[string]float64, bool) {
	t := now()
	out := make(map[string]float64, len(cur))
	if !r.primed || r.last == nil {
		r.last, r.lastT, r.primed = cur, t, true
		return out, false
	}
	dt := t.Sub(r.lastT).Seconds()
	if dt <= 0 {
		dt = 1
	}
	for k, v := range cur {
		prev, ok := r.last[k]
		if !ok || v < prev {
			out[k] = 0
			continue
		}
		out[k] = float64(v-prev) / dt
	}
	r.last, r.lastT = cur, t
	return out, true
}

func (r *rateTracker) reset() { r.primed = false; r.last = nil }

// Ramps used by the built in sources.
var (
	RampCPU = chart.NewRamp(
		chart.MustHex("#22c55e"), chart.MustHex("#84cc16"), chart.MustHex("#facc15"),
		chart.MustHex("#fb923c"), chart.MustHex("#ef4444"),
	)
	RampMemory = chart.NewRamp(
		chart.MustHex("#1d4ed8"), chart.MustHex("#3b82f6"), chart.MustHex("#60a5fa"), chart.MustHex("#a5b4fc"),
	)
	RampSwap = chart.NewRamp(
		chart.MustHex("#b45309"), chart.MustHex("#f59e0b"), chart.MustHex("#fbbf24"),
	)
	RampLoad = chart.NewRamp(
		chart.MustHex("#7c3aed"), chart.MustHex("#c084fc"), chart.MustHex("#f0abfc"),
	)
	RampRx = chart.NewRamp(
		chart.MustHex("#0891b2"), chart.MustHex("#22d3ee"), chart.MustHex("#a5f3fc"),
	)
	RampTx = chart.NewRamp(
		chart.MustHex("#be185d"), chart.MustHex("#f472b6"), chart.MustHex("#fbcfe8"),
	)
	RampRead = chart.NewRamp(
		chart.MustHex("#15803d"), chart.MustHex("#4ade80"), chart.MustHex("#bbf7d0"),
	)
	RampWrite = chart.NewRamp(chart.MustHex("#c2410c"), chart.MustHex("#fb923c"))
	// RampTemp starts cool and ends hot.
	RampTemp = chart.NewRamp(
		chart.MustHex("#22d3ee"), chart.MustHex("#4ade80"), chart.MustHex("#facc15"),
		chart.MustHex("#f97316"), chart.MustHex("#ef4444"),
	)
)

// band turns a solid color into a subtle vertical gradient for stacked bands.
func band(hex string) chart.Ramp {
	c := chart.MustHex(hex)
	return chart.NewRamp(chart.Mul(c, 0.62), c, chart.Lerp(c, chart.RGB(255, 255, 255), 0.18))
}

// Options configures source construction.
type Options struct {
	// Capacity is the history length in samples.
	Capacity int
	// Interface restricts the network source to one interface (empty = all).
	Interface string
	// Demo replaces live readings with synthetic ones.
	Demo bool
}

// All builds the default source list for this machine, in display order.
func All(opt Options) []Source {
	if opt.Capacity < 1 {
		opt.Capacity = 300
	}
	live := []func(int) Source{
		newCPUSource,
		newMemSource,
		newSwapSource,
		newLoadSource,
		func(c int) Source { return newNetSource(c, opt.Interface) },
		newDiskSource,
		newTempSource,
	}
	var out []Source
	have := map[string]bool{}
	for _, ctor := range live {
		s := ctor(opt.Capacity)
		if s == nil || !s.Available() {
			continue
		}
		out = append(out, s)
		have[s.ID()] = true
	}
	if !opt.Demo {
		return out
	}
	// Demo mode always offers the full set of tabs, even on a machine (or in
	// a container) without the relevant sensors.
	fallbacks := []Source{
		buildCPUSource(opt.Capacity, runtime.NumCPU()),
		buildMemSource(opt.Capacity, 32<<30),
		buildSwapSource(opt.Capacity, 4<<30),
		buildLoadSource(opt.Capacity),
		buildNetSource(opt.Capacity, ""),
		buildDiskSource(opt.Capacity, map[string]bool{"nvme0n1": true}),
		buildTempSource(opt.Capacity, "demo", 52),
	}
	for _, fb := range fallbacks {
		if fb == nil || have[fb.ID()] {
			continue
		}
		out = append(out, fb)
		have[fb.ID()] = true
	}
	for i := range out {
		out[i] = newDemo(out[i])
	}
	sort.SliceStable(out, func(i, j int) bool { return sourceOrder(out[i].ID()) < sourceOrder(out[j].ID()) })
	return out
}

// Find returns the source with the given id, or nil.
func Find(sources []Source, id string) Source {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, s := range sources {
		if s.ID() == id {
			return s
		}
	}
	return nil
}

func sourceOrder(id string) int {
	for i, s := range []string{"cpu", "memory", "swap", "load", "network", "disk", "temperature"} {
		if s == id {
			return i
		}
	}
	return 99
}

// Available implements Source for the shared bookkeeping.
func (b *base) Available() bool { return true }

// AllSeries returns every series set (summary first, then breakdown). It is
// used by sidebars and the demo generator, which need the compact view even
// while a source is displaying its breakdown.
func (b *base) AllSeries() [][]*chart.Series {
	return [][]*chart.Series{b.combined, b.detailed}
}
