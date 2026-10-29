package controller

import "math"

// History records claim arrival times per day of a plan, as a pool log would.
// It runs the plan against an always-sufficient pool, so closed-loop claim
// times (eval waves, RL steps) are the ones the workload wants, not ones
// distorted by a slow pool.
func History(p *Plan, inf Infra) [][]float64 {
	rec := &recorder{}
	Simulate(p, inf, rec, math.Inf(1), 0)
	out := make([][]float64, p.W.Days)
	for _, t := range rec.all {
		d := int(t / Day)
		if d < p.W.Days {
			out[d] = append(out[d], t-float64(d)*Day)
		}
	}
	return out
}

type recorder struct {
	all  []float64
	last float64
}

func (r *recorder) Name() string      { return "record" }
func (r *recorder) Interval() float64 { return 5 }
func (r *recorder) HintLead() float64 { return 0 }
func (r *recorder) Desired(o Observation) int {
	for _, t := range o.RecentClaims {
		if t > r.last {
			r.all = append(r.all, t)
		}
	}
	if n := len(o.RecentClaims); n > 0 {
		r.last = math.Max(r.last, o.RecentClaims[n-1])
	}
	return 1000 // effectively unlimited pool
}
