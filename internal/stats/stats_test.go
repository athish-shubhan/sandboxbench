package stats

import (
	"math"
	"testing"
)

func TestPercentile(t *testing.T) {
	x := []float64{4, 1, 3, 2, 5}
	for p, want := range map[float64]float64{0: 1, 25: 2, 50: 3, 100: 5, 90: 4.6} {
		if got := Percentile(x, p); math.Abs(got-want) > 1e-9 {
			t.Errorf("p%v = %v, want %v", p, got, want)
		}
	}
	if !math.IsNaN(Percentile(nil, 50)) {
		t.Error("empty sample should give NaN")
	}
	if Percentile([]float64{7}, 99) != 7 {
		t.Error("single sample")
	}
	if x[0] != 4 {
		t.Error("input was modified")
	}
}

func TestSummarize(t *testing.T) {
	s := Summarize([]float64{1, 2, 3, 4})
	if s.N != 4 || s.Mean != 2.5 || s.Min != 1 || s.Max != 4 || s.P50 != 2.5 {
		t.Fatalf("%+v", s)
	}
}

func TestMedianRatioConstantSamples(t *testing.T) {
	a := []float64{1, 1, 1, 1, 1, 1}
	b := []float64{2, 2, 2, 2, 2, 2}
	for _, paired := range []bool{true, false} {
		r := MedianRatio(a, b, paired, 1)
		if r.Ratio != 2 || r.Lo != 2 || r.Hi != 2 {
			t.Fatalf("paired=%v: %+v", paired, r)
		}
	}
}

// Host drift shared by both arms inflates the unpaired interval; pairing
// removes it. A/A (identical samples) must contain 1.
func TestPairedIntervalIsTighterUnderDrift(t *testing.T) {
	var a, b []float64
	for i := 0; i < 12; i++ {
		base := 1 + float64(i) // slow drift
		a = append(a, base)
		b = append(b, 1.5*base)
	}
	p := MedianRatio(a, b, true, 7)
	u := MedianRatio(a, b, false, 7)
	if p.Hi-p.Lo >= u.Hi-u.Lo {
		t.Fatalf("paired width %.3f should be below unpaired %.3f", p.Hi-p.Lo, u.Hi-u.Lo)
	}
	if p.Lo > 1.5 || p.Hi < 1.5 {
		t.Fatalf("paired interval %+v misses the true ratio 1.5", p)
	}
	aa := MedianRatio(a, a, true, 3)
	if aa.Ratio != 1 || aa.Lo > 1 || aa.Hi < 1 {
		t.Fatalf("A/A: %+v", aa)
	}
}

func TestMedianRatioDeterministic(t *testing.T) {
	a, b := []float64{1, 2, 3, 4}, []float64{2, 4, 5, 9}
	if MedianRatio(a, b, true, 5) != MedianRatio(a, b, true, 5) {
		t.Fatal("same seed gave different intervals")
	}
}
