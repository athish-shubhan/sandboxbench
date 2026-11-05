# Architecture

How the harness and the pool simulator are put together. How noise is controlled and what each number means is
in [`METHOD.md`](METHOD.md).

```mermaid
flowchart LR
    subgraph harness ["cmd/sandboxbench"]
        EXP["experiments<br/>coldstart, exec, workloads,<br/>memory, isolation, snapshot"]
        ORD["bench.order(rep)<br/>arms rotate each rep:<br/>runc, runc-aa, runsc"]
        EXP --> ORD
    end
    ORD --> OCI["internal/oci<br/>one config.json for every arm"]
    ORD --> RT["internal/rt<br/>runc / runsc CLI:<br/>create, start, exec, kill, delete,<br/>pause, checkpoint, restore"]
    WORK["internal/work<br/>agent tasks, syscall micro-benchmarks,<br/>isolation checks (Python)"] --> EXP
    RT -->|wall times, exit codes, output| SAMP["raw samples per arm and rep"]
    RT -->|host PIDs, PSS| SAMP
    SAMP --> STATS["internal/stats<br/>percentiles, paired bootstrap CI<br/>of median ratios (fixed seeds)"]
    STATS --> RES["results/*.json<br/>samples + summary"]
    RES --> PLOT["bench/plot.py<br/>SUMMARY.md, figures"]

    RES -->|coldstart samples| POOL["cmd/poolsim + controller<br/>two simulated weeks of claims"]
    POOL --> POL["policies: static, HPA, KEDA,<br/>predictive with or without hints"]
    POL --> PJ["results/pool.jsonl"]
```

## Packages

| package | role |
|---|---|
| `cmd/sandboxbench` | runs each experiment over all arms with the arm order rotated every repetition, writes `results/<experiment>.json` with raw samples and the summary |
| `internal/oci` | builds the single OCI bundle (read-only root, no network, seccomp deny list, optional memory and pids limits) both runtimes run |
| `internal/rt` | drives a runtime through its CLI, the way a containerd shim would, and times each stage; finds a runtime's host processes for memory accounting |
| `internal/work` | the workload corpus and the isolation checks |
| `internal/stats` | percentiles and paired percentile-bootstrap intervals for ratios of medians, seeded so a re-run of the summary is identical |
| `controller` | the warm-pool simulator: claim workload (interactive, eval suites, RL steps), infrastructure model fed by measured cold starts, policies |
| `cmd/poolsim` | runs every policy over the simulated weeks and writes `results/pool.jsonl` |

## What CI checks without root or runsc

- unit tests for every package, including the CLI driver against a fake runtime script;
- the workload and cold-start summaries recomputed from the committed raw samples equal the committed summaries;
- the pool simulation reproduces `results/pool.jsonl` byte for byte.
