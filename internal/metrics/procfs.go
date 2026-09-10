package metrics

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ProcRoot and SysRoot are the pseudo filesystems the collectors read. They are
// variables so tests can point them at fixtures.
var (
	ProcRoot = "/proc"
	SysRoot  = "/sys"
)

// now is the clock used for rate calculations.
var now = time.Now

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// scanLines opens path and yields key/value pairs split by sep.
func scanLines(path string, fn func(fields []string) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !fn(strings.Fields(line)) {
			break
		}
	}
	return sc.Err()
}

// cpuTimes is one row of /proc/stat.
type cpuTimes struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal, Guest, GuestNice float64
	Total                                                                   float64
	Busy                                                                    float64
}

// parseCPUStat parses a "cpu..." line with jiffy counters.
func parseCPUStat(fields []string) (cpuTimes, bool) {
	var t cpuTimes
	if len(fields) < 5 {
		return t, false
	}
	vals := make([]float64, 0, 10)
	for _, f := range fields[1:] {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			break
		}
		vals = append(vals, v)
	}
	get := func(i int) float64 {
		if i < len(vals) {
			return vals[i]
		}
		return 0
	}
	t.User, t.Nice, t.System, t.Idle = get(0), get(1), get(2), get(3)
	t.IOWait, t.IRQ, t.SoftIRQ, t.Steal = get(4), get(5), get(6), get(7)
	t.Guest, t.GuestNice = get(8), get(9)
	// guest time is already accounted inside user/nice
	t.Total = t.User + t.Nice + t.System + t.Idle + t.IOWait + t.IRQ + t.SoftIRQ + t.Steal
	t.Busy = t.Total - t.Idle - t.IOWait
	return t, true
}

// readCPUStat returns the aggregate CPU line and the per-core lines.
func readCPUStat() (cpuTimes, []cpuTimes, error) {
	var total cpuTimes
	var cores []cpuTimes
	found := false
	err := scanLines(filepath.Join(ProcRoot, "stat"), func(f []string) bool {
		if len(f) == 0 || !strings.HasPrefix(f[0], "cpu") {
			return false
		}
		t, ok := parseCPUStat(f)
		if !ok {
			return false
		}
		if f[0] == "cpu" {
			total, found = t, true
			return true
		}
		cores = append(cores, t)
		return true
	})
	if err != nil {
		return total, nil, err
	}
	if !found {
		return total, nil, errors.New("metrics: no cpu line in /proc/stat")
	}
	return total, cores, nil
}

// memInfo holds the /proc/meminfo values we care about, in bytes.
type memInfo struct {
	Total, Free, Available, Buffers, Cached, SReclaimable, Shmem float64
	SwapTotal, SwapFree                                          float64
	Dirty                                                        float64
}

func parseMeminfo(path string) (memInfo, error) {
	var m memInfo
	err := scanLines(path, func(f []string) bool {
		if len(f) < 2 {
			return true
		}
		key := strings.TrimSuffix(f[0], ":")
		v, err := strconv.ParseFloat(f[1], 64)
		if err != nil {
			return true
		}
		if len(f) > 2 && strings.EqualFold(f[2], "kB") {
			v *= 1024
		}
		switch key {
		case "MemTotal":
			m.Total = v
		case "MemFree":
			m.Free = v
		case "MemAvailable":
			m.Available = v
		case "Buffers":
			m.Buffers = v
		case "Cached":
			m.Cached = v
		case "SReclaimable":
			m.SReclaimable = v
		case "Shmem":
			m.Shmem = v
		case "SwapTotal":
			m.SwapTotal = v
		case "SwapFree":
			m.SwapFree = v
		case "Dirty":
			m.Dirty = v
		}
		return true
	})
	return m, err
}

// loadAvg holds /proc/loadavg.
type loadAvg struct {
	One, Five, Fifteen float64
	Running, Total     int
	LastPID            int
}

func readLoadAvg() (loadAvg, error) {
	var l loadAvg
	s, err := readFile(filepath.Join(ProcRoot, "loadavg"))
	if err != nil {
		return l, err
	}
	f := strings.Fields(s)
	if len(f) < 3 {
		return l, errors.New("metrics: malformed /proc/loadavg")
	}
	l.One, _ = strconv.ParseFloat(f[0], 64)
	l.Five, _ = strconv.ParseFloat(f[1], 64)
	l.Fifteen, _ = strconv.ParseFloat(f[2], 64)
	if len(f) > 3 {
		if parts := strings.SplitN(f[3], "/", 2); len(parts) == 2 {
			l.Running, _ = strconv.Atoi(parts[0])
			l.Total, _ = strconv.Atoi(parts[1])
		}
	}
	if len(f) > 4 {
		l.LastPID, _ = strconv.Atoi(f[4])
	}
	return l, nil
}

// netCounters is the cumulative byte counters of one interface.
type netCounters struct {
	Name             string
	RxBytes, TxBytes uint64
}

func readNetDev() ([]netCounters, error) {
	var out []netCounters
	err := scanLines(filepath.Join(ProcRoot, "net", "dev"), func(f []string) bool {
		if len(f) < 17 || !strings.Contains(f[0], ":") {
			return true
		}
		name := strings.TrimSuffix(f[0], ":")
		rx, err1 := strconv.ParseUint(f[1], 10, 64)
		tx, err2 := strconv.ParseUint(f[9], 10, 64)
		if err1 != nil || err2 != nil {
			return true
		}
		out = append(out, netCounters{Name: name, RxBytes: rx, TxBytes: tx})
		return true
	})
	return out, err
}

// diskCounters is the cumulative sector counters of one block device.
type diskCounters struct {
	Name                      string
	ReadSectors, WriteSectors uint64
	Reads, Writes             uint64
}

// sectorSize is the fixed unit used by /proc/diskstats.
const sectorSize = 512

func readDiskStats(allow func(string) bool) ([]diskCounters, error) {
	var out []diskCounters
	err := scanLines(filepath.Join(ProcRoot, "diskstats"), func(f []string) bool {
		if len(f) < 10 {
			return true
		}
		name := f[2]
		if allow != nil && !allow(name) {
			return true
		}
		rs, err1 := strconv.ParseUint(f[5], 10, 64)
		ws, err2 := strconv.ParseUint(f[9], 10, 64)
		rc, err3 := strconv.ParseUint(f[3], 10, 64)
		wc, err4 := strconv.ParseUint(f[7], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			return true
		}
		out = append(out, diskCounters{Name: name, ReadSectors: rs, WriteSectors: ws, Reads: rc, Writes: wc})
		return true
	})
	return out, err
}

// wholeDisks lists block devices that are whole disks (not partitions).
func wholeDisks() map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(SysRoot, "block"))
	if err != nil {
		return out
	}
	for _, e := range entries {
		out[e.Name()] = true
	}
	return out
}

// skipDevice filters out virtual and removable devices that would only add
// noise to a disk throughput chart.
func skipDevice(name string) bool {
	for _, p := range []string{"loop", "ram", "zram", "dm-", "md", "sr", "fd", "nbd"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// hwmonTemp finds the most relevant temperature sensor in /sys/class/hwmon.
func hwmonTemp() (value float64, label string, ok bool) {
	preferred := []string{"k10temp", "zenpower", "coretemp", "cpu_thermal", "acpitz"}
	dirs, err := os.ReadDir(filepath.Join(SysRoot, "class", "hwmon"))
	if err != nil {
		return 0, "", false
	}
	byName := map[string]string{}
	for _, d := range dirs {
		base := filepath.Join(SysRoot, "class", "hwmon", d.Name())
		name, err := readFile(filepath.Join(base, "name"))
		if err != nil {
			continue
		}
		byName[strings.TrimSpace(name)] = base
	}
	for _, want := range preferred {
		base, ok := byName[want]
		if !ok {
			continue
		}
		if v, lbl, ok := firstTemp(base); ok {
			return v, want + " " + lbl, true
		}
	}
	for name, base := range byName {
		if v, lbl, ok := firstTemp(base); ok {
			return v, name + " " + lbl, true
		}
	}
	// Fall back to the thermal zones.
	tz, err := os.ReadDir(filepath.Join(SysRoot, "class", "thermal"))
	if err == nil {
		for _, d := range tz {
			if !strings.HasPrefix(d.Name(), "thermal_zone") {
				continue
			}
			p := filepath.Join(SysRoot, "class", "thermal", d.Name(), "temp")
			s, err := readFile(p)
			if err != nil {
				continue
			}
			if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
				return v / 1000, strings.ReplaceAll(d.Name(), "_", " "), true
			}
		}
	}
	return 0, "", false
}

// firstTemp reads the first temperature channel of a hwmon device.
func firstTemp(base string) (float64, string, bool) {
	for i := 1; i <= 8; i++ {
		p := filepath.Join(base, "temp"+strconv.Itoa(i)+"_input")
		s, err := readFile(p)
		if err != nil {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			continue
		}
		lbl := "temp" + strconv.Itoa(i)
		if l, err := readFile(filepath.Join(base, "temp"+strconv.Itoa(i)+"_label")); err == nil {
			if t := strings.TrimSpace(l); t != "" {
				lbl = t
			}
		}
		return v / 1000, lbl, true
	}
	return 0, "", false
}

// procsRunning returns the number of processes in the run queue.
func procsRunning() (int, bool) {
	entries, err := os.ReadDir(ProcRoot)
	if err != nil {
		return 0, false
	}
	n := 0
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err == nil {
			n++
		}
	}
	return n, true
}
