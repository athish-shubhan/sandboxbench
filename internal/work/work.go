// Package work defines the workload corpus: agent-shaped tasks (repo setup,
// dependency install, test run, compile) and syscall micro-benchmarks. Each
// task has an untimed setup and a timed run, both executed inside the
// sandbox with /work as a fresh host directory and /corpus mounted read-only.
package work

type Task struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"` // agent | micro
	Setup string `json:"setup,omitempty"`
	Run   string `json:"run"`
	Desc  string `json:"desc"`
}

const venv = "python3 -m venv /work/venv && /work/venv/bin/pip install -q --no-index --find-links /corpus/wheels "

func Agent() []Task {
	return []Task{
		{Name: "git_clone", Kind: "agent", Desc: "clone flask over the git protocol (pack transfer + checkout), then walk history",
			Run: "git clone -q file:///corpus/repos/flask.git flask && git -C flask checkout -q 3.1.0 && git -C flask log --oneline | wc -l"},
		{Name: "pip_install", Kind: "agent", Desc: "create a venv and install pytest, flask, requests, numpy from local wheels",
			Run: venv + "pytest more-itertools flask requests numpy && /work/venv/bin/python -c 'import numpy, flask, requests'"},
		{Name: "pytest", Kind: "agent", Desc: "run the more-itertools test suite (663 tests)",
			Setup: venv + "pytest && git clone -q /corpus/repos/more-itertools.git mi && git -C mi checkout -q v10.5.0",
			Run:   "cd mi && /work/venv/bin/python -m pytest -q -p no:cacheprovider tests | tail -1"},
		{Name: "compile_c", Kind: "agent", Desc: "build Lua 5.4 from source with make -j2 (many short gcc processes)",
			Setup: "cp -r /corpus/src/lua /work/lua",
			Run:   "make -s -C /work/lua -j2 MYCFLAGS=-std=c99 MYLIBS= CC=gcc >/dev/null 2>&1 || make -C /work/lua -j2 CC=gcc 2>&1 | tail -3; test -x /work/lua/lua && /work/lua/lua -v"},
		{Name: "python_imports", Kind: "agent", Desc: "20 fresh python3 processes importing common stdlib modules (code-exec job startup)",
			Run: "for i in $(seq 20); do python3 -c 'import json, asyncio, email.parser, http.client, sqlite3, xml.etree.ElementTree, decimal, unittest' || exit 1; done"},
	}
}

func Micro() []Task {
	m := "/corpus/bin/micro "
	return []Task{
		{Name: "getpid", Kind: "micro", Run: m + "getpid 1000000", Desc: "raw getpid syscall"},
		{Name: "stat_work", Kind: "micro", Run: m + "stat 100000 /work/f", Desc: "stat on the bind-mounted /work"},
		{Name: "stat_tmp", Kind: "micro", Run: m + "stat 100000 /tmp/f", Desc: "stat on the sandbox tmpfs"},
		{Name: "openclose_work", Kind: "micro", Run: m + "openclose 50000 /work/f", Desc: "open+close on /work"},
		{Name: "forkexec", Kind: "micro", Run: m + "forkexec 300", Desc: "fork + exec /bin/true + wait"},
		{Name: "pipe_rtt", Kind: "micro", Run: m + "pipe 50000", Desc: "1-byte pipe round trip between two processes"},
		{Name: "write_work", Kind: "micro", Run: m + "write 256 /work/big", Desc: "256 MiB sequential write + fsync on /work"},
		{Name: "read_work", Kind: "micro", Setup: m + "write 256 /work/big >/dev/null", Run: m + "read 256 /work/big", Desc: "256 MiB sequential read on /work (page cache warm)"},
	}
}

// Check is one isolation property, checked from inside the sandbox by a
// separate exec, so one check that takes the sandbox down cannot hide the
// others. Each prints a single JSON value.
type Check struct {
	Name string
	Py   string
}

func IsolationChecks() []Check {
	return []Check{
		{"cloud_metadata_169.254.169.254", `
import socket, json
s = socket.socket(); s.settimeout(1.5)
try:
    s.connect(("169.254.169.254", 80)); print(json.dumps("REACHABLE"))
except OSError as e:
    print(json.dumps("unreachable (%s)" % (e.strerror or e.__class__.__name__)))`},
		{"dns_lookup", `
import socket, json
try:
    socket.getaddrinfo("github.com", 443); print(json.dumps("RESOLVES"))
except OSError as e:
    print(json.dumps("fails (%s)" % e.__class__.__name__))`},
		{"network_interfaces", `
import os, json
print(json.dumps(sorted(os.listdir("/sys/class/net")) if os.path.isdir("/sys/class/net") else [l.split(":")[0].strip() for l in open("/proc/net/dev").read().split("\n")[2:] if l.strip()]))`},
		{"write_to_root", `
import os, json
try:
    open("/etc/sandbox-write-test", "w"); print(json.dumps("WRITABLE"))
except OSError as e:
    print(json.dumps("denied (%s)" % os.strerror(e.errno)))`},
		{"seccomp_mode", statusField("Seccomp")},
		{"cap_eff", statusField("CapEff")},
		{"no_new_privs", statusField("NoNewPrivs")},
		{"kernel_seen", `
import os, json; print(json.dumps(os.uname().release))`},
		{"pids_visible", `
import os, json; print(len([p for p in os.listdir("/proc") if p.isdigit()]))`},
		{"mount_tmpfs", `
import subprocess, json
p = subprocess.run(["mount", "-t", "tmpfs", "none", "/mnt"], capture_output=True, text=True)
print(json.dumps("ALLOWED" if p.returncode == 0 else "denied"))`},
		{"unshare_user_ns", `
import subprocess, json
p = subprocess.run(["unshare", "-U", "true"], capture_output=True, text=True)
print(json.dumps("ALLOWED" if p.returncode == 0 else "denied"))`},
		{"processes_started_of_200_with_pids_limit_64", `
import subprocess, json
kids, n = [], 0
try:
    for i in range(200):
        kids.append(subprocess.Popen(["sleep", "5"])); n += 1
except OSError:
    pass
for k in kids: k.kill()
for k in kids: k.wait()
print(n)`},
		{"allocate_512MiB_with_256MiB_limit", `
import subprocess, sys, json
p = subprocess.run([sys.executable, "-c", "b = bytearray(512 << 20); b[::4096] = b'x' * len(b[::4096]); print('allocated')"], capture_output=True, text=True)
print(json.dumps("allocated 512 MiB" if p.returncode == 0 else "process killed (exit %d), sandbox survived" % p.returncode))`},
	}
}

func statusField(f string) string {
	return `
import json
v = "absent"
for l in open("/proc/self/status"):
    if l.startswith("` + f + `:"):
        v = l.split(":", 1)[1].strip()
print(json.dumps(v))`
}
