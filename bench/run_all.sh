#!/usr/bin/env bash
# Reproduces results/: runtime measurements (about 25 minutes on 2 vCPUs),
# then the warm-pool simulation fed with the measured runsc cold starts.
# Needs root, runc, runsc (gVisor) and a populated corpus (bench/prepare.sh).
set -euo pipefail
cd "$(dirname "$0")/.."
bench/rootfs.sh >/dev/null
go build -o bin/sandboxbench ./cmd/sandboxbench
go build -o bin/poolsim ./cmd/poolsim
mkdir -p results
./bin/sandboxbench all -out results 2>&1 | tee results/run.log
./bin/sandboxbench snapshot -out results 2>&1 | tee -a results/run.log
./bin/poolsim -coldstart results/coldstart.json -out results/pool.jsonl > results/pool.log
python3 bench/plot.py results
