# SandboxBench method

## What is compared

Two OCI runtimes driven the way a containerd shim drives them, through their CLIs (`create`, `start`, `exec`, `kill`, `delete`, `run`):

- **runc** 1.5 (namespaces, cgroups, seccomp: the host kernel serves every syscall);
- **runsc** (gVisor) with the **systrap** platform: application syscalls are trapped (seccomp `SECCOMP_RET_TRAP` plus a stub process per address space) and served by gVisor's user-space kernel, the Sentry. Files outside the sandbox go through the gofer or, with directfs, through host file descriptors the Sentry opens under a strict host seccomp filter.

Kata Containers and Firecracker need KVM; this VM has no `/dev/kvm` (it is itself a Firecracker guest without nested virtualisation), so microVM runtimes are not measured. The harness takes any OCI runtime binary, so adding Kata on a KVM host is a flag.

## Same bundle for both

`internal/oci` writes one `config.json` used unchanged by both runtimes:

- root: a **read-only, non-recursive bind** of the host root filesystem (`bench/rootfs.sh`). Both runtimes see byte-identical files (Ubuntu 24.04, Python 3.13, git, gcc, make). Non-recursive means none of the host's other mounts are visible.
- mounts: fresh `/proc`, `/dev`, `/dev/pts`, `/dev/shm`, `/sys` (ro), tmpfs `/tmp` and `/run`, `/work` bind-mounted read-write from a fresh host directory per sandbox, `/corpus` bind-mounted read-only.
- namespaces: pid, network (no interfaces except lo), ipc, uts, mount.
- Docker's default capability set minus `NET_RAW`, `noNewPrivileges`, and a seccomp deny list (keyring, module and kexec loading, mount, namespaces, BPF, perf, ptrace).

## Noise control

- **Interleaving with rotation.** Each repetition runs every arm once, and the order rotates every repetition, so no runtime always runs first (cold caches, background work, thermal state).
- **A/A arm.** A second runc arm with its own state directory and an identical bundle runs in every repetition. Its distance from runc is the noise floor; a runsc effect inside the A/A interval is not evidence of anything. This is the answer to the problem kubernetes-sigs/agent-sandbox#403 ran into: fixed thresholds cannot tell noise from regressions, a measured noise floor can.
- **Paired bootstrap.** Ratios of medians come with 95% percentile-bootstrap intervals that resample repetitions (pairs), which removes slow host drift shared by both arms in a repetition.
- **Fresh state.** Every workload repetition gets a new sandbox and an empty `/work`; untimed setup (creating a venv, cloning the repo under test) runs inside the sandbox before the timed command.
- **Warm-up.** One untimed lifecycle per runtime before the cold-start measurement, so binaries are in the page cache.

## Workloads

Agent-shaped tasks, each a shell command run with `exec` inside the sandbox:

| task | why |
|---|---|
| git_clone | repo setup: pack transfer over the git protocol from a local bare mirror, checkout of ~250 files, history walk |
| pip_install | dependency install: venv creation plus five wheels including numpy (thousands of small files) |
| pytest | test run: 663 tests of more-itertools (Python imports, stat-heavy) |
| compile_c | build: Lua 5.4 with `make -j2` (many short gcc processes) |
| python_imports | code-execution jobs: 20 fresh interpreters importing common modules |

The corpus is offline (`bench/prepare.sh` fetches it once), so no sandbox needs a network and no run depends on PyPI or GitHub latency.

Micro-benchmarks (`bench/micro/micro.c`, static binary): raw `getpid` (pure syscall cost), `stat` and `open`/`close` on `/work` (bind mount from the host) and on the sandbox's tmpfs, fork+exec+wait, a pipe round trip between two processes (context switches), and sequential write/read of 256 MiB on `/work`.

## Lifecycle stages

`create` (namespaces, cgroups, rootfs setup; for runsc also starting the Sentry and gofer), `start`, first `exec`, teardown (`kill` + `delete`), and the one-shot `run /bin/true`. Claim to first exec = create + start + first exec: what an agent waits for between asking for a sandbox and running its first command, excluding Kubernetes scheduling and image pulls (modelled separately in the warm-pool simulation).

## Memory

16 idle sandboxes at once; per-sandbox proportional set size (PSS) summed over every host process of the sandbox (found from `runtime state` pids, process command lines mentioning the runtime's state root, and their descendants), plus the drop in `MemAvailable` after dropping caches. Idle density is an upper bound, not a capacity plan.

## Isolation checks

Each check is a separate `exec`, so a check that kills the sandbox is recorded as such and does not hide the others. They are properties, not exploits: metadata endpoint reachability, DNS, interfaces, writes to the root, seccomp mode, effective capabilities, no-new-privs, the kernel version the workload sees, visible pids, mount, user namespaces, and what happens at the pids and memory limits.

## Warm state (snapshot experiment)

The sandbox runs a "warm executor" as its init process: it imports json, sqlite3, numpy, pandas and matplotlib, writes `/work/ready`, then rewrites `/work/hb` with an increasing counter every 5 ms. The host watches `/work` through the bind mount.

- Cold: create + start until `/work/ready` appears (the import time measured inside is recorded too).
- Checkpoint (runsc only): `runsc checkpoint` of the warm sandbox into an image directory; the image size is the directory's size.
- Restore (runsc only): `runsc restore --detach` into a new container from that image, until the heartbeat counter changes. This is time until the restored process runs again, not until its working set has been touched.
- Pause / resume: `pause` and `resume` on a warm sandbox (cgroup freezer for runc).

runc's checkpoint needs CRIU, which was not installable here, so runc has no checkpoint or restore arm. 10 repetitions per runtime.

## Warm-pool simulation

`controller/` is a discrete-event simulator, not a Kubernetes controller. It models:

- claims from three sources with different shapes: interactive sessions (diurnal Poisson, open loop), eval suites (a step of 100 claims at launch, then closed loop as tasks finish), and RL steps (a batch of 128 claims at once, the next step after all rollouts and a training phase);
- sandbox creation as a lognormal scheduling delay (median 2 s) plus a measured runsc cold start, with at most 32 creations in flight (bursts queue);
- policies acting every 5 to 15 s on what a controller could observe (claim times, idle and in-flight counts, optional launch hints).

Policies: static pool; the upstream HPA example (claim rate, min 10, max 100, 15 s sync, 300 s scale-down stabilisation) and the same without the max; KEDA-style scale to zero; and a predictive newsvendor policy that learns a per-minute-of-day demand quantile from a week of history, detects periodic batch claimers, and optionally listens to launch hints from the eval scheduler. Every policy is scored on the same second week of claims.

## Threats to validity

- One 2-vCPU VM without KVM: absolute times are slow and noisy; the A/A arm says how noisy. Ratios between runtimes on the same host are the meaningful output.
- The rootfs is a bind of the host root, not an image with an overlay; image-pull and overlay costs are not measured. runsc's rootfs overlay (`--overlay2`) default applies.
- cgroups v1 on this host.
- The warm-pool workload is synthetic, with shapes taken from how eval and RL jobs claim sandboxes, not from a production trace.
