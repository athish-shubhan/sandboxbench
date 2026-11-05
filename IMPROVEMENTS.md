# Improvements log

## Round 1 (2026-11-05): tests for stats, oci and rt; results-file and process-accounting fixes; CI

### What changed

1. **Tests for the statistics behind every headline number.** `internal/stats` (percentiles, paired bootstrap CIs
   for ratios of medians) had no tests. New tests check percentiles against numpy's linear interpolation,
   `Summarize`, that pairing removes host drift (paired CI at least 3x narrower than unpaired under a 2x drift, both
   covering the true ratio), and **pin `MedianRatio`'s output for a fixed seed** to the values main's code
   produces, so a refactor cannot silently change the published intervals.
2. **Bug: one empty arm could lose a whole experiment's results file.** `Summarize` of an empty sample returned
   `min = +Inf`, `max = -Inf` and NaN percentiles, and `MedianRatio` returned NaN or +Inf when a sample was empty
   or a median was 0. `encoding/json` rejects those values, and `main` ignored the error from `json.MarshalIndent`,
   so `results/<experiment>.json` would be written **empty** after the run (up to 25 minutes). `Summary` and
   `Ratio` now encode non-finite values as `null` (finite values encode as before; a test pins the bytes), empty
   samples give NaN rather than ±Inf, `Percentile` clamps `p` to [0, 100], and `main` stops with an error instead
   of writing an empty file if encoding ever fails.
3. **Bug: the runc arm's process accounting matched the A/A arm.** `rt.Pids` picked processes whose command line
   contained the state root as a substring. The runc root `.../runc` is a prefix of the A/A root `.../runc-aa`, so
   any live `runc-aa` process would be counted as runc's (and PSS added to runc's per-sandbox memory). The match
   now requires the root to be a whole path or a parent path of an argument. In a normal `all` run the memory
   experiment only starts runc and runsc sandboxes, one arm at a time, so the committed `memory.json` should not be
   affected; leftovers from an interrupted run would have been.
4. **Bug: bind mounts were written in random order.** `oci.New` iterated a map, so `config.json` differed from
   run to run, and a bind nested under another could be mounted before its parent. Binds are now sorted. The
   harness passes one bind (`/corpus`), so published results are not affected.
5. **Error handling in the harness.** A micro-benchmark rep whose output has no `ns_per_op` is now a failed rep
   (it was recorded as 0 ns and pulled the median down); warm-up failures stop the experiment instead of being
   ignored; snapshot teardown, delete and pre-checkpoint heartbeat errors are recorded in `errors`; failed
   `drop_caches` is logged; `poolsim` checks write and close errors. One unused function removed.
6. **CI** (`.github/workflows/ci.yml`): gofmt, `go vet`, `go test -race`, a check that the warm-pool simulation
   reproduces `results/pool.jsonl` byte for byte, and golangci-lint v2.5.0 (`.golangci.yml`).
7. **README**: a Quick start that runs from a clean clone without root, runc or runsc.

### Why it matters

The README's claims are ratios with bootstrap CIs; the code computing them is now tested and pinned. The two
accounting bugs (A/A processes counted as runc's, nondeterministic bundles) are the kind that quietly bias a
benchmark that is about controlling noise.

### How it was verified

```
$ go test -count=1 -v ./... | grep -E '^--- ' | awk '{print $2}' | sort | uniq -c    # main
      5 PASS:
$ go test -count=1 -v ./... | grep -E '^--- ' | awk '{print $2}' | sort | uniq -c    # this branch
     16 PASS:
$ go test -race -count=1 ./...          # all packages ok
$ golangci-lint run ./...               # main: 29 issues; branch: 0 issues
$ go vet ./... && gofmt -l .            # clean
```

Coverage (`go test -cover ./...`): `internal/stats` 0% -> 100%, `internal/oci` 0% -> 92.0%, `internal/rt` 0% -> 42.7%,
`controller` 93.0% -> 95.5% (an unused function removed).

On main's code:

```
json.MarshalIndent(stats.Summarize(nil))                      -> json: unsupported value: NaN
json.MarshalIndent(stats.MedianRatio([0 0], [1 1], true, 1))  -> json: unsupported value: +Inf
TestPidsSeparatesArmsWithSharedPrefix: runc arm counted the runc-aa arm's process 24537: [24537 24538]
TestNewWorkLimitsAndBinds: bind order [/opt/corpus /a /z /opt/corpus/sub], want [/a /opt/corpus /opt/corpus/sub /z]
TestWriteIsDeterministic: run 1 wrote a different config.json
```

Warm-pool simulation (section 7 of the README), before and after the changes:

```
$ go run ./cmd/poolsim -coldstart results/coldstart.json -out /tmp/pool.jsonl   # 34 s
$ cmp results/pool.jsonl /tmp/pool.jsonl && echo IDENTICAL
IDENTICAL
```

### Not verified

- The runtime measurements (README sections 1 to 6: workload overheads, lifecycle, syscalls, memory, snapshot,
  isolation) were not re-run: this VM has runc but no runsc build, and the harness needs both. No README number
  was changed. The bootstrap code that computed their CIs is unchanged (pinned by `TestMedianRatioReproducible`).

### Files touched

`internal/stats/stats.go`, `internal/stats/stats_test.go` (new), `internal/rt/rt.go`, `internal/rt/rt_test.go` (new),
`internal/oci/oci.go`, `internal/oci/oci_test.go` (new), `cmd/sandboxbench/main.go`, `cmd/sandboxbench/snapshot.go`,
`cmd/poolsim/main.go`, `controller/policy.go`, `.github/workflows/ci.yml` (new), `.golangci.yml` (new), `README.md`,
`IMPROVEMENTS.md` (new).

## Round 2 (2026-11-05): README tables recomputed from the committed raw samples; ratios paired by repetition

Stacked on round 1 (`improve/round-1`).

### What changed

1. **The README's workload and lifecycle numbers are now checked in CI.** `results/workloads.json` and
   `results/coldstart.json` contain every raw sample next to the summary. Two new tests recompute the summaries
   from the raw samples with the harness's own code and compare them with the committed summaries: every
   median, percentile, ratio and bootstrap interval matches (relative tolerance 1e-12). So the README's
   workload table (for example git clone +150% [+142, +162], A/A -8% [-10, +4]) and the lifecycle ratio
   (claim to first exec 3.3x, CI 3.1 to 3.5) are pinned to the data they came from. This needs no runc or runsc.
2. **Bug: workload ratios could pair different repetitions.** `summarizeWork` used a paired bootstrap whenever
   the arms had the same number of successful reps, and paired by position. If runc failed rep 0 and runsc failed
   rep 7, position i was rep i+1 for runc and rep i for runsc. Ratios are now paired by repetition over the reps
   both arms completed. On a constructed case where every rep's true ratio is 2, the old code reported
   1.6 [0.67, 3.33] and the new code 2 [2, 2]. The committed run had no failed reps (312 samples, 0 failures),
   so its numbers are unchanged, as the test in item 1 shows.

### How it was verified

```
$ go test -count=1 -v ./cmd/sandboxbench
--- PASS: TestCommittedWorkloadSummaryReproduces
--- PASS: TestSummarizePairsByRep
--- PASS: TestCommittedColdstartSummaryReproduces
# on round 1's summarizeWork:
summary_test.go:92: paired by rep, every pair has ratio 2 exactly: {Ratio:1.6 Lo:0.6666666666666666 Hi:3.3333333333333335}
```

Recomputed agent-workload rows (runc p50, runsc p50, runsc vs runc, A/A), identical to the README table:

```
git_clone       1.39 s  3.47 s  +150% [+142, +162]  A/A -8% [-10, +4]
pip_install     5.45 s  9.75 s   +79% [+71, +89]    A/A +2% [-3, +9]
pytest          2.93 s  4.05 s   +38% [+33, +45]    A/A +1% [-5, +5]
compile_c       3.80 s  9.67 s  +155% [+142, +161]  A/A +6% [-1, +10]
python_imports  2.16 s  4.30 s   +99% [+83, +112]   A/A -2% [-11, +4]
```

```
$ go test -count=1 -v ./... | grep -E '^--- ' | awk '{print $2}' | sort | uniq -c    # round 1 -> this round
     16 PASS: -> 19 PASS:
$ go test -race -count=1 ./... ; golangci-lint run ./...    # all ok; 0 issues
```

`cmd/sandboxbench` coverage 0% -> 11.5% (the rest drives runc and runsc).

### Not verified

The raw samples themselves were not re-measured (needs runsc, see round 1). The tests show the published
summaries follow from the published samples, not that a new run would give the same samples.

### Files touched

`cmd/sandboxbench/main.go`, `cmd/sandboxbench/summary_test.go` (new), `IMPROVEMENTS.md`.
