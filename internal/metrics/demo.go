package metrics

import (
	"hash/fnv"
	"math"
	"math/rand"
	"runtime"

	"github.com/c0m4r/tui-chart/internal/chart"
)

// mib is one mebibyte in bytes.
const mib = 1 << 20

// demoSource wraps a live source and produces plausible synthetic readings so
// the interface can be explored (and screenshotted) without waiting for real
// traffic. Generators are seeded per source so runs are reproducible.
type demoSource struct {
	Source
	rng   *rand.Rand
	t     float64
	phase float64
	total float64
	burst float64
}

// newDemo wraps a source with a synthetic generator and pre-fills its history.
func newDemo(src Source) Source {
	if src == nil {
		return nil
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(src.ID()))
	d := &demoSource{
		Source: src,
		rng:    rand.New(rand.NewSource(int64(h.Sum64() & 0x7fffffff))),
		phase:  float64(h.Sum64()%1000) / 100.0,
	}
	if _, hi, fixed := src.Range(); fixed {
		d.total = hi
	}
	d.prefill()
	return d
}

// prefill fills the history so charts look alive immediately.
func (d *demoSource) prefill() {
	n := d.Source.Capacity()
	if n > 240 {
		n = 240
	}
	d.t = -float64(n)
	for i := 0; i < n; i++ {
		d.advance()
	}
}

// Sample advances the generator and pushes one synthetic reading.
func (d *demoSource) Sample() { d.advance() }

// advance moves the clock one step and pushes a reading for every series set.
// Both the summary and the breakdown are fed so switching views never shows an
// empty chart.
func (d *demoSource) advance() {
	d.t++
	if a, ok := d.Source.(interface{ AllSeries() [][]*chart.Series }); ok {
		detailed := false
		for _, set := range a.AllSeries() {
			if len(set) == 0 {
				continue
			}
			d.pushInto(set, d.values(set, detailed))
			detailed = true
		}
		return
	}
	active := d.Source.Series()
	d.pushInto(active, d.values(active, d.Source.Mode() == ModeDetailed))
}

func (d *demoSource) pushInto(series []*chart.Series, vals []float64) {
	for i, s := range series {
		v := 0.0
		if i < len(vals) {
			v = vals[i]
		}
		s.Push(v)
	}
}

func (d *demoSource) noise(scale float64) float64 {
	return (d.rng.Float64() - 0.5) * 2 * scale
}

// values generates one sample for each series of the given set.
func (d *demoSource) values(series []*chart.Series, detailed bool) []float64 {
	t := d.t
	w := func(period, amp, off float64) float64 { return amp * math.Sin(2*math.Pi*(t+off)/period) }
	out := make([]float64, len(series))
	switch d.Source.ID() {
	case "cpu":
		usage := clamp(38+w(97, 14, 0)+w(23, 9, 5)+w(7, 4, 2)+d.noise(1.6), 2, 99)
		if !detailed {
			out[0] = usage
			break
		}
		frac := []float64{0.55, 0.22, 0.03, 0.09, 0.07, 0.04}
		sum := 0.0
		for i := range out {
			f := 0.0
			if i < len(frac) {
				f = frac[i] * (1 + d.noise(0.12))
			}
			out[i] = usage * f
			sum += out[i]
		}
		if sum > 0 { // keep the stack summing to the reported total
			k := usage / sum
			for i := range out {
				out[i] *= k
			}
		}
	case "memory":
		if d.total <= 0 {
			d.total = 32 << 30
		}
		used := clamp(d.total*(0.46+0.05*math.Sin(2*math.Pi*t/240)+d.noise(0.004)), 0, d.total)
		cache := clamp(d.total*(0.28+0.03*math.Sin(2*math.Pi*(t+40)/150)+d.noise(0.003)), 0, d.total-used)
		if detailed {
			out[0], out[1], out[2] = used, cache, d.total-used-cache
		} else {
			out[0] = used
		}
	case "swap":
		if d.total <= 0 {
			d.total = 4 << 30
		}
		out[0] = clamp(d.total*(0.08+0.05*math.Sin(2*math.Pi*(t+30)/420)+d.noise(0.004)), 0, d.total)
	case "load":
		c := float64(runtime.NumCPU())
		out[0] = clamp(c*(0.62+w(61, 0.3, 0))+d.noise(0.12), 0, c*3)
		out[1] = clamp(c*(0.56+w(131, 0.22, 40))+d.noise(0.08), 0, c*3)
		out[2] = clamp(c*(0.5+w(233, 0.16, 80))+d.noise(0.04), 0, c*3)
	case "network":
		if d.rng.Float64() < 0.06 { // occasional bursts
			d.burst = 6 + d.rng.Float64()*10
		}
		d.burst *= 0.86
		rx := (1.2 + 3.4*(0.5+0.5*math.Sin(2*math.Pi*t/37)) + d.burst + d.noise(0.4)) * float64(mib)
		tx := (0.5 + 1.1*(0.5+0.5*math.Sin(2*math.Pi*(t+11)/53)) + d.burst*0.35 + d.noise(0.2)) * float64(mib)
		rx, tx = math.Max(rx, 4096), math.Max(tx, 4096)
		if detailed {
			out[0], out[1] = rx, tx
		} else {
			out[0] = rx + tx
		}
	case "disk":
		if d.rng.Float64() < 0.05 {
			d.burst = 20 + d.rng.Float64()*60
		}
		d.burst *= 0.8
		r := math.Max((0.8+1.6*(0.5+0.5*math.Sin(2*math.Pi*(t+7)/67))+d.noise(0.3))*float64(mib), 1024)
		wr := math.Max((1.4+2.2*(0.5+0.5*math.Sin(2*math.Pi*(t+19)/89))+d.burst*0.2+d.noise(0.3))*float64(mib), 1024)
		if detailed {
			out[0], out[1] = r, wr
		} else {
			out[0] = r + wr
		}
	case "temperature":
		out[0] = 52 + w(173, 12, 0) + w(47, 4, 13) + d.noise(0.5)
	default:
		for i := range out {
			out[i] = 50 + 40*math.Sin(2*math.Pi*(t+float64(i)*13)/97) + d.noise(4)
		}
	}
	return out
}

// Preferred mirrors the wrapped source.
func (d *demoSource) Preferred() chart.Renderer { return d.Source.Preferred() }

// Info reports the newest synthetic reading of each series, so the footer
// stays meaningful while running on generated data.
func (d *demoSource) Info() []KV {
	out := make([]KV, 0, len(d.Source.Series()))
	for _, s := range d.Source.Series() {
		if v, ok := s.Last(); ok {
			out = append(out, KV{Key: s.Name, Value: d.Source.Unit().Format(v)})
		}
	}
	return out
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
