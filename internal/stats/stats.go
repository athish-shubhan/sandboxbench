// Package stats summarises benchmark samples: percentiles, and bootstrap
// confidence intervals for the ratio of medians between two runtimes.
package stats

import (
	"math"
	"math/rand/v2"
	"sort"
)

func Percentile(x []float64, p float64) float64 {
	if len(x) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), x...)
	sort.Float64s(s)
	if len(s) == 1 {
		return s[0]
	}
	r := p / 100 * float64(len(s)-1)
	lo := int(math.Floor(r))
	hi := int(math.Ceil(r))
	return s[lo] + (s[hi]-s[lo])*(r-float64(lo))
}

func Median(x []float64) float64 { return Percentile(x, 50) }

// Summary is the usual description of a latency sample (milliseconds or seconds).
type Summary struct {
	N    int     `json:"n"`
	P50  float64 `json:"p50"`
	P90  float64 `json:"p90"`
	P99  float64 `json:"p99"`
	Mean float64 `json:"mean"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
}

func Summarize(x []float64) Summary {
	s := Summary{N: len(x), P50: Percentile(x, 50), P90: Percentile(x, 90), P99: Percentile(x, 99), Min: math.Inf(1), Max: math.Inf(-1)}
	for _, v := range x {
		s.Mean += v
		s.Min = math.Min(s.Min, v)
		s.Max = math.Max(s.Max, v)
	}
	if len(x) > 0 {
		s.Mean /= float64(len(x))
	}
	return s
}

// Ratio is median(b)/median(a) with a percentile-bootstrap 95% interval.
// Samples are paired by index when paired is true (interleaved runs: rep i
// of a and rep i of b ran back to back), which removes slow drift of the host.
type Ratio struct {
	Ratio float64 `json:"ratio"`
	Lo    float64 `json:"ci95_lo"`
	Hi    float64 `json:"ci95_hi"`
}

func MedianRatio(a, b []float64, paired bool, seed uint64) Ratio {
	r := rand.New(rand.NewPCG(seed, 17))
	const B = 4000
	rs := make([]float64, B)
	n := len(a)
	ra, rb := make([]float64, len(a)), make([]float64, len(b))
	for i := 0; i < B; i++ {
		for j := range ra {
			ra[j] = a[r.IntN(n)]
		}
		if paired && len(a) == len(b) {
			for j := range rb {
				k := r.IntN(n)
				ra[j], rb[j] = a[k], b[k]
			}
		} else {
			for j := range rb {
				rb[j] = b[r.IntN(len(b))]
			}
		}
		rs[i] = Median(rb) / Median(ra)
	}
	return Ratio{Ratio: Median(b) / Median(a), Lo: Percentile(rs, 2.5), Hi: Percentile(rs, 97.5)}
}
