package metrics

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// HostInfo describes the machine, for the dashboard header.
type HostInfo struct {
	Hostname string
	Kernel   string
	Cores    int
	MemTotal float64
	Uptime   time.Duration
	OS       string
}

// ReadHost collects static facts about the machine.
func ReadHost() HostInfo {
	h := HostInfo{Cores: runtime.NumCPU(), OS: runtime.GOOS}
	if name, err := os.Hostname(); err == nil {
		h.Hostname = name
	}
	if s, err := readFile(filepath.Join(ProcRoot, "sys", "kernel", "osrelease")); err == nil {
		h.Kernel = strings.TrimSpace(s)
	}
	if m, err := parseMeminfo(filepath.Join(ProcRoot, "meminfo")); err == nil {
		h.MemTotal = m.Total
	}
	if s, err := readFile(filepath.Join(ProcRoot, "uptime")); err == nil {
		if f := strings.Fields(s); len(f) > 0 {
			if secs, err := strconv.ParseFloat(f[0], 64); err == nil {
				h.Uptime = time.Duration(secs * float64(time.Second))
			}
		}
	}
	return h
}
