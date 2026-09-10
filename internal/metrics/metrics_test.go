package metrics

import (
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c0m4r/tui-chart/internal/chart"
)

// useFixtures copies the recorded /proc and /sys trees into a temporary
// directory and points the collectors at it, so tests that rewrite counters
// stay independent of each other and of the checked in fixtures.
func useFixtures(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	copyTree(t, filepath.Join("testdata", "proc"), filepath.Join(dir, "proc"))
	copyTree(t, filepath.Join("testdata", "sys"), filepath.Join(dir, "sys"))
	oldProc, oldSys := ProcRoot, SysRoot
	ProcRoot = filepath.Join(dir, "proc")
	SysRoot = filepath.Join(dir, "sys")
	t.Cleanup(func() { ProcRoot, SysRoot = oldProc, oldSys })
	return dir
}

// copyTree recursively copies a directory.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
}

// fakeClock makes rate calculations deterministic.
func fakeClock(t *testing.T, start time.Time) *time.Time {
	t.Helper()
	cur := start
	old := now
	now = func() time.Time { return cur }
	t.Cleanup(func() { now = old })
	return &cur
}

func TestParseCPUStat(t *testing.T) {
	useFixtures(t)
	proc, _, err := readCPUStat()
	if err != nil {
		t.Fatal(err)
	}
	if proc.Total != 1000+20+300+8000+50+10+5+2 {
		t.Errorf("total = %v", proc.Total)
	}
	if proc.Busy != proc.Total-8000-50 {
		t.Errorf("busy = %v", proc.Busy)
	}
	if proc.Guest != 0 || proc.Steal != 2 {
		t.Errorf("guest/steal = %v/%v", proc.Guest, proc.Steal)
	}
	_, cores, err := readCPUStat()
	if err != nil || len(cores) != 2 {
		t.Fatalf("cores = %d, err = %v", len(cores), err)
	}
}

func TestCPUSourceUsage(t *testing.T) {
	useFixtures(t)
	src := newCPUSource(10)
	if src == nil {
		t.Fatal("cpu source unavailable with fixtures")
	}
	// Two reads of the same file produce identical counters, so no sample is
	// pushed: the source must not report nonsense.
	src.Sample()
	if src.Series()[0].Len() != 0 {
		t.Errorf("first sample should only prime the baseline, got %d samples", src.Series()[0].Len())
	}
	lo, hi, fixed := src.Range()
	if !fixed || lo != 0 || hi != 100 {
		t.Errorf("cpu range = %v..%v fixed=%v", lo, hi, fixed)
	}
	if !src.HasDetail() {
		t.Error("cpu source should offer a breakdown")
	}
	src.SetMode(ModeDetailed)
	if len(src.Series()) != 6 {
		t.Errorf("breakdown series = %d, want 6", len(src.Series()))
	}
	if !src.Stacked() {
		t.Error("cpu breakdown should be stacked")
	}
	src.SetMode(ModeCombined)
	if len(src.Series()) != 1 {
		t.Errorf("summary series = %d, want 1", len(src.Series()))
	}
}

func TestMemSource(t *testing.T) {
	useFixtures(t)
	src := newMemSource(10)
	if src == nil {
		t.Fatal("mem source unavailable with fixtures")
	}
	src.Sample()
	used, ok := src.Series()[0].Last()
	if !ok {
		t.Fatal("no sample pushed")
	}
	// MemTotal 16000000 kB, MemAvailable 6000000 kB -> 10000000 kB used.
	const want = 10_000_000 * 1024
	if math.Abs(used-want) > 1 {
		t.Errorf("used = %v, want %v", used, want)
	}
	lo, hi, fixed := src.Range()
	if !fixed || hi != 16_000_000*1024 {
		t.Errorf("mem range = %v..%v fixed=%v", lo, hi, fixed)
	}
	// The breakdown must sum back to the total.
	src.SetMode(ModeDetailed)
	series := src.Series()
	if len(series) != 3 {
		t.Fatalf("breakdown series = %d", len(series))
	}
	sum := 0.0
	for _, s := range series {
		v, _ := s.Last()
		sum += v
	}
	if math.Abs(sum-hi) > 1 {
		t.Errorf("breakdown sums to %v, want %v", sum, hi)
	}
}

func TestSwapAndTempSources(t *testing.T) {
	useFixtures(t)
	sw := newSwapSource(5)
	if sw == nil {
		t.Fatal("swap source unavailable")
	}
	sw.Sample()
	if v, _ := sw.Series()[0].Last(); math.Abs(v-1000000*1024) > 1 {
		t.Errorf("swap used = %v, want 1000000 kB", v)
	}
	tp := newTempSource(5)
	if tp == nil {
		t.Fatal("temp source unavailable")
	}
	tp.Sample()
	if v, _ := tp.Series()[0].Last(); math.Abs(v-55) > 0.01 {
		t.Errorf("temperature = %v, want 55", v)
	}
	if got := tp.Info()[0].Value; got == "" {
		t.Error("temperature should report its sensor")
	}
}

func TestNetSourceRates(t *testing.T) {
	useFixtures(t)
	clock := fakeClock(t, time.Unix(1000, 0))
	src := newNetSource(5, "")
	if src == nil {
		t.Fatal("net source unavailable")
	}
	src.Sample() // primes the counters
	if src.Series()[0].Len() != 0 {
		t.Fatal("the first sample cannot produce a rate")
	}
	// Rewrite the counters with a known increase: 1000 bytes more on rx and
	// 500 more on tx per interface after one second.
	path := filepath.Join(ProcRoot, "net", "dev")
	writeFile(t, path, `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 1000000    1000    0    0    0     0          0         0  1000000    1000    0    0    0     0       0          0
  eth0: 5001000    5001    0    0    0     0          0         0  2000500    2001    0    0    0     0       0          0
 wlan0: 1001000    1001    0    0    0     0          0         0   500500     501    0    0    0     0       0          0
`)
	*clock = clock.Add(time.Second)
	src.Sample()
	// lo is skipped, so the rate is 2000 B/s in and 1000 B/s out.
	if got := lastOf(t, src); math.Abs(got-3000) > 1 {
		t.Errorf("throughput = %v, want 3000 B/s", got)
	}
	src.SetMode(ModeDetailed)
	series := src.Series()
	rx, _ := series[0].Last()
	tx, _ := series[1].Last()
	if math.Abs(rx-2000) > 1 || math.Abs(tx-1000) > 1 {
		t.Errorf("rx/tx = %v/%v, want 2000/1000", rx, tx)
	}
}

func TestDiskSourceRates(t *testing.T) {
	useFixtures(t)
	clock := fakeClock(t, time.Unix(2000, 0))
	src := newDiskSource(5)
	if src == nil {
		t.Fatal("disk source unavailable")
	}
	src.Sample()
	writeFile(t, filepath.Join(ProcRoot, "diskstats"), ` 259       0 nvme0n1 1000 100 22000 500 2000 200 42000 900 0 0 0 0 0 0
 253       0 dm-0 500 0 4000 100 600 0 8000 200 0 0 0 0 0 0
`)
	*clock = clock.Add(time.Second)
	src.Sample()
	// 2000 extra sectors read and 2000 written; a sector is 512 bytes. The
	// loop device and the device mapper entry must be ignored.
	got := lastOf(t, src)
	if math.Abs(got-4000*512) > 1 {
		t.Errorf("disk throughput = %v, want %v", got, 4000*512)
	}
	src.SetMode(ModeDetailed)
	if v, _ := src.Series()[0].Last(); math.Abs(v-2000*512) > 1 {
		t.Errorf("read rate = %v, want %v", v, 2000*512)
	}
}

func TestLoadSource(t *testing.T) {
	useFixtures(t)
	src := newLoadSource(5)
	if src == nil {
		t.Fatal("load source unavailable")
	}
	src.Sample()
	series := src.Series()
	if len(series) != 3 {
		t.Fatalf("series = %d, want 3", len(series))
	}
	if v, _ := series[0].Last(); math.Abs(v-0.52) > 1e-9 {
		t.Errorf("load1 = %v", v)
	}
	if v, _ := series[2].Last(); math.Abs(v-0.59) > 1e-9 {
		t.Errorf("load15 = %v", v)
	}
	if got := src.Preferred(); got != chart.RendererLine {
		t.Errorf("load should prefer lines, got %v", got)
	}
}

func TestRateTrackerHandlesCounterResets(t *testing.T) {
	clock := fakeClock(t, time.Unix(3000, 0))
	var rt rateTracker
	if _, ok := rt.rates(map[string]float64{"a": 100}); ok {
		t.Error("the first call only primes the tracker")
	}
	*clock = clock.Add(time.Second)
	rates, ok := rt.rates(map[string]float64{"a": 200})
	if !ok || rates["a"] != 100 {
		t.Errorf("rates = %v ok=%v", rates, ok)
	}
	// A counter that goes backwards (device reset) must not produce negative
	// or absurd rates.
	*clock = clock.Add(time.Second)
	rates, _ = rt.rates(map[string]float64{"a": 5})
	if rates["a"] != 0 {
		t.Errorf("counter reset should clamp to zero, got %v", rates["a"])
	}
}

func TestDemoSourcesAreDeterministic(t *testing.T) {
	useFixtures(t)
	a := newDemo(newCPUSource(120))
	b := newDemo(newCPUSource(120))
	if a == nil || b == nil {
		t.Fatal("demo sources unavailable")
	}
	sa, sb := a.Series()[0], b.Series()[0]
	if sa.Len() == 0 {
		t.Fatal("the demo source should pre-fill the history")
	}
	if sa.Len() != sb.Len() {
		t.Fatalf("prefill lengths differ: %d vs %d", sa.Len(), sb.Len())
	}
	for i := 0; i < sa.Len(); i++ {
		if sa.Data.At(i) != sb.Data.At(i) {
			t.Fatalf("demo is not deterministic at %d: %v vs %v", i, sa.Data.At(i), sb.Data.At(i))
		}
	}
	// Values must stay inside the axis range and actually vary.
	lo, hi, _ := a.Range()
	min, max := math.Inf(1), math.Inf(-1)
	for i := 0; i < sa.Len(); i++ {
		v := sa.Data.At(i)
		if v < lo || v > hi {
			t.Fatalf("demo value %v outside %v..%v", v, lo, hi)
		}
		min, max = math.Min(min, v), math.Max(max, v)
	}
	if max-min < 5 {
		t.Errorf("demo data is too flat: %v..%v", min, max)
	}
	// Sampling advances the series with a fresh value.
	beforeLen, _ := sa.Last()
	capBefore := sa.Len()
	a.Sample()
	if after, ok := sa.Last(); !ok || (after == beforeLen && sa.Len() == capBefore) {
		t.Error("sampling did not push a new value")
	}
}

func TestDemoProvidesInfoForEverySource(t *testing.T) {
	useFixtures(t)
	for _, mk := range []func(int) Source{newCPUSource, newMemSource, newSwapSource, newLoadSource} {
		src := mk(30)
		if src == nil {
			continue
		}
		d := newDemo(src)
		if len(d.Info()) == 0 {
			t.Errorf("%s: demo Info() is empty", src.ID())
		}
	}
}

func TestAllSourcesWithFixtures(t *testing.T) {
	useFixtures(t)
	sources := All(Options{Capacity: 30})
	ids := map[string]bool{}
	for _, s := range sources {
		ids[s.ID()] = true
		if s.Capacity() != 30 {
			t.Errorf("%s: capacity = %d", s.ID(), s.Capacity())
		}
		s.Sample()
		for _, ser := range s.Series() {
			if ser.Ramp == nil {
				t.Errorf("%s: series %q has no ramp", s.ID(), ser.Name)
			}
		}
	}
	for _, want := range []string{"cpu", "memory", "swap", "load", "network", "disk", "temperature"} {
		if !ids[want] {
			t.Errorf("source %q missing from the registry", want)
		}
	}
	if Find(sources, "cpu") == nil || Find(sources, "nope") != nil {
		t.Error("Find misbehaves")
	}
	// Demo mode must expose the same ids and keep working without /proc.
	ProcRoot, SysRoot = "/nonexistent-proc", "/nonexistent-sys"
	demo := All(Options{Capacity: 20, Demo: true})
	if len(demo) < 5 {
		t.Fatalf("demo mode produced %d sources", len(demo))
	}
	for _, s := range demo {
		s.Sample()
		if s.Series()[0].Len() == 0 {
			t.Errorf("%s: demo series empty", s.ID())
		}
	}
}

func TestHostInfo(t *testing.T) {
	useFixtures(t)
	h := ReadHost()
	if h.Cores < 1 {
		t.Errorf("cores = %d", h.Cores)
	}
	if h.MemTotal <= 0 {
		t.Errorf("memtotal = %v", h.MemTotal)
	}
	if h.Kernel == "" {
		t.Error("kernel release not read")
	}
	if h.Uptime <= 0 {
		t.Errorf("uptime = %v", h.Uptime)
	}
}

func lastOf(t *testing.T, s Source) float64 {
	t.Helper()
	v, ok := s.Series()[0].Last()
	if !ok {
		t.Fatalf("%s: no samples", s.ID())
	}
	return v
}

// writeFile is a tiny helper for tests that mutate their fixtures.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
