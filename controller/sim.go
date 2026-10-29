package controller

import (
	"container/heap"
	"math"
	"math/rand/v2"
	"sort"
)

// Infra models how sandboxes come to exist.
type Infra struct {
	SchedMedian float64   // pod scheduling + setup median, s (lognormal)
	SchedSigma  float64   // lognormal sigma
	RuntimeMs   []float64 // empirical runtime claim-to-ready samples, ms (from sandboxbench coldstart)
	MaxCreating int       // concurrent creations across the cluster
	HandoffMs   float64   // binding a claim to a ready sandbox
}

func DefaultInfra(runtimeMs []float64) Infra {
	if len(runtimeMs) == 0 {
		runtimeMs = []float64{300}
	}
	return Infra{SchedMedian: 2.0, SchedSigma: 0.4, RuntimeMs: runtimeMs, MaxCreating: 32, HandoffMs: 50}
}

// Observation is what a policy sees at a control tick.
type Observation struct {
	Now          float64
	Idle         int       // ready, unclaimed
	Creating     int       // in flight or queued
	Waiting      int       // claims with no sandbox yet
	RecentClaims []float64 // claim arrival times in the last 15 minutes
	Hints        []Launch  // eval launches announced and not yet started
}

// Policy decides the desired number of ready-or-coming sandboxes not yet
// bound to a claim.
type Policy interface {
	Name() string
	Interval() float64 // seconds between ticks
	Desired(o Observation) int
	HintLead() float64 // how early it wants eval launches announced (0 = does not listen)
}

type Result struct {
	Policy        string             `json:"policy"`
	Knob          float64            `json:"knob"`
	Claims        int                `json:"claims"`
	P50Ms         float64            `json:"p50_ms"`
	P99Ms         float64            `json:"p99_ms"`
	P999Ms        float64            `json:"p999_ms"`
	OverSLO       float64            `json:"share_over_slo"`
	ByClassP99Ms  map[string]float64 `json:"p99_ms_by_class"`
	IdleHoursDay  float64            `json:"idle_sandbox_hours_per_day"`
	CreatedPerDay float64            `json:"sandboxes_created_per_day"`
	RLStallSecDay float64            `json:"rl_gpu_stall_sec_per_day"`
	RLSteps       int                `json:"rl_steps"`
}

// ---- event queue

type event struct {
	t    float64
	kind int
	a, b int
	f    float64
}

type evq []event

func (q evq) Len() int             { return len(q) }
func (q evq) Less(i, j int) bool   { return q[i].t < q[j].t }
func (q evq) Swap(i, j int)        { q[i], q[j] = q[j], q[i] }
func (q *evq) Push(x any)          { *q = append(*q, x.(event)) }
func (q *evq) Pop() any            { o := *q; e := o[len(o)-1]; *q = o[:len(o)-1]; return e }
func (q *evq) at(t float64, k int) { heap.Push(q, event{t: t, kind: k}) }

const (
	evInteractive = iota
	evLaunch
	evCreated
	evRelease
	evTick
	evRLStep
	evEvalNext
)

type claim struct {
	t     float64
	class string
	dur   float64
	suite int // eval suite index, or rl step index
}

type suite struct {
	left    int
	running int
}

// Simulate runs the plan under a policy and reports latency and cost for
// the days in [evalFrom, Days).
func Simulate(p *Plan, inf Infra, pol Policy, slo float64, evalFrom int) Result {
	w := p.W
	r := rand.New(rand.NewPCG(w.Seed, 31))
	q := &evq{}
	end := float64(w.Days) * Day
	from := float64(evalFrom) * Day
	for i := range p.Interactive {
		heap.Push(q, event{t: p.Interactive[i].Time, kind: evInteractive, a: i})
	}
	for i, l := range p.Launches {
		heap.Push(q, event{t: l.Time, kind: evLaunch, a: i})
	}
	for d := 0; d < w.Days; d++ {
		heap.Push(q, event{t: float64(d)*Day + w.RLStart, kind: evRLStep, a: d})
	}
	q.at(0, evTick)

	var (
		idle, inflight, queued int
		waiting                []claim
		recent                 []float64
		lat                    []float64
		latClass               = map[string][]float64{}
		idleArea               float64
		lastT                  float64
		created                int
		suites                 = make([]suite, len(p.Launches))
		rlLeft                 = map[int]int{}
		rlMax                  = map[int]float64{}
		rlStall                float64
		rlSteps                int
		rlDay                  = map[int]int{}
		rlHints                []Launch // the trainer announces its next step when training starts
	)
	createDelay := func() float64 {
		return lognormal(r, inf.SchedMedian, inf.SchedSigma) + inf.RuntimeMs[r.IntN(len(inf.RuntimeMs))]/1000
	}
	startCreate := func(now float64) {
		for queued > 0 && inflight < inf.MaxCreating {
			queued--
			inflight++
			created++
			heap.Push(q, event{t: now + createDelay(), kind: evCreated})
		}
	}
	var grant func(now float64, c claim, l float64)
	arrive := func(now float64, c claim) {
		recent = append(recent, now)
		if idle > 0 {
			idle--
			grant(now, c, inf.HandoffMs/1000)
			return
		}
		waiting = append(waiting, c)
		// the claim controller creates a sandbox for every claim it cannot serve
		if inflight+queued < len(waiting) {
			queued++
			startCreate(now)
		}
	}
	grant = func(now float64, c claim, l float64) {
		if c.t >= from {
			lat = append(lat, l*1000)
			latClass[c.class] = append(latClass[c.class], l*1000)
		}
		heap.Push(q, event{t: now + l + c.dur, kind: evRelease, a: c.suite, f: l, b: classID(c.class)})
		if c.class == "rl" && l > rlMax[c.suite] {
			rlMax[c.suite] = l
		}
	}
	nextEval := func(now float64, s int) {
		su := &suites[s]
		if su.left == 0 {
			return
		}
		su.left--
		su.running++
		arrive(now, claim{t: now, class: "eval", dur: lognormal(r, w.EvalTaskMedian, 0.6), suite: s})
	}
	rlIdx := 0
	for q.Len() > 0 {
		e := heap.Pop(q).(event)
		if e.t > end {
			break
		}
		if e.t >= from {
			idleArea += float64(idle) * (e.t - math.Max(lastT, from))
		}
		lastT = e.t
		now := e.t
		switch e.kind {
		case evInteractive:
			a := p.Interactive[e.a]
			arrive(now, claim{t: now, class: "interactive", dur: a.Dur, suite: -1})
		case evLaunch:
			l := p.Launches[e.a]
			suites[e.a].left = l.Tasks
			for i := 0; i < w.EvalConc && suites[e.a].left > 0; i++ {
				nextEval(now, e.a)
			}
		case evCreated:
			inflight--
			if len(waiting) > 0 {
				c := waiting[0]
				waiting = waiting[1:]
				grant(now, c, now-c.t+inf.HandoffMs/1000)
			} else {
				idle++
			}
			startCreate(now)
		case evRelease:
			switch e.b {
			case 1: // eval
				suites[e.a].running--
				nextEval(now, e.a)
			case 2: // rl
				rlLeft[e.a]--
				if rlLeft[e.a] == 0 {
					if now >= from {
						rlStall += rlMax[e.a]
						rlSteps++
					}
					d := rlDay[e.a]
					if t := now + w.RLTrain; t < float64(d)*Day+w.RLEnd {
						heap.Push(q, event{t: t, kind: evRLStep, a: d})
						rlHints = append(rlHints, Launch{Time: t, Tasks: w.RLBatch, Kind: "rl"})
						if len(rlHints) > 16 {
							rlHints = rlHints[1:]
						}
					}
				}
			}
		case evRLStep:
			idx := rlIdx
			rlIdx++
			rlLeft[idx] = w.RLBatch
			rlDay[idx] = e.a
			for i := 0; i < w.RLBatch; i++ {
				arrive(now, claim{t: now, class: "rl", dur: lognormal(r, w.RLRollout, 0.3), suite: idx})
			}
		case evTick:
			cut := now - 900
			k := sort.SearchFloat64s(recent, cut)
			recent = recent[k:]
			var hints []Launch
			if lead := pol.HintLead(); lead > 0 {
				for _, l := range p.Launches {
					if l.Time > now && l.Time <= now+lead {
						l.Tasks = min(l.Tasks, w.EvalConc) // the first wave
						hints = append(hints, l)
					}
				}
				for _, l := range rlHints {
					if l.Time > now && l.Time <= now+lead {
						hints = append(hints, l)
					}
				}
			}
			want := pol.Desired(Observation{Now: now, Idle: idle, Creating: inflight + queued, Waiting: len(waiting), RecentClaims: recent, Hints: hints})
			supply := idle + inflight + queued - len(waiting)
			if supply < want {
				queued += want - supply
				startCreate(now)
			} else if extra := supply - want; extra > 0 {
				// cancel queued creations first, then delete idle sandboxes
				c := min(extra, queued)
				queued -= c
				extra -= c
				idle -= min(extra, idle)
			}
			q.at(now+pol.Interval(), evTick)
		}
	}
	res := Result{Policy: pol.Name(), Claims: len(lat), ByClassP99Ms: map[string]float64{}, RLSteps: rlSteps}
	days := float64(w.Days - evalFrom)
	res.P50Ms, res.P99Ms, res.P999Ms = pct(lat, 50), pct(lat, 99), pct(lat, 99.9)
	over := 0
	for _, l := range lat {
		if l > slo*1000 {
			over++
		}
	}
	res.OverSLO = float64(over) / float64(len(lat))
	for c, v := range latClass {
		res.ByClassP99Ms[c] = pct(v, 99)
	}
	res.IdleHoursDay = idleArea / 3600 / days
	res.CreatedPerDay = float64(created) / float64(w.Days)
	res.RLStallSecDay = rlStall / days
	return res
}

func classID(c string) int {
	switch c {
	case "eval":
		return 1
	case "rl":
		return 2
	}
	return 0
}

func pct(x []float64, p float64) float64 {
	if len(x) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), x...)
	sort.Float64s(s)
	i := p / 100 * float64(len(s)-1)
	lo := int(math.Floor(i))
	hi := int(math.Ceil(i))
	return s[lo] + (s[hi]-s[lo])*(i-float64(lo))
}
