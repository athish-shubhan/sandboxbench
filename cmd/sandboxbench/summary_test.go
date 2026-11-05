package main

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"sandboxbench/internal/stats"
	"sandboxbench/internal/work"
)

// The README's workload table comes from results/workloads.json. Recomputing
// the summary from the raw samples in that file must give the committed
// summary exactly: medians, percentiles, ratios and bootstrap intervals.
func TestCommittedWorkloadSummaryReproduces(t *testing.T) {
	b, err := os.ReadFile("../../results/workloads.json")
	if err != nil {
		t.Skip("results/workloads.json not present")
	}
	var f struct {
		Result struct {
			Tasks   []work.Task       `json:"tasks"`
			Samples []Sample          `json:"samples"`
			Summary []json.RawMessage `json:"summary"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	got := summarizeWork(f.Result.Tasks, f.Result.Samples)
	if len(got) != len(f.Result.Summary) {
		t.Fatalf("%d tasks, committed %d", len(got), len(f.Result.Summary))
	}
	for i, g := range got {
		gb, _ := json.Marshal(g)
		var gm, wm any
		_ = json.Unmarshal(gb, &gm)
		_ = json.Unmarshal(f.Result.Summary[i], &wm)
		if !equalJSON(gm, wm) {
			t.Errorf("%s: recomputed\n%s\ncommitted\n%s", g.Task, gb, f.Result.Summary[i])
		}
	}
}

func equalJSON(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k := range x {
			if !equalJSON(x[k], y[k]) {
				return false
			}
		}
		return true
	case float64:
		y, ok := b.(float64)
		return ok && math.Abs(x-y) <= 1e-12*math.Max(1, math.Abs(y))
	default:
		xb, _ := json.Marshal(a)
		yb, _ := json.Marshal(b)
		return string(xb) == string(yb)
	}
}

// When different reps fail in different arms, ratios must pair the same
// rep, not the same index.
func TestSummarizePairsByRep(t *testing.T) {
	task := work.Task{Name: "x", Kind: "agent"}
	var s []Sample
	// runc takes 1 s, runsc 2 s, except that rep r of each arm takes 1+r s
	// longer: pairing by rep gives a ratio CI close to 2.
	for rep := 0; rep < 8; rep++ {
		drift := float64(rep)
		s = append(s, Sample{Arm: "runc", Task: "x", Rep: rep, WallSec: 1 + drift},
			Sample{Arm: "runsc", Task: "x", Rep: rep, WallSec: 2 * (1 + drift)},
			Sample{Arm: "runc-aa", Task: "x", Rep: rep, WallSec: 1 + drift})
	}
	s[0*3+0].Code = 1 // runc fails rep 0
	s[7*3+1].Code = 1 // runsc fails rep 7: equal counts, different reps
	got := summarizeWork([]work.Task{task}, s)[0]
	if got.Failures["runc"] != 1 || got.Failures["runsc"] != 1 {
		t.Fatalf("failures %v", got.Failures)
	}
	if got.ByArm["runc"].N != 7 || got.ByArm["runsc"].N != 7 {
		t.Fatalf("summaries keep every success: %+v", got.ByArm)
	}
	r := got.RunscRunc
	if math.Abs(r.Ratio-2) > 1e-9 || r.Lo < 2-1e-9 || r.Hi > 2+1e-9 {
		t.Fatalf("paired by rep, every pair has ratio 2 exactly: %+v", r)
	}
	if aa := got.AAOverRun; math.Abs(aa.Ratio-1) > 1e-9 {
		t.Fatalf("A/A %+v", aa)
	}
}

// Same for the lifecycle numbers (README: claim to first exec 70 vs 232 ms,
// 3.3x, CI 3.1 to 3.5; A/A 1.01x): recompute from results/coldstart.json's
// raw samples.
func TestCommittedColdstartSummaryReproduces(t *testing.T) {
	b, err := os.ReadFile("../../results/coldstart.json")
	if err != nil {
		t.Skip("results/coldstart.json not present")
	}
	var f struct {
		Result struct {
			Samples map[string]*stageSamples `json:"samples"`
			Summary json.RawMessage          `json:"summary"`
			Ratio   json.RawMessage          `json:"median_ratio"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	got := summarizeStages(f.Result.Samples).(map[string]any)
	for _, k := range []string{"summary", "median_ratio"} {
		gb, _ := json.Marshal(got[k])
		var gm, wm any
		_ = json.Unmarshal(gb, &gm)
		want := f.Result.Summary
		if k == "median_ratio" {
			want = f.Result.Ratio
		}
		_ = json.Unmarshal(want, &wm)
		if !equalJSON(gm, wm) {
			t.Errorf("%s differs:\n%s\nvs committed\n%s", k, gb, want)
		}
	}
	r := got["median_ratio"].(map[string]map[string]stats.Ratio)["runsc/runc"]["claim_to_first_exec"]
	if math.Abs(r.Ratio-3.31) > 0.01 || math.Abs(r.Lo-3.09) > 0.01 || math.Abs(r.Hi-3.53) > 0.01 {
		t.Fatalf("claim to first exec ratio %+v; README says 3.3x, CI 3.1 to 3.5", r)
	}
}
