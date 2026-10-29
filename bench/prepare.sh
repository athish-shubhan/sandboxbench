#!/usr/bin/env bash
# Builds the workload corpus under corpus/ (not committed): two git repos as
# bare mirrors, a wheel directory for offline pip installs, the Lua 5.4 source
# tree, and the micro-benchmark binary. Needs network once; the benchmark
# itself runs offline (sandboxes have no network).
set -euo pipefail
cd "$(dirname "$0")/.."
C=corpus
mkdir -p $C/repos $C/wheels $C/src $C/bin
clone() { # url name ref
  [ -d $C/repos/$2.git ] || git clone -q --mirror "$1" $C/repos/$2.git
}
clone https://github.com/pallets/flask flask
clone https://github.com/more-itertools/more-itertools more-itertools
git --git-dir=$C/repos/flask.git rev-parse --verify -q refs/tags/3.1.0 >/dev/null
[ -d $C/src/lua ] || { git clone -q --depth 1 --branch v5.4.7 https://github.com/lua/lua $C/src/lua && rm -rf $C/src/lua/.git; }
CERT=${CERT:-}
[ -f /root/.ccr/ca-bundle.crt ] && CERT="--cert /root/.ccr/ca-bundle.crt"
python3 -m pip download -q $CERT -d $C/wheels --only-binary=:all: \
  pytest==8.3.4 more-itertools==10.5.0 flask==3.1.0 requests==2.32.3 numpy==2.2.1 setuptools wheel
gcc -O2 -static -o $C/bin/micro bench/micro/micro.c
du -sh $C/*
