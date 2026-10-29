// Package controller sizes a warm pool of agent sandboxes. It has a claim
// workload model (interactive sessions, eval-suite launches, RL rollout
// steps), a discrete-event simulator of a pool with a finite sandbox
// creation rate, and pool policies: static, HPA on claim rate (the upstream
// agent-sandbox example), KEDA-style scale to zero, and a predictive
// newsvendor policy.
package controller

import (
	"math"
	"math/rand/v2"
	"sort"
)

const Day = 86400.0

// Workload describes one simulated stretch of days. Times are seconds.
type Workload struct {
	Days int
	Seed uint64

	// interactive agent sessions: Poisson, diurnal, open loop
	InteractivePeak   float64 // claims/s at the daily peak
	InteractiveMedian float64 // session length median, s

	// nightly eval suite: one launch per day near NightlyAt, closed loop
	NightlyAt     float64 // seconds after midnight
	NightlyJitter float64 // uniform +/- s
	NightlyTasks  int
	// PR-triggered eval suites during working hours: Poisson launches
	PREvalsPerHour float64
	PRTasksMin     int
	PRTasksMax     int
	EvalConc       int     // concurrent sandboxes per suite (first wave size)
	EvalTaskMedian float64 // s

	// RL training job: during [RLStart, RLEnd) every day; each step claims
	// RLBatch sandboxes at once and waits for all; then trains RLTrain s.
	RLStart, RLEnd float64
	RLBatch        int
	RLRollout      float64 // rollout median, s
	RLTrain        float64 // s between steps (GPU busy)
}

func DefaultWorkload(days int, seed uint64) Workload {
	return Workload{Days: days, Seed: seed,
		InteractivePeak: 0.25, InteractiveMedian: 300,
		NightlyAt: 2 * 3600, NightlyJitter: 600, NightlyTasks: 600,
		PREvalsPerHour: 1.5, PRTasksMin: 80, PRTasksMax: 300, EvalConc: 100, EvalTaskMedian: 150,
		RLStart: 10 * 3600, RLEnd: 22 * 3600, RLBatch: 128, RLRollout: 60, RLTrain: 90}
}

// Launch is an eval suite start: the scheduler knows it at Time and can
// announce it Hint seconds early to a pool that listens.
type Launch struct {
	Time  float64
	Tasks int
	Kind  string // nightly | pr
}

// Plan is the open-loop part of a workload: interactive arrivals with their
// durations and eval launches. RL steps are generated inside the simulation
// because each step starts when the previous one finishes.
type Plan struct {
	W           Workload
	Interactive []Arrival
	Launches    []Launch
	rng         *rand.Rand
}

type Arrival struct {
	Time, Dur float64
}

func lognormal(r *rand.Rand, median, sigma float64) float64 {
	return median * math.Exp(sigma*r.NormFloat64())
}

// diurnal is 0.15 at night rising to 1 at 15:00.
func diurnal(t float64) float64 {
	h := math.Mod(t, Day) / 3600
	return 0.15 + 0.85*math.Max(0, math.Sin(math.Pi*(h-7)/16))
}

func NewPlan(w Workload) *Plan {
	r := rand.New(rand.NewPCG(w.Seed, 99))
	p := &Plan{W: w, rng: rand.New(rand.NewPCG(w.Seed, 7))}
	end := float64(w.Days) * Day
	// thinning for the non-homogeneous Poisson process
	for t := 0.0; ; {
		t += r.ExpFloat64() / w.InteractivePeak
		if t >= end {
			break
		}
		if r.Float64() < diurnal(t) {
			p.Interactive = append(p.Interactive, Arrival{t, lognormal(r, w.InteractiveMedian, 0.8)})
		}
	}
	for d := 0; d < w.Days; d++ {
		base := float64(d) * Day
		p.Launches = append(p.Launches, Launch{base + w.NightlyAt + (2*r.Float64()-1)*w.NightlyJitter, w.NightlyTasks, "nightly"})
		for t := base + 9*3600; ; {
			t += r.ExpFloat64() * 3600 / w.PREvalsPerHour
			if t >= base+18*3600 {
				break
			}
			if wd := d % 7; wd < 5 { // weekdays only
				p.Launches = append(p.Launches, Launch{t, w.PRTasksMin + r.IntN(w.PRTasksMax-w.PRTasksMin+1), "pr"})
			}
		}
	}
	sort.Slice(p.Launches, func(i, j int) bool { return p.Launches[i].Time < p.Launches[j].Time })
	return p
}
