package controller

import (
	"fmt"
	"math"
	"sort"
)

// Static keeps N sandboxes warm at all times.
type Static struct{ N int }

func (s Static) Name() string            { return "static" }
func (s Static) Interval() float64       { return 5 }
func (s Static) Desired(Observation) int { return s.N }
func (s Static) HintLead() float64       { return 0 }

func rate(o Observation, window float64) float64 {
	k := sort.SearchFloat64s(o.RecentClaims, o.Now-window)
	return float64(len(o.RecentClaims)-k) / window
}

// HPA is the upstream agent-sandbox example: a HorizontalPodAutoscaler on
// the warm pool driven by claim rate, min 10 / max 100 replicas, with HPA's
// default 15 s sync and 300 s scale-down stabilisation. Target is how many
// seconds of claims at the current rate the pool should hold.
type HPA struct {
	Target   float64
	Min, Max int
	hist     []struct {
		t float64
		v int
	}
}

func NewHPA(target float64) *HPA { return &HPA{Target: target, Min: 10, Max: 100} }

func (h *HPA) Name() string      { return "hpa" }
func (h *HPA) Interval() float64 { return 15 }
func (h *HPA) HintLead() float64 { return 0 }
func (h *HPA) Desired(o Observation) int {
	raw := int(math.Ceil(rate(o, 60) * h.Target))
	raw = max(h.Min, min(h.Max, raw))
	h.hist = append(h.hist, struct {
		t float64
		v int
	}{o.Now, raw})
	for len(h.hist) > 0 && h.hist[0].t < o.Now-300 {
		h.hist = h.hist[1:]
	}
	m := 0
	for _, x := range h.hist {
		m = max(m, x.v)
	}
	return m
}

// KEDA scales on the same signal but may go to zero after a cooldown with no
// claims; it has no upper bound here.
type KEDA struct {
	Target   float64
	Cooldown float64
	last     float64
	hpa      *HPA
}

func NewKEDA(target float64) *KEDA {
	return &KEDA{Target: target, Cooldown: 300, hpa: &HPA{Target: target, Min: 0, Max: 1 << 30}}
}

func (k *KEDA) Name() string      { return "keda" }
func (k *KEDA) Interval() float64 { return 15 }
func (k *KEDA) HintLead() float64 { return 0 }
func (k *KEDA) Desired(o Observation) int {
	if n := len(o.RecentClaims); n > 0 {
		k.last = o.RecentClaims[n-1]
	}
	d := k.hpa.Desired(o)
	if o.Now-k.last > k.Cooldown {
		return 0
	}
	return max(d, 1)
}

// Predictive is the policy proposed here. Desired pool =
//
//	max(seasonal, reactive)  for steady demand, both computed from claims
//	                         that are not part of a batch:
//	  seasonal: newsvendor quantile of claims in the next Window seconds at
//	            this minute of the day, learned from a week of history;
//	  reactive: 1.5 x the last 30 s claim rate x Window;
//	+ burst  for periodic batch claimers (RL steps): batches are grouped by
//	         size, and when one size recurs with a stable period the pool
//	         is pre-warmed with that many sandboxes one lead time before the
//	         next expected batch;
//	+ hinted for eval launches announced by the eval scheduler (optional).
//
// The steady part is held for 60 s before it may shrink, so the pool does
// not churn between ticks. Quantile is the newsvendor critical ratio
// cu / (cu + co): the cost of a claim waiting for a creation against an
// idle sandbox for the window.
type Predictive struct {
	Quantile float64
	Window   float64 // s of demand to cover (creation lead time)
	Hints    bool
	slots    [][]float64 // per 60 s slot of the day: steady claims in the next Window s, one value per history day
	period   periodDetector
	label    string
	held     []struct {
		t float64
		v float64
	}
}

const (
	slot     = 60.0
	batchMin = 32  // claims within batchWin seconds that count as one batch
	batchWin = 1.0 // s
)

// steady drops claims that belong to a batch (batchMin or more claims
// within batchWin seconds of the first): those are predicted separately.
func steady(ts []float64) []float64 {
	out := make([]float64, 0, len(ts))
	for i := 0; i < len(ts); {
		j := i
		for j < len(ts) && ts[j]-ts[i] < batchWin {
			j++
		}
		if j-i < batchMin {
			out = append(out, ts[i])
			i++
			continue
		}
		i = j
	}
	return out
}

// FitPredictive learns per-slot steady demand from claim logs of past days.
func FitPredictive(history [][]float64, days int, quantile, window float64, hints bool) *Predictive {
	p := &Predictive{Quantile: quantile, Window: window, Hints: hints, slots: make([][]float64, int(Day/slot))}
	for d := 0; d < days; d++ {
		arr := steady(history[d])
		for s := range p.slots {
			t0 := float64(s) * slot
			best := 0
			for off := 0.0; off < slot; off += 10 {
				a := sort.SearchFloat64s(arr, t0+off)
				b := sort.SearchFloat64s(arr, t0+off+window)
				best = max(best, b-a)
			}
			p.slots[s] = append(p.slots[s], float64(best))
		}
	}
	p.label = "predictive"
	if hints {
		p.label = "predictive+hints"
	}
	return p
}

func (p *Predictive) Name() string      { return p.label }
func (p *Predictive) Interval() float64 { return 5 }
func (p *Predictive) HintLead() float64 {
	if p.Hints {
		return 90
	}
	return 0
}

func quantile(x []float64, q float64) float64 {
	s := append([]float64(nil), x...)
	sort.Float64s(s)
	if len(s) == 0 {
		return 0
	}
	i := int(math.Ceil(q*float64(len(s)))) - 1
	return s[max(0, min(len(s)-1, i))]
}

func (p *Predictive) Desired(o Observation) int {
	tod := math.Mod(o.Now, Day)
	s := int(math.Mod(tod+p.Window, Day) / slot)
	s0 := int(tod / slot)
	seasonal := math.Max(quantile(p.slots[s], p.Quantile), quantile(p.slots[s0], p.Quantile))
	st := steady(o.RecentClaims)
	k := sort.SearchFloat64s(st, o.Now-30)
	reactive := float64(len(st)-k) / 30 * p.Window * 1.5
	want := math.Max(seasonal, reactive)
	// hold the steady part for 60 s before shrinking
	p.held = append(p.held, struct {
		t float64
		v float64
	}{o.Now, want})
	for len(p.held) > 0 && p.held[0].t < o.Now-60 {
		p.held = p.held[1:]
	}
	for _, h := range p.held {
		want = math.Max(want, h.v)
	}
	p.period.observe(o.RecentClaims)
	burst := float64(p.period.expect(o.Now, p.Window+25))
	hinted := 0.0
	for _, h := range o.Hints {
		// hints arrive up to 90 s early; creating a batch takes about 10 s
		// at 32 concurrent creations, so act on them 30 s ahead
		if h.Time-o.Now <= 30 {
			hinted += float64(h.Tasks)
		}
	}
	if hinted > 0 {
		burst = 0 // an announced batch replaces the guess
	}
	return int(math.Ceil(want + burst + hinted))
}

// periodDetector finds batch claims and, per batch size, predicts the next
// batch when that size recurs with a stable period.
type periodDetector struct {
	seen   map[float64]bool
	starts map[int][]float64 // size bucket -> recent start times
	sizes  map[int][]int     // size bucket -> recent sizes
}

// bucket groups batch sizes so a batch of 128 that happens to coincide with
// one interactive claim (129) still matches its series.
func bucket(n int) int { return (n + 8) / 16 }

func (d *periodDetector) observe(recent []float64) {
	if d.seen == nil {
		d.seen = map[float64]bool{}
		d.starts = map[int][]float64{}
		d.sizes = map[int][]int{}
	}
	for i := 0; i < len(recent); {
		j := i
		for j < len(recent) && recent[j]-recent[i] < batchWin {
			j++
		}
		if j-i >= batchMin && !d.seen[recent[i]] {
			d.seen[recent[i]] = true
			b := bucket(j - i)
			d.starts[b] = append(d.starts[b], recent[i])
			d.sizes[b] = append(d.sizes[b], j-i)
			if len(d.starts[b]) > 8 {
				d.starts[b], d.sizes[b] = d.starts[b][1:], d.sizes[b][1:]
			}
		}
		if j-i >= batchMin {
			i = j
		} else {
			i++
		}
	}
}

// expect returns how many sandboxes periodic batches will claim within lead
// seconds (0 if none is due or nothing is periodic enough to bet on). The
// period is the median gap between recent batches of a size, and its spread
// the median absolute deviation, so one odd gap (the first step of a day, a
// slow step) does not switch prediction off.
func (d *periodDetector) expect(now, lead float64) int {
	total := 0
	for b, st := range d.starts {
		var recent []float64
		for _, x := range st {
			if now-x < 7200 {
				recent = append(recent, x)
			}
		}
		sort.Float64s(recent)
		n := len(recent)
		if n < 3 || now-recent[n-1] > 3600 {
			continue
		}
		var gaps []float64
		for i := 1; i < n; i++ {
			gaps = append(gaps, recent[i]-recent[i-1])
		}
		m := median(gaps)
		var dev []float64
		for _, g := range gaps {
			dev = append(dev, math.Abs(g-m))
		}
		spread := 1.4826 * median(dev)
		if m <= 0 || spread/m > 0.25 {
			continue
		}
		size := 0
		for _, x := range d.sizes[b] {
			size = max(size, x)
		}
		next := recent[n-1] + m
		if now >= next-lead-spread && now < next+2*spread+5 {
			total += size
		}
	}
	return total
}

func median(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	s := append([]float64(nil), x...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func meanSD(x []float64) (float64, float64) {
	if len(x) == 0 {
		return 0, 0
	}
	var s, ss float64
	for _, v := range x {
		s += v
	}
	m := s / float64(len(x))
	for _, v := range x {
		ss += (v - m) * (v - m)
	}
	return m, math.Sqrt(ss / float64(len(x)))
}

func (r Result) String() string {
	return fmt.Sprintf("%-17s knob=%-6g p50=%6.0fms p99=%7.0fms overSLO=%5.2f%% idle=%6.1fh/day rlstall=%6.0fs/day created=%.0f/day",
		r.Policy, r.Knob, r.P50Ms, r.P99Ms, 100*r.OverSLO, r.IdleHoursDay, r.RLStallSecDay, r.CreatedPerDay)
}
