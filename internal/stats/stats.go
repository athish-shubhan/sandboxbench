// Package stats summarises benchmark samples: percentiles, and bootstrap
// confidence intervals for the ratio of medians between two runtimes.
package stats

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"sort"
)

// Percentile is the p-th percentile (0..100) with linear interpolation
// between order statistics (numpy's default). NaN for an empty sample; p is
// clamped to [0, 100].
func Percentile(x []float64, p float64) float64 {
	if len(x) == 0 || math.IsNaN(p) {
		return math.NaN()
	}
	p = math.Max(0, math.Min(100, p))
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

// Summarize describes x. For an empty sample every statistic is NaN (null in
// JSON), so one arm that produced no samples cannot break the results file.
func Summarize(x []float64) Summary {
	if len(x) == 0 {
		nan := math.NaN()
		return Summary{P50: nan, P90: nan, P99: nan, Mean: nan, Min: nan, Max: nan}
	}
	s := Summary{N: len(x), P50: Percentile(x, 50), P90: Percentile(x, 90), P99: Percentile(x, 99), Min: math.Inf(1), Max: math.Inf(-1)}
	for _, v := range x {
		s.Mean += v
		s.Min = math.Min(s.Min, v)
		s.Max = math.Max(s.Max, v)
	}
	s.Mean /= float64(len(x))
	return s
}

// Ratio is median(b)/median(a) with a percentile-bootstrap 95% interval.
// Samples are paired by index when paired is true (interleaved runs: rep i
// of a and rep i of b ran back to back), which removes slow drift of the host.
// Pairing needs len(a) == len(b); otherwise the samples are resampled
// independently. With an empty sample every field is NaN (null in JSON).
type Ratio struct {
	Ratio float64 `json:"ratio"`
	Lo    float64 `json:"ci95_lo"`
	Hi    float64 `json:"ci95_hi"`
}

func MedianRatio(a, b []float64, paired bool, seed uint64) Ratio {
	if len(a) == 0 || len(b) == 0 {
		nan := math.NaN()
		return Ratio{nan, nan, nan}
	}
	r := rand.New(rand.NewPCG(seed, 17))
	const B = 4000
	rs := make([]float64, B)
	n := len(a)
	ra, rb := make([]float64, len(a)), make([]float64, len(b))
	for i := 0; i < B; i++ {
		// In the paired case these draws are overwritten below. They are kept
		// so that the random stream, and with it every published interval, is
		// unchanged for a given seed.
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

// encoding/json rejects NaN and ±Inf. Without the methods below a single
// empty arm or zero median made json.Marshal fail for a whole experiment's
// results. Non-finite values are written as null.

func finite(x float64) *float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return nil
	}
	return &x
}

func (s Summary) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		N    int      `json:"n"`
		P50  *float64 `json:"p50"`
		P90  *float64 `json:"p90"`
		P99  *float64 `json:"p99"`
		Mean *float64 `json:"mean"`
		Min  *float64 `json:"min"`
		Max  *float64 `json:"max"`
	}{s.N, finite(s.P50), finite(s.P90), finite(s.P99), finite(s.Mean), finite(s.Min), finite(s.Max)})
}

func (r Ratio) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Ratio *float64 `json:"ratio"`
		Lo    *float64 `json:"ci95_lo"`
		Hi    *float64 `json:"ci95_hi"`
	}{finite(r.Ratio), finite(r.Lo), finite(r.Hi)})
}
