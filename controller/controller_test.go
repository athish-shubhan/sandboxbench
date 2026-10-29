package controller

import (
	"math"
	"testing"
)

func TestHugeStaticPoolHasNoWaits(t *testing.T) {
	w := DefaultWorkload(2, 3)
	p := NewPlan(w)
	r := Simulate(p, DefaultInfra(nil), Static{N: 2000}, 1, 1)
	if r.P99Ms > 51 || r.OverSLO != 0 {
		t.Fatalf("p99 %.0f ms over %.3f", r.P99Ms, r.OverSLO)
	}
}

func TestNoPoolMeansCreationLatency(t *testing.T) {
	w := DefaultWorkload(2, 3)
	p := NewPlan(w)
	inf := DefaultInfra(nil)
	r := Simulate(p, inf, Static{N: 0}, 1, 1)
	if r.P50Ms < 1000*inf.SchedMedian*0.7 {
		t.Fatalf("with no pool every claim waits for a creation: p50 %.0f ms", r.P50Ms)
	}
	if r.IdleHoursDay > 0.01 {
		t.Fatalf("idle %.2f", r.IdleHoursDay)
	}
}

func TestBurstsQueueOnCreationRate(t *testing.T) {
	// a 128-sandbox RL step with 32 concurrent creations needs 4 rounds
	w := DefaultWorkload(1, 5)
	w.InteractivePeak, w.NightlyTasks, w.PREvalsPerHour = 1e-6, 0, 0
	p := NewPlan(w)
	inf := DefaultInfra(nil)
	inf.SchedSigma = 0
	r := Simulate(p, inf, Static{N: 0}, 1, 0)
	want := 4 * (inf.SchedMedian + 0.3) * 1000
	if math.Abs(r.ByClassP99Ms["rl"]-want) > 200 {
		t.Fatalf("rl p99 %.0f ms, want about %.0f", r.ByClassP99Ms["rl"], want)
	}
	if r.RLSteps == 0 || r.RLStallSecDay <= 0 {
		t.Fatalf("%+v", r)
	}
}

func TestHistoryAndPredictiveRun(t *testing.T) {
	w := DefaultWorkload(4, 9)
	p := NewPlan(w)
	inf := DefaultInfra(nil)
	h := History(p, inf)
	if len(h) != 4 || len(h[0]) == 0 {
		t.Fatalf("history days %d", len(h))
	}
	pr := FitPredictive(h, 2, 0.85, 15, true)
	r := Simulate(p, inf, pr, 1, 2)
	hpa := Simulate(p, inf, NewHPA(20), 1, 2)
	// RL steps are closed loop (a faster pool means more steps), so claim
	// counts differ a little between policies; the open-loop part is equal
	if math.Abs(float64(r.Claims-hpa.Claims)) > 0.05*float64(hpa.Claims) {
		t.Fatalf("policies saw different workloads: %d vs %d claims", r.Claims, hpa.Claims)
	}
}

func TestPeriodDetector(t *testing.T) {
	var d periodDetector
	var recent []float64
	for k := 0; k < 4; k++ {
		for i := 0; i < 64; i++ {
			recent = append(recent, float64(k*150)+float64(i)*0.001)
		}
	}
	d.observe(recent)
	if got := d.expect(450+140, 15); got != 64 {
		t.Fatalf("expected a batch of 64 before t=600, got %d", got)
	}
	if got := d.expect(450+60, 15); got != 0 {
		t.Fatalf("too early: %d", got)
	}
}
