package chart

import (
	"math"
	"sort"
)

// Ring is a fixed capacity circular buffer of float64 samples ordered oldest
// to newest.
type Ring struct {
	buf  []float64
	head int // next write position
	n    int // number of valid samples
}

// NewRing allocates a ring with the given capacity.
func NewRing(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring{buf: make([]float64, capacity)}
}

// Cap returns the ring capacity.
func (r *Ring) Cap() int { return len(r.buf) }

// Len returns the number of stored samples.
func (r *Ring) Len() int { return r.n }

// Full reports whether the ring is at capacity.
func (r *Ring) Full() bool { return r.n == len(r.buf) }

// Resize changes the capacity, keeping the newest samples.
func (r *Ring) Resize(n int) {
	if n < 1 {
		n = 1
	}
	if n == len(r.buf) {
		return
	}
	old := r.Slice(nil)
	nb := make([]float64, n)
	keep := len(old)
	if keep > n {
		old = old[keep-n:]
		keep = n
	}
	copy(nb[n-keep:], old)
	*r = Ring{buf: nb, n: keep, head: 0}
}

// Push appends a sample, evicting the oldest when full.
func (r *Ring) Push(v float64) {
	r.buf[r.head] = v
	r.head = (r.head + 1) % len(r.buf)
	if r.n < len(r.buf) {
		r.n++
	}
}

// At returns the i-th sample counting from the oldest (0).
func (r *Ring) At(i int) float64 {
	if i < 0 || i >= r.n {
		return 0
	}
	start := r.head - r.n
	if start < 0 {
		start += len(r.buf)
	}
	return r.buf[(start+i)%len(r.buf)]
}

// Last returns the newest sample.
func (r *Ring) Last() (float64, bool) {
	if r.n == 0 {
		return 0, false
	}
	return r.At(r.n - 1), true
}

// Oldest returns the oldest sample.
func (r *Ring) Oldest() (float64, bool) {
	if r.n == 0 {
		return 0, false
	}
	return r.At(0), true
}

// Slice copies the contents oldest to newest into dst (allocating if short).
func (r *Ring) Slice(dst []float64) []float64 {
	if cap(dst) < r.n {
		dst = make([]float64, r.n)
	}
	dst = dst[:r.n]
	for i := 0; i < r.n; i++ {
		dst[i] = r.At(i)
	}
	return dst
}

// Reset empties the ring.
func (r *Ring) Reset() { r.n = 0; r.head = 0 }

// Series is one named data stream drawn on a chart.
type Series struct {
	// Name is the legend label, for example "used".
	Name string
	// Ramp colors the series from its base (low values) to its top.
	Ramp Ramp
	// Data holds the samples, oldest first.
	Data *Ring
	// Hidden excludes the series from rendering.
	Hidden bool
}

// NewSeries creates a series with a ring buffer capacity.
func NewSeries(name string, capacity int, ramp Ramp) *Series {
	return &Series{Name: name, Ramp: ramp, Data: NewRing(capacity)}
}

// Push appends a sample.
func (s *Series) Push(v float64) {
	if s.Data == nil {
		s.Data = NewRing(1)
	}
	s.Data.Push(v)
}

// Last returns the newest value.
func (s *Series) Last() (float64, bool) {
	if s.Data == nil {
		return 0, false
	}
	return s.Data.Last()
}

// Len returns the sample count.
func (s *Series) Len() int {
	if s.Data == nil {
		return 0
	}
	return s.Data.Len()
}

// Stats summarizes a window of samples.
type Stats struct {
	Count  int
	Last   float64
	Min    float64
	Max    float64
	Avg    float64
	Median float64
	P95    float64
}

// ComputeStats summarizes a ring over the newest n samples (n <= 0 means all).
func ComputeStats(r *Ring, n int) Stats {
	if r == nil || r.Len() == 0 {
		return Stats{}
	}
	if n <= 0 || n > r.Len() {
		n = r.Len()
	}
	start := r.Len() - n
	st := Stats{Count: n, Min: math.Inf(1), Max: math.Inf(-1)}
	vals := make([]float64, 0, n)
	sum := 0.0
	for i := start; i < r.Len(); i++ {
		v := r.At(i)
		if math.IsNaN(v) {
			continue
		}
		vals = append(vals, v)
		sum += v
		if v < st.Min {
			st.Min = v
		}
		if v > st.Max {
			st.Max = v
		}
	}
	if len(vals) == 0 {
		return Stats{}
	}
	st.Count = len(vals)
	st.Last = vals[len(vals)-1]
	st.Avg = sum / float64(len(vals))
	sort.Float64s(vals)
	st.Median = percentile(vals, 0.5)
	st.P95 = percentile(vals, 0.95)
	return st
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := p * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo < 0 {
		lo = 0
	}
	if hi >= len(sorted) {
		hi = len(sorted) - 1
	}
	if lo == hi {
		return sorted[lo]
	}
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}
