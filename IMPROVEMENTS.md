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

## Round 3 (2026-11-05): runtime driver and workload corpus tested; architecture doc

Stacked on round 2 (`improve/round-2`).

### What changed

1. **Tests for the runtime driver (`internal/rt`) against a fake OCI runtime.** A shell script stands in for
   runc/runsc and records its arguments, so the exact command lines are pinned without root: global flags before
   the subcommand (`--root`, and `--platform=systrap --network=none` plus extra flags for runsc), create/start/
   pause/kill/delete, `exec --env ... --cwd ... <id> <cmd>`, and `run` followed by `delete --force`. Also: a
   failing lifecycle command's error includes the runtime's stderr; a workload's non-zero exit is returned as data
   (exit code 7, stdout, stderr), not as a harness error; a context deadline kills a hung exec and it is reported
   as a non-zero exit; `StatePid` parses `state` and returns 0 when the runtime is missing.
2. **Tests for the workload corpus (`internal/work`)**: task and check names are unique (they are keys in
   `results/*.json`), every task is complete, micro tasks run the micro-benchmark binary, and every Python
   isolation check parses (`ast.parse`); a syntax error would otherwise only show up as a failed check after a
   full run. No problem was found; these tests pin the corpus.
3. **`docs/architecture.md`**: a mermaid diagram from the experiments through the arm rotation, the OCI bundle,
   the CLI driver and the statistics to `results/`, and the pool simulator fed by the measured cold starts; a
   package table; what CI checks without root or runsc. Linked from the README. Rendered locally with
   `@mermaid-js/mermaid-cli` 11.4.2 without errors.

### How it was verified

```
$ go test -count=1 -v ./... | grep -E '^--- ' | awk '{print $2}' | sort | uniq -c    # round 2 -> this round
     19 PASS: -> 27 PASS:
$ go test -race -count=1 ./...      # all ok
$ golangci-lint run ./...           # 0 issues
```

Coverage: `internal/rt` 42.7% -> 94.9%, `internal/work` 0% -> 100%.

### Not verified

The driver is tested against a stand-in, not against runc or runsc themselves (no runsc build here; see round 1).

### Files touched

`internal/rt/cli_test.go` (new), `internal/work/work_test.go` (new), `docs/architecture.md` (new), `README.md`,
`IMPROVEMENTS.md`.

## Round 4 (2026-11-05): the runtime benchmark starts on a fresh machine; runc side re-measured; runc smoke test in CI

Stacked on round 3 (`improve/round-3`).

### What changed

1. **Bug: `bench/run_all.sh` failed on a fresh machine.** The container root is a read-only bind of the host's
   `/`, and the bundle bind-mounts the corpus at `/corpus` and a work directory at `/work`. On a read-only root the
   mount points must already exist, and nothing created them, so the first `runc create` failed:

   ```
   runc create failed: ... error mounting "/home/user/sandboxbench/corpus" to rootfs at "/corpus":
   create mountpoint for /corpus mount: make mountpoint "/corpus": mkdirat /var/lib/sandboxbench/rootfs/corpus:
   read-only file system
   ```

   `bench/rootfs.sh` now creates the two empty directories on the host before binding the root.
2. **runc side of the README re-measured** (same VM type, runc 1.5.2 as in `results/`, corpus from
   `bench/prepare.sh`). gVisor could not be built here (see below), so all three arms were runc, via
   `bench/runc-as-runsc.sh`, which drops runsc's `--platform`/`--network` flags; only the runc and A/A columns are
   meaningful:

   | | committed | re-run |
   |---|---|---|
   | cold start: claim to first exec, runc p50 (40 reps) | 70.2 ms | 45.9 ms |
   | teardown p50 / one-shot `run` p50 | 115.8 / 35.0 ms | 113.7 / 37.1 ms |
   | A/A claim-to-first-exec ratio | 1.008 [0.918, 1.087] | 0.985 [0.898, 1.121] |
   | runc p50: git clone, pip install, pytest, Lua build, python imports (8 reps) | 1.39, 5.45, 2.93, 3.80, 2.16 s | 1.25, 7.11, 2.45, 3.30, 1.87 s |
   | runc p50: getpid, fork+exec | 146 ns, 1.33 ms | 128 ns, 1.06 ms |
   | A/A intervals containing 0 (13 tasks) | 12 of 13 | 13 of 13 |

   The A/A noise floor behaves as the method intends; absolute runc times move by up to about 25% (pip install
   slower, the rest faster), so the absolute columns in the README are this machine's on that day. Failed reps: 0.
3. **CI drives real runc.** A `harness-runc` job builds the harness, binds the root with `bench/rootfs.sh` and
   runs a 3-rep cold start with runc in every arm, so the bundle, the CLI driver and the results file are tested end
   to end on each PR (not the README's numbers).

### How it was verified

```
$ bench/prepare.sh                                  # corpus: 41M repos, 19M wheels, Lua source, micro binary
$ bench/rootfs.sh
$ go build -o /tmp/sbb ./cmd/sandboxbench
$ /tmp/sbb coldstart -runsc "$PWD/bench/runc-as-runsc.sh" -out /tmp/sb-runc     # 40 reps
$ /tmp/sbb workloads -runsc "$PWD/bench/runc-as-runsc.sh" -out /tmp/sb-runc     # 8 reps, 605 s, 0 failed reps
```

Before the `rootfs.sh` fix the first command failed with the error above. `go test ./...` and `go vet` unchanged
(no Go code changed); the new CI job's result is on this PR.

### Not verified

**gVisor.** Building `runsc` from gVisor's `go` branch at the README's commit `ace9117e` was not allowed in this
session (compiling and running an externally fetched container runtime needs the owner's approval), so the
runsc columns, the runsc/runc ratios, the isolation, memory and snapshot results remain un-re-measured. With a
`runsc` binary at `/usr/local/bin/runsc`, `bench/run_all.sh` should now run end to end.

### Files touched

`bench/rootfs.sh`, `bench/runc-as-runsc.sh` (new), `.github/workflows/ci.yml`, `IMPROVEMENTS.md`.
