// Command poolsim compares warm-pool policies for agent sandboxes on a
// simulated two weeks of claims: the first week is history the predictive
// policy learns from, the second week is scored.
//
//	poolsim -coldstart results/coldstart.json -out results/pool.jsonl
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"sandboxbench/controller"
)

func main() {
	cold := flag.String("coldstart", "results/coldstart.json", "sandboxbench coldstart output (runsc claim-to-first-exec samples)")
	arm := flag.String("runtime", "runsc", "which runtime's cold start feeds the creation delay")
	out := flag.String("out", "results/pool.jsonl", "results (JSON lines)")
	slo := flag.Float64("slo", 1.0, "claim-to-ready SLO, seconds")
	seed := flag.Uint64("seed", 1, "workload seed")
	jitter := flag.Float64("nightly-jitter", 600, "nightly eval launch jitter, +/- seconds")
	flag.Parse()

	var rtMs []float64
	if b, err := os.ReadFile(*cold); err == nil {
		var c struct {
			Result struct {
				Samples map[string]struct {
					Ready []float64 `json:"claim_to_first_exec_ms"`
				} `json:"samples"`
			} `json:"result"`
		}
		if err := json.Unmarshal(b, &c); err == nil {
			rtMs = c.Result.Samples[*arm].Ready
		}
	}
	log.Printf("runtime cold-start samples: %d (%s)", len(rtMs), *arm)
	inf := controller.DefaultInfra(rtMs)
	w := controller.DefaultWorkload(14, *seed)
	w.NightlyJitter = *jitter
	plan := controller.NewPlan(w)
	hist := controller.History(plan, inf)
	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	emit := func(r controller.Result, knob float64) {
		r.Knob = knob
		fmt.Println(r)
		enc.Encode(struct {
			controller.Result
			Seed          uint64  `json:"seed"`
			NightlyJitter float64 `json:"nightly_jitter_s"`
			SLO           float64 `json:"slo_s"`
		}{r, *seed, *jitter, *slo})
	}
	const from = 7
	for _, n := range []int{0, 25, 50, 100, 150, 200, 300, 400} {
		emit(controller.Simulate(plan, inf, controller.Static{N: n}, *slo, from), float64(n))
	}
	for _, t := range []float64{5, 10, 20, 40, 80, 160} {
		emit(controller.Simulate(plan, inf, controller.NewHPA(t), *slo, from), t)
		h := controller.NewHPA(t)
		h.Max = 1 << 30
		r := controller.Simulate(plan, inf, h, *slo, from)
		r.Policy = "hpa-nomax"
		emit(r, t)
		emit(controller.Simulate(plan, inf, controller.NewKEDA(t), *slo, from), t)
	}
	for _, q := range []float64{0.5, 0.7, 0.85, 1.0} {
		for _, hints := range []bool{false, true} {
			p := controller.FitPredictive(hist, from, q, 15, hints)
			emit(controller.Simulate(plan, inf, p, *slo, from), q)
		}
	}
}
