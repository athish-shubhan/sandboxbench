#!/usr/bin/env bash
# The container root for both runtimes: a non-recursive, read-only bind of
# the host's root filesystem (Ubuntu 24.04 with python3.13, git, gcc, make).
# Non-recursive means none of the host's other mounts (/proc, /sys, /dev,
# fuse and attached volumes) are visible; each runtime mounts its own /proc,
# /dev, /sys and tmpfs /tmp from the OCI spec. Using the same tree for runc
# and runsc keeps file contents identical across runtimes.
set -euo pipefail
R=${1:-/var/lib/sandboxbench/rootfs}
# The bundle bind-mounts the corpus at /corpus and a per-sandbox directory at
# /work. On a read-only root the mount points must already exist, so create
# them (empty) on the host before the root is bound; runc otherwise fails
# with "mkdirat .../rootfs/corpus: read-only file system".
mkdir -p /corpus /work
mkdir -p "$R"
if ! mountpoint -q "$R"; then
  mount --bind / "$R"
  mount -o remount,bind,ro "$R"
fi
echo "$R"
