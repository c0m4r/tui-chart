package chart

import (
	"math"
	"strconv"
	"strings"
)

// Scale maps data values onto the vertical axis of a plot.
type Scale struct {
	Min, Max float64
	Ticks    []float64
	Step     float64
}

// Norm maps v into [0,1] across the scale. Values outside the range are
// clamped, which keeps spikes from escaping the plot area.
func (s Scale) Norm(v float64) float64 {
	if s.Max <= s.Min {
		return 0
	}
	n := (v - s.Min) / (s.Max - s.Min)
	if n < 0 {
		return 0
	}
	if n > 1 {
		return 1
	}
	return n
}

// NewScale computes a nice scale covering [lo,hi] with at most maxTicks ticks.
// Optional hard limits clamp the resulting range (for example 0..100 for a
// percentage axis).
func NewScale(lo, hi float64, maxTicks int, hardLo, hardHi *float64) Scale {
	return NewScaleUnit(UnitNone, lo, hi, maxTicks, hardLo, hardHi)
}

// NewScaleUnit is NewScale for an axis labelled with a specific unit.
func NewScaleUnit(unit Unit, lo, hi float64, maxTicks int, hardLo, hardHi *float64) Scale {
	if maxTicks < 2 {
		maxTicks = 2
	}
	if hardLo != nil && lo < *hardLo {
		lo = *hardLo
	}
	if hardHi != nil && hi > *hardHi {
		hi = *hardHi
	}
	if math.IsNaN(lo) || math.IsNaN(hi) || math.IsInf(lo, 0) || math.IsInf(hi, 0) {
		lo, hi = 0, 1
	}
	if hi < lo {
		lo, hi = hi, lo
	}
	if hi == lo {
		// Degenerate: build a small window around the single value.
		switch {
		case hi == 0:
			lo, hi = 0, 1
		case hi > 0:
			lo = 0
		default:
			hi = 0
		}
		if hi == lo {
			lo, hi = lo-1, hi+1
		}
	}
	step := niceStepFor(unit, hi-lo, maxTicks)
	if step <= 0 {
		step = 1
	}
	nlo := math.Floor(lo/step) * step
	nhi := math.Ceil(hi/step) * step
	if hardLo != nil && nlo < *hardLo {
		nlo = *hardLo
	}
	if hardHi != nil && nhi > *hardHi {
		nhi = *hardHi
	}
	// Never return a range narrower than the data.
	if nlo > lo {
		nlo = lo
	}
	if nhi < hi {
		nhi = hi
	}
	s := Scale{Min: nlo, Max: nhi, Step: step}
	for v := nlo; v <= nhi+step*1e-9; v += step {
		s.Ticks = append(s.Ticks, roundTick(v, step))
		if len(s.Ticks) > 64 {
			break
		}
	}
	return s
}

// roundTick removes floating point fuzz from tick values.
func roundTick(v, step float64) float64 {
	d := 0
	if step > 0 {
		d = int(math.Max(0, math.Ceil(-math.Log10(step)))) + 2
	}
	if d > 12 {
		d = 12
	}
	f := math.Pow(10, float64(d))
	return math.Round(v*f) / f
}

// niceStepFor chooses a step that is nice in the unit the axis is labelled in:
// byte axes step in powers of 1024 so the labels read 0/5/10 GiB instead of
// 4.66/9.31/13.97 GiB.
func niceStepFor(u Unit, rng float64, maxTicks int) float64 {
	if (u == UnitBytes || u == UnitBytesRate) && rng >= 1024 {
		d, k := 1.0, 0
		for rng/d >= 1024 && k < 5 {
			d *= 1024
			k++
		}
		return niceStep(rng/d, maxTicks) * d
	}
	return niceStep(rng, maxTicks)
}

// niceStep picks the smallest "nice" step that keeps the number of ticks at or
// below maxTicks. Multipliers include 2.5 so percentage axes land on
// 0/25/50/75/100 instead of a cramped eleven tick grid.
func niceStep(rng float64, maxTicks int) float64 {
	if rng <= 0 || maxTicks < 2 {
		return 1
	}
	raw := rng / float64(maxTicks-1)
	exp := math.Floor(math.Log10(raw))
	best := math.Inf(1)
	for _, mult := range []float64{1, 2, 2.5, 5, 10} {
		for _, e := range []float64{exp - 1, exp, exp + 1} {
			step := mult * math.Pow(10, e)
			if step <= 0 {
				continue
			}
			if int(math.Floor(rng/step))+1 <= maxTicks && step < best {
				best = step
			}
		}
	}
	if math.IsInf(best, 1) {
		best = niceNum(raw, true)
	}
	return best
}

// niceNum mirrors Heckbert's "nice numbers" algorithm from Graphics Gems.
func niceNum(x float64, round bool) float64 {
	if x <= 0 {
		return 1
	}
	exp := math.Floor(math.Log10(x))
	f := x / math.Pow(10, exp)
	var nf float64
	if round {
		switch {
		case f < 1.5:
			nf = 1
		case f < 3:
			nf = 2
		case f < 7:
			nf = 5
		default:
			nf = 10
		}
	} else {
		switch {
		case f <= 1:
			nf = 1
		case f <= 2:
			nf = 2
		case f <= 5:
			nf = 5
		default:
			nf = 10
		}
	}
	return nf * math.Pow(10, exp)
}

// Unit describes how a value is formatted for humans.
type Unit uint8

// Supported units.
const (
	UnitNone Unit = iota
	UnitPercent
	UnitBytes     // IEC: KiB, MiB, GiB
	UnitBytesRate // IEC per second
	UnitCount     // SI: k, M, G
	UnitRate      // SI per second
	UnitCelsius
	UnitWatts
	UnitLoad
	UnitSeconds
)

// Suffix returns the unit suffix shown next to axis or legend values.
func (u Unit) Suffix() string {
	switch u {
	case UnitPercent:
		return "%"
	case UnitBytes:
		return "B"
	case UnitBytesRate:
		return "B/s"
	case UnitCelsius:
		return "°C"
	case UnitWatts:
		return "W"
	case UnitRate:
		return "/s"
	case UnitSeconds:
		return "s"
	}
	return ""
}

// Format renders a value with unit awareness and automatic precision.
func (u Unit) Format(v float64) string {
	switch u {
	case UnitPercent:
		return trimFloat(v, 1) + "%"
	case UnitBytes, UnitBytesRate:
		return formatIEC(v, u == UnitBytesRate, false)
	case UnitCount, UnitRate:
		return formatSI(v) + u.Suffix()
	case UnitCelsius:
		return trimFloat(v, 1) + "°C"
	case UnitWatts:
		return trimFloat(v, 1) + "W"
	case UnitLoad:
		return trimFloat(v, 2)
	case UnitSeconds:
		return formatDuration(v)
	}
	return trimFloat(v, 2)
}

// FormatShort renders a compact value for axis ticks (no unit suffix).
func (u Unit) FormatShort(v float64) string {
	switch u {
	case UnitPercent:
		return trimFloat(v, 0) + "%"
	case UnitBytes, UnitBytesRate:
		return formatIEC(v, false, true)
	case UnitCount, UnitRate:
		return formatSI(v)
	case UnitCelsius:
		return trimFloat(v, 0) + "°"
	case UnitWatts:
		return trimFloat(v, 0)
	case UnitLoad:
		return trimFloat(v, 1)
	case UnitSeconds:
		return formatDuration(v)
	}
	return trimFloat(v, 1)
}

func trimFloat(v float64, prec int) string {
	if math.Abs(v) < 5e-9 {
		v = 0
	}
	s := strconv.FormatFloat(v, 'f', prec, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	if s == "" || s == "-" {
		s = "0"
	}
	return s
}

func formatIEC(v float64, perSec, short bool) string {
	neg := v < 0
	if neg {
		v = -v
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	prec := 0
	switch {
	case i == 0:
		prec = 0
	case v < 10:
		prec = 2
	case v < 100:
		prec = 1
	}
	if short {
		switch {
		case i == 0:
			prec = 0
		case v < 10:
			prec = 1
		default:
			prec = 0
		}
	}
	s := trimFloat(v, prec)
	unit := units[i]
	if short {
		unit = strings.TrimSuffix(unit, "B")
		if unit == "" {
			unit = "B"
		}
	} else {
		unit = " " + unit
	}
	if perSec {
		unit += "/s"
	}
	if neg {
		s = "-" + s
	}
	return s + unit
}

func formatSI(v float64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	units := []string{"", "k", "M", "G", "T"}
	i := 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	prec := 0
	if i > 0 && v < 10 {
		prec = 1
	}
	s := trimFloat(v, prec)
	if neg {
		s = "-" + s
	}
	return s + units[i]
}

func formatDuration(v float64) string {
	switch {
	case v < 60:
		return trimFloat(v, 1) + "s"
	case v < 3600:
		return trimFloat(v/60, 1) + "m"
	case v < 86400:
		return trimFloat(v/3600, 1) + "h"
	default:
		return trimFloat(v/86400, 1) + "d"
	}
}
