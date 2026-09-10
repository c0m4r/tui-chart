package metrics

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/c0m4r/tui-chart/internal/chart"
)

// cpuSource reports overall CPU utilisation plus a per component breakdown.
type cpuSource struct {
	base
	last   cpuTimes
	primed bool
	cores  int
	mhz    float64
	lastv  [6]float64
}

func newCPUSource(capacity int) Source {
	if _, cores, err := readCPUStat(); err == nil {
		return buildCPUSource(capacity, len(cores))
	}
	return nil
}

// buildCPUSource wires up the series without touching /proc, so synthetic
// instances can be created anywhere.
func buildCPUSource(capacity, cores int) *cpuSource {
	s := &cpuSource{cores: cores}
	s.id = "cpu"
	s.title = "CPU"
	s.unit = chart.UnitPercent
	s.capacity = capacity
	s.stacked = true
	s.detailName = "breakdown"
	s.combined = []*chart.Series{
		chart.NewSeries("usage", capacity, RampCPU),
	}
	s.detailed = []*chart.Series{
		chart.NewSeries("user", capacity, band("#34d399")),
		chart.NewSeries("system", capacity, band("#60a5fa")),
		chart.NewSeries("nice", capacity, band("#a78bfa")),
		chart.NewSeries("iowait", capacity, band("#fbbf24")),
		chart.NewSeries("irq", capacity, band("#f472b6")),
		chart.NewSeries("steal", capacity, band("#94a3b8")),
	}
	s.mhz = cpuMHz()
	return s
}

// Range pins the CPU axis to 0..100 so charts stay comparable over time.
func (s *cpuSource) Range() (float64, float64, bool) { return 0, 100, true }

// Preferred suggests a renderer that suits the active mode.
func (s *cpuSource) Preferred() chart.Renderer {
	if s.Mode() == ModeDetailed {
		return chart.RendererArea
	}
	return chart.RendererArea
}

// Sample reads /proc/stat and derives utilisation since the previous call.
func (s *cpuSource) Sample() {
	cur, _, err := readCPUStat()
	if err != nil {
		return
	}
	if !s.primed {
		s.last, s.primed = cur, true
		return
	}
	dt := cur.Total - s.last.Total
	if dt <= 0 {
		return
	}
	pct := func(a, b float64) float64 {
		d := b - a
		if d < 0 {
			d = 0
		}
		return d / dt * 100
	}
	user := pct(s.last.User, cur.User)
	system := pct(s.last.System, cur.System)
	nice := pct(s.last.Nice, cur.Nice)
	iowait := pct(s.last.IOWait, cur.IOWait)
	irq := pct(s.last.IRQ, cur.IRQ) + pct(s.last.SoftIRQ, cur.SoftIRQ)
	steal := pct(s.last.Steal, cur.Steal)
	idle := pct(s.last.Idle, cur.Idle)
	usage := 100 - idle
	if usage < 0 {
		usage = 0
	}
	s.last = cur
	s.lastv = [6]float64{user, system, nice, iowait, irq, steal}
	s.pushCombined(usage)
	s.pushDetailed(user, system, nice, iowait, irq, steal)
}

// Info reports the most recent breakdown for the header.
func (s *cpuSource) Info() []KV {
	v := s.lastv
	return []KV{
		{"usr", pctString(v[0])},
		{"sys", pctString(v[1])},
		{"wait", pctString(v[3])},
		{"irq", pctString(v[4])},
	}
}

// Caps describes the CPU for the header.
func (s *cpuSource) Caps() []KV {
	out := []KV{{"cores", strconv.Itoa(s.cores)}}
	if s.mhz > 0 {
		out = append(out, KV{"clock", chart.UnitNone.Format(s.mhz/1000) + " GHz"})
	}
	return out
}

func pctString(v float64) string {
	return chart.UnitPercent.Format(v)
}

// cpuMHz reads the current clock of the first CPU, when cpufreq is available.
func cpuMHz() float64 {
	for _, p := range []string{
		filepath.Join(SysRoot, "devices/system/cpu/cpu0/cpufreq/scaling_cur_freq"),
		filepath.Join(SysRoot, "devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq"),
	} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if khz, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64); err == nil && khz > 0 {
			return khz / 1000
		}
	}
	return 0
}

// memSource reports memory usage with a used/cache/free breakdown.
type memSource struct {
	base
	info  memInfo
	total float64
}

func newMemSource(capacity int) Source {
	m, err := parseMeminfo(filepath.Join(ProcRoot, "meminfo"))
	if err != nil || m.Total <= 0 {
		return nil
	}
	return buildMemSource(capacity, m.Total)
}

// buildMemSource creates a memory source with a known total.
func buildMemSource(capacity int, total float64) *memSource {
	s := &memSource{total: total}
	s.id = "memory"
	s.title = "Memory"
	s.unit = chart.UnitBytes
	s.capacity = capacity
	s.stacked = true
	s.detailName = "breakdown"
	s.combined = []*chart.Series{
		chart.NewSeries("used", capacity, RampMemory),
	}
	s.detailed = []*chart.Series{
		chart.NewSeries("used", capacity, band("#3b82f6")),
		chart.NewSeries("cache", capacity, band("#8b5cf6")),
		chart.NewSeries("free", capacity, band("#334155")),
	}
	return s
}

// Range pins the memory axis to the installed RAM.
func (s *memSource) Range() (float64, float64, bool) { return 0, s.total, true }

// Preferred suggests the area renderer for both modes.
func (s *memSource) Preferred() chart.Renderer { return chart.RendererArea }

// Sample reads /proc/meminfo.
func (s *memSource) Sample() {
	m, err := parseMeminfo(filepath.Join(ProcRoot, "meminfo"))
	if err != nil {
		return
	}
	s.info = m
	used := m.Total - m.Available
	cache := m.Buffers + m.Cached + m.SReclaimable
	free := m.Total - used - cache
	if free < 0 {
		free = 0
	}
	if used < 0 {
		used = 0
	}
	s.pushCombined(used)
	s.pushDetailed(used, cache, free)
}

// Info reports the memory split for the header.
func (s *memSource) Info() []KV {
	m := s.info
	used := m.Total - m.Available
	cache := m.Buffers + m.Cached + m.SReclaimable
	pct := 0.0
	if m.Total > 0 {
		pct = used / m.Total * 100
	}
	return []KV{
		{"used", chart.UnitBytes.Format(used) + " (" + chart.UnitPercent.Format(pct) + ")"},
		{"cache", chart.UnitBytes.Format(cache)},
		{"free", chart.UnitBytes.Format(m.Free)},
	}
}

// swapSource reports swap usage.
type swapSource struct {
	base
	info  memInfo
	total float64
}

func newSwapSource(capacity int) Source {
	m, err := parseMeminfo(filepath.Join(ProcRoot, "meminfo"))
	if err != nil || m.SwapTotal <= 0 {
		return nil
	}
	return buildSwapSource(capacity, m.SwapTotal)
}

// buildSwapSource creates a swap source with a known size.
func buildSwapSource(capacity int, total float64) *swapSource {
	s := &swapSource{total: total}
	s.id = "swap"
	s.title = "Swap"
	s.unit = chart.UnitBytes
	s.capacity = capacity
	s.combined = []*chart.Series{
		chart.NewSeries("used", capacity, RampSwap),
	}
	return s
}

// Range pins the swap axis to the configured swap size.
func (s *swapSource) Range() (float64, float64, bool) { return 0, s.total, true }

// Preferred suggests the area renderer.
func (s *swapSource) Preferred() chart.Renderer { return chart.RendererArea }

// Sample reads /proc/meminfo for swap counters.
func (s *swapSource) Sample() {
	m, err := parseMeminfo(filepath.Join(ProcRoot, "meminfo"))
	if err != nil || m.SwapTotal <= 0 {
		return
	}
	s.info = m
	used := m.SwapTotal - m.SwapFree
	if used < 0 {
		used = 0
	}
	s.pushCombined(used)
}

// Info reports swap usage for the header.
func (s *swapSource) Info() []KV {
	m := s.info
	used := m.SwapTotal - m.SwapFree
	pct := 0.0
	if m.SwapTotal > 0 {
		pct = used / m.SwapTotal * 100
	}
	return []KV{
		{"used", chart.UnitBytes.Format(used) + " (" + chart.UnitPercent.Format(pct) + ")"},
		{"size", chart.UnitBytes.Format(m.SwapTotal)},
	}
}

// loadSource reports the 1, 5 and 15 minute load averages.
type loadSource struct {
	base
	info loadAvg
}

func newLoadSource(capacity int) Source {
	if _, err := readLoadAvg(); err != nil {
		return nil
	}
	return buildLoadSource(capacity)
}

// buildLoadSource creates a load average source.
func buildLoadSource(capacity int) *loadSource {
	s := &loadSource{}
	s.id = "load"
	s.title = "Load"
	s.unit = chart.UnitLoad
	s.capacity = capacity
	s.combined = []*chart.Series{
		chart.NewSeries("1m", capacity, RampLoad),
		chart.NewSeries("5m", capacity, chart.NewRamp(chart.MustHex("#0ea5e9"), chart.MustHex("#7dd3fc"))),
		chart.NewSeries("15m", capacity, chart.NewRamp(chart.MustHex("#64748b"), chart.MustHex("#cbd5e1"))),
	}
	return s
}

// Preferred suggests lines, which suit three overlaid series.
func (s *loadSource) Preferred() chart.Renderer { return chart.RendererLine }

// Sample reads /proc/loadavg.
func (s *loadSource) Sample() {
	l, err := readLoadAvg()
	if err != nil {
		return
	}
	s.info = l
	s.pushCombined(l.One, l.Five, l.Fifteen)
}

// Info reports the run queue for the header.
func (s *loadSource) Info() []KV {
	out := []KV{}
	if s.info.Total > 0 {
		out = append(out, KV{"runnable", strconv.Itoa(s.info.Running) + "/" + strconv.Itoa(s.info.Total)})
	}
	if n, ok := procsRunning(); ok {
		out = append(out, KV{"procs", strconv.Itoa(n)})
	}
	return out
}

// netSource reports network throughput in bytes per second.
type netSource struct {
	base
	tracker rateTracker
	info    []KV
	iface   string
}

func newNetSource(capacity int, iface string) Source {
	if _, err := readNetDev(); err != nil {
		return nil
	}
	return buildNetSource(capacity, iface)
}

// buildNetSource creates a network throughput source.
func buildNetSource(capacity int, iface string) *netSource {
	s := &netSource{iface: iface}
	s.id = "network"
	s.title = "Network"
	s.unit = chart.UnitBytesRate
	s.capacity = capacity
	s.stacked = false
	s.detailName = "rx/tx"
	s.combined = []*chart.Series{
		chart.NewSeries("throughput", capacity, RampRx),
	}
	s.detailed = []*chart.Series{
		chart.NewSeries("rx", capacity, RampRx),
		chart.NewSeries("tx", capacity, RampTx),
	}
	return s
}

// Preferred suggests a filled area for total throughput and lines for rx/tx.
func (s *netSource) Preferred() chart.Renderer {
	if s.Mode() == ModeDetailed {
		return chart.RendererLine
	}
	return chart.RendererArea
}

// Sample sums the interface counters and derives rates.
func (s *netSource) Sample() {
	devs, err := readNetDev()
	if err != nil {
		return
	}
	cur := map[string]float64{}
	var rxTotal, txTotal uint64
	names := make([]string, 0, len(devs))
	for _, d := range devs {
		if d.Name == "lo" {
			continue
		}
		if s.iface != "" && d.Name != s.iface {
			continue
		}
		base := "rx." + d.Name
		cur[base] = float64(d.RxBytes)
		cur["tx."+d.Name] = float64(d.TxBytes)
		rxTotal += d.RxBytes
		txTotal += d.TxBytes
		names = append(names, d.Name)
	}
	if len(names) == 0 {
		return
	}
	cur["rx"] = float64(rxTotal)
	cur["tx"] = float64(txTotal)
	rates, ok := s.tracker.rates(cur)
	if !ok {
		return
	}
	rx, tx := rates["rx"], rates["tx"]
	s.pushCombined(rx + tx)
	s.pushDetailed(rx, tx)
	s.info = []KV{
		{"rx", chart.UnitBytesRate.Format(rx)},
		{"tx", chart.UnitBytesRate.Format(tx)},
		{"if", summarizeIfaces(names)},
	}
}

// Info reports the current throughput.
func (s *netSource) Info() []KV { return s.info }

func summarizeIfaces(names []string) string {
	if len(names) == 0 {
		return "-"
	}
	if len(names) <= 3 {
		return strings.Join(names, ",")
	}
	return names[0] + "+" + strconv.Itoa(len(names)-1)
}

// diskSource reports block device throughput in bytes per second.
type diskSource struct {
	base
	tracker rateTracker
	info    []KV
	devs    map[string]bool
}

func newDiskSource(capacity int) Source {
	disks := wholeDisks()
	if len(disks) == 0 {
		return nil
	}
	return buildDiskSource(capacity, disks)
}

// buildDiskSource creates a disk throughput source for the given devices.
func buildDiskSource(capacity int, disks map[string]bool) *diskSource {
	s := &diskSource{devs: disks}
	s.id = "disk"
	s.title = "Disk I/O"
	s.unit = chart.UnitBytesRate
	s.capacity = capacity
	s.detailName = "read/write"
	s.combined = []*chart.Series{
		chart.NewSeries("throughput", capacity, RampRead),
	}
	s.detailed = []*chart.Series{
		chart.NewSeries("read", capacity, RampRead),
		chart.NewSeries("write", capacity, RampWrite),
	}
	return s
}

// Preferred suggests areas for totals and lines for the read/write split.
func (s *diskSource) Preferred() chart.Renderer {
	if s.Mode() == ModeDetailed {
		return chart.RendererLine
	}
	return chart.RendererArea
}

// Sample reads /proc/diskstats and derives throughput rates.
func (s *diskSource) Sample() {
	stats, err := readDiskStats(func(name string) bool {
		return s.devs[name] && !skipDevice(name)
	})
	if err != nil {
		return
	}
	cur := map[string]float64{}
	var rs, ws uint64
	names := make([]string, 0, len(stats))
	for _, d := range stats {
		cur["r."+d.Name] = float64(d.ReadSectors) * sectorSize
		cur["w."+d.Name] = float64(d.WriteSectors) * sectorSize
		rs += d.ReadSectors
		ws += d.WriteSectors
		names = append(names, d.Name)
	}
	if len(names) == 0 {
		return
	}
	cur["r"] = float64(rs) * sectorSize
	cur["w"] = float64(ws) * sectorSize
	rates, ok := s.tracker.rates(cur)
	if !ok {
		return
	}
	r, w := rates["r"], rates["w"]
	s.pushCombined(r + w)
	s.pushDetailed(r, w)
	s.info = []KV{
		{"read", chart.UnitBytesRate.Format(r)},
		{"write", chart.UnitBytesRate.Format(w)},
		{"dev", summarizeIfaces(names)},
	}
}

// Info reports current disk throughput.
func (s *diskSource) Info() []KV { return s.info }

// tempSource reports a hardware temperature in degrees Celsius.
type tempSource struct {
	base
	label string
	value float64
}

func newTempSource(capacity int) Source {
	v, label, ok := hwmonTemp()
	if !ok {
		return nil
	}
	return buildTempSource(capacity, label, v)
}

// buildTempSource creates a temperature source for a named sensor.
func buildTempSource(capacity int, label string, value float64) *tempSource {
	s := &tempSource{label: label, value: value}
	s.id = "temperature"
	s.title = "Temp"
	s.unit = chart.UnitCelsius
	s.capacity = capacity
	s.combined = []*chart.Series{
		chart.NewSeries("temp", capacity, RampTemp),
	}
	return s
}

// Preferred suggests the area renderer.
func (s *tempSource) Preferred() chart.Renderer { return chart.RendererArea }

// Baseline keeps the temperature axis anchored near the sensor floor.
func (s *tempSource) Baseline() float64 { return math.Max(0, s.value-25) }

// Sample reads the temperature sensor.
func (s *tempSource) Sample() {
	v, _, ok := hwmonTemp()
	if !ok {
		return
	}
	s.value = v
	s.pushCombined(v)
}

// Info reports the sensor label.
func (s *tempSource) Info() []KV {
	return []KV{{"sensor", s.label}}
}
