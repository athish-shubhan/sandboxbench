package stats

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"testing"
)

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

// Expected values are numpy.percentile (linear interpolation).
func TestPercentile(t *testing.T) {
	x := []float64{4, 1, 3, 2} // unsorted on purpose
	near(t, "p0", Percentile(x, 0), 1, 0)
	near(t, "p50", Percentile(x, 50), 2.5, 1e-12)
	near(t, "p90", Percentile(x, 90), 3.7, 1e-12)
	near(t, "p100", Percentile(x, 100), 4, 0)
	near(t, "single", Percentile([]float64{5}, 99), 5, 0)
	near(t, "p>100 clamps", Percentile(x, 150), 4, 0)
	near(t, "p<0 clamps", Percentile(x, -5), 1, 0)
	if !math.IsNaN(Percentile(nil, 50)) || !math.IsNaN(Percentile(x, math.NaN())) {
		t.Fatal("empty sample or NaN p should give NaN")
	}
	if x[0] != 4 {
		t.Fatal("Percentile must not sort its input in place")
	}
}

func TestSummarize(t *testing.T) {
	s := Summarize([]float64{3, 1, 2})
	if s.N != 3 || s.P50 != 2 || s.Mean != 2 || s.Min != 1 || s.Max != 3 {
		t.Fatalf("%+v", s)
	}
	near(t, "p90", s.P90, 2.8, 1e-12)
	e := Summarize(nil)
	if e.N != 0 || !math.IsNaN(e.P50) || !math.IsNaN(e.Min) || !math.IsNaN(e.Max) || !math.IsNaN(e.Mean) {
		t.Fatalf("empty: %+v", e)
	}
}

// Pins the bootstrap output for a fixed seed. These values come from the
// implementation that produced results/; if this test fails, every published
// confidence interval would change on a re-run.
func TestMedianRatioReproducible(t *testing.T) {
	a := []float64{1.31, 1.42, 1.38, 1.40, 1.36, 1.45, 1.33, 1.39}
	b := []float64{3.40, 3.52, 3.47, 3.61, 3.44, 3.49, 3.38, 3.55}
	if got, want := MedianRatio(a, b, true, 10), (Ratio{2.5126353790613725, 2.4683098591549295, 2.5563909774436087}); got != want {
		t.Fatalf("paired: got %#v, want %#v", got, want)
	}
	if got, want := MedianRatio(a, b, false, 10), (Ratio{2.5126353790613725, 2.4397163120567376, 2.6165413533834587}); got != want {
		t.Fatalf("unpaired: got %#v, want %#v", got, want)
	}
}

// Under slow host drift that hits both arms of a pair equally, the paired
// interval must be much narrower than the unpaired one and both must cover
// the true ratio.
func TestMedianRatioPairingRemovesDrift(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	const n, trueRatio = 40, 2.0
	a, b := make([]float64, n), make([]float64, n)
	for i := range a {
		drift := 1 + float64(i)/n // the host gets 2x slower over the run
		a[i] = drift * (1 + 0.01*r.NormFloat64())
		b[i] = trueRatio * drift * (1 + 0.01*r.NormFloat64())
	}
	p, u := MedianRatio(a, b, true, 1), MedianRatio(a, b, false, 1)
	for name, x := range map[string]Ratio{"paired": p, "unpaired": u} {
		if !(x.Lo <= trueRatio && trueRatio <= x.Hi) {
			t.Fatalf("%s CI %+v misses %v", name, x, trueRatio)
		}
	}
	if pw, uw := p.Hi-p.Lo, u.Hi-u.Lo; pw*3 > uw {
		t.Fatalf("paired width %.4f should be well below unpaired %.4f", pw, uw)
	}
}

func TestMedianRatioEdgeCases(t *testing.T) {
	for _, c := range [][2][]float64{{nil, {1}}, {{1}, nil}, {nil, nil}} {
		r := MedianRatio(c[0], c[1], true, 1)
		if !math.IsNaN(r.Ratio) || !math.IsNaN(r.Lo) || !math.IsNaN(r.Hi) {
			t.Fatalf("empty input: %+v", r)
		}
	}
	// unequal lengths fall back to unpaired resampling instead of misindexing
	r := MedianRatio([]float64{1, 1, 1}, []float64{2, 2}, true, 1)
	if r.Ratio != 2 || r.Lo != 2 || r.Hi != 2 {
		t.Fatalf("unequal lengths: %+v", r)
	}
}

func TestJSONNonFinite(t *testing.T) {
	b, err := json.Marshal(map[string]any{"s": Summarize(nil), "r": MedianRatio([]float64{0, 0}, []float64{1, 1}, true, 1)})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"r":{"ratio":null,"ci95_lo":null,"ci95_hi":null},"s":{"n":0,"p50":null,"p90":null,"p99":null,"mean":null,"min":null,"max":null}}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
	b, err = json.Marshal(Summarize([]float64{1, 2}))
	if err != nil || string(b) != `{"n":2,"p50":1.5,"p90":1.9,"p99":1.99,"mean":1.5,"min":1,"max":2}` {
		t.Fatalf("finite values must encode as before: %s %v", b, err)
	}
}
