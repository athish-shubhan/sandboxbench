// Command sandboxbench measures agent-sandbox runtimes (runc, gVisor runsc)
// on the same OCI bundle: lifecycle latency, exec round trip, agent-shaped
// workloads, syscall micro-benchmarks, memory per sandbox, and isolation
// properties. Every comparison carries an A/A arm (a second, identical runc)
// so the noise floor is measured, not assumed.
//
//	sandboxbench coldstart|exec|workloads|memory|isolation|all [flags]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"sandboxbench/internal/oci"
	"sandboxbench/internal/rt"
	"sandboxbench/internal/stats"
	"sandboxbench/internal/work"
)

var (
	fs       = flag.NewFlagSet("sandboxbench", flag.ExitOnError)
	runcBin  = fs.String("runc", "/usr/bin/runc", "runc binary")
	runscBin = fs.String("runsc", "/usr/local/bin/runsc", "runsc binary")
	rootfs   = fs.String("rootfs", "/var/lib/sandboxbench/rootfs", "container root (bench/rootfs.sh)")
	corpus   = fs.String("corpus", "corpus", "workload corpus (bench/prepare.sh)")
	state    = fs.String("state", "/run/sandboxbench", "runtime state directories")
	workDir  = fs.String("workdir", "/var/tmp/sbwork", "per-sandbox /work directories")
	outDir   = fs.String("out", "results", "output directory")
	reps     = fs.Int("reps", 0, "repetitions (0 = per-experiment default)")
	only     = fs.String("tasks", "", "comma-separated task names (workloads)")
	nMem     = fs.Int("n", 16, "sandboxes started at once (memory)")
)

type bench struct {
	arms   []rt.Runtime // [runc, runc-aa, runsc]
	corpus string
	seq    int
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: sandboxbench coldstart|exec|workloads|memory|isolation|snapshot|all [flags]")
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs.Parse(os.Args[2:])
	c, err := filepath.Abs(*corpus)
	if err != nil {
		log.Fatal(err)
	}
	b := &bench{corpus: c}
	runc := rt.Runc(*runcBin, filepath.Join(*state, "runc"))
	aa := rt.Runc(*runcBin, filepath.Join(*state, "runc-aa"))
	aa.Name = "runc-aa"
	b.arms = []rt.Runtime{runc, aa, rt.Runsc(*runscBin, filepath.Join(*state, "runsc"))}
	for _, a := range b.arms {
		os.MkdirAll(a.Root, 0o755)
	}
	os.MkdirAll(*workDir, 0o755)
	os.MkdirAll(*outDir, 0o755)
	run := map[string]func() (any, error){
		"coldstart": func() (any, error) { return b.coldstart(pick(*reps, 40)) },
		"exec":      func() (any, error) { return b.execLatency(pick(*reps, 100)) },
		"workloads": func() (any, error) { return b.workloads(pick(*reps, 8)) },
		"memory":    func() (any, error) { return b.memory(*nMem) },
		"isolation": func() (any, error) { return b.isolation() },
		"snapshot":  func() (any, error) { return b.snapshot(pick(*reps, 10)) },
	}
	names := []string{cmd}
	if cmd == "all" {
		names = []string{"isolation", "coldstart", "exec", "memory", "workloads"}
	}
	for _, n := range names {
		f, ok := run[n]
		if !ok {
			log.Fatalf("unknown command %q", n)
		}
		log.Printf("== %s", n)
		t := time.Now()
		res, err := f()
		if err != nil {
			log.Fatalf("%s: %v", n, err)
		}
		out := map[string]any{"experiment": n, "env": environment(b.arms), "elapsed_sec": time.Since(t).Seconds(), "result": res}
		bb, _ := json.MarshalIndent(out, "", "  ")
		if err := os.WriteFile(filepath.Join(*outDir, n+".json"), bb, 0o644); err != nil {
			log.Fatal(err)
		}
		log.Printf("   %s done in %.0fs", n, time.Since(t).Seconds())
	}
}

func pick(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// sandbox creates and starts a container running `sleep infinity`.
type sandbox struct {
	r      rt.Runtime
	id     string
	work   string
	bundle string
}

func (b *bench) newID(r rt.Runtime) string {
	b.seq++
	return fmt.Sprintf("sb-%s-%d-%d", r.Name, os.Getpid(), b.seq)
}

func (b *bench) bundle(r rt.Runtime, id string, withWork bool, mem, pids int64, args ...string) (string, string, error) {
	bundle := filepath.Join(*state, "bundles", id)
	o := oci.Options{Args: args, Rootfs: *rootfs, MemLimit: mem, PidsLimit: pids,
		Binds: map[string]string{"/corpus": b.corpus}}
	wd := ""
	if withWork {
		wd = filepath.Join(*workDir, id)
		if err := os.MkdirAll(wd, 0o755); err != nil {
			return "", "", err
		}
		o.Work = wd
	}
	return bundle, wd, oci.Write(bundle, oci.New(o))
}

func (b *bench) start(ctx context.Context, r rt.Runtime, withWork bool, mem, pids int64) (*sandbox, time.Duration, time.Duration, error) {
	id := b.newID(r)
	bundle, wd, err := b.bundle(r, id, withWork, mem, pids, "sleep", "infinity")
	if err != nil {
		return nil, 0, 0, err
	}
	s := &sandbox{r: r, id: id, work: wd, bundle: bundle}
	dc, err := r.Create(ctx, id, bundle)
	if err != nil {
		s.stop()
		return nil, 0, 0, err
	}
	ds, err := r.Start(ctx, id)
	if err != nil {
		s.stop()
		return nil, 0, 0, err
	}
	return s, dc, ds, nil
}

func (s *sandbox) stop() time.Duration {
	d, _ := s.r.Teardown(context.Background(), s.id)
	os.RemoveAll(s.bundle)
	if s.work != "" {
		os.RemoveAll(s.work)
	}
	return d
}

func (s *sandbox) sh(ctx context.Context, script string) (rt.ExecResult, error) {
	cwd := ""
	if s.work != "" {
		cwd = "/work"
	}
	return s.r.Exec(ctx, s.id, nil, cwd, "sh", "-c", script)
}

// order rotates arms per rep so no runtime always runs first (warm caches,
// thermal state, background work drift).
func (b *bench) order(rep int) []rt.Runtime {
	n := len(b.arms)
	out := make([]rt.Runtime, n)
	for i := range out {
		out[i] = b.arms[(i+rep)%n]
	}
	return out
}

func ms(d time.Duration) float64 { return float64(d) / 1e6 }

// ---- cold start

type stageSamples struct {
	CreateMs    []float64 `json:"create_ms"`
	StartMs     []float64 `json:"start_ms"`
	FirstExecMs []float64 `json:"first_exec_ms"`
	ReadyMs     []float64 `json:"claim_to_first_exec_ms"`
	TeardownMs  []float64 `json:"teardown_ms"`
	OneShotMs   []float64 `json:"run_true_oneshot_ms"`
}

func (b *bench) coldstart(n int) (any, error) {
	ctx := context.Background()
	S := map[string]*stageSamples{}
	for _, a := range b.arms {
		S[a.Name] = &stageSamples{}
	}
	// warm-up: first runs pay for page-cache misses on the runtime binaries
	for _, a := range b.arms {
		if s, _, _, err := b.start(ctx, a, false, 0, 0); err == nil {
			s.sh(ctx, "true")
			s.stop()
		} else {
			return nil, err
		}
	}
	for rep := 0; rep < n; rep++ {
		for _, a := range b.order(rep) {
			s, dc, ds, err := b.start(ctx, a, false, 0, 0)
			if err != nil {
				return nil, err
			}
			e, err := s.r.Exec(ctx, s.id, nil, "", "/bin/true")
			if err != nil || e.Code != 0 {
				s.stop()
				return nil, fmt.Errorf("%s first exec: %v %s", a.Name, err, e.Stderr)
			}
			dt := s.stop()
			x := S[a.Name]
			x.CreateMs = append(x.CreateMs, ms(dc))
			x.StartMs = append(x.StartMs, ms(ds))
			x.FirstExecMs = append(x.FirstExecMs, ms(e.Wall))
			x.ReadyMs = append(x.ReadyMs, ms(dc+ds+e.Wall))
			x.TeardownMs = append(x.TeardownMs, ms(dt))
			id := b.newID(a)
			bundle, _, err := b.bundle(a, id, false, 0, 0, "/bin/true")
			if err != nil {
				return nil, err
			}
			r, err := a.Run(ctx, id, bundle)
			os.RemoveAll(bundle)
			if err != nil || r.Code != 0 {
				return nil, fmt.Errorf("%s run: %v %s", a.Name, err, r.Stderr)
			}
			x.OneShotMs = append(x.OneShotMs, ms(r.Wall))
		}
		if rep%10 == 9 {
			log.Printf("   coldstart %d/%d", rep+1, n)
		}
	}
	return summarizeStages(S), nil
}

func summarizeStages(S map[string]*stageSamples) any {
	out := map[string]any{"samples": S}
	sum := map[string]map[string]stats.Summary{}
	cmp := map[string]map[string]stats.Ratio{}
	for name, x := range S {
		sum[name] = map[string]stats.Summary{"create_ms": stats.Summarize(x.CreateMs), "start_ms": stats.Summarize(x.StartMs),
			"first_exec_ms": stats.Summarize(x.FirstExecMs), "claim_to_first_exec_ms": stats.Summarize(x.ReadyMs),
			"teardown_ms": stats.Summarize(x.TeardownMs), "run_true_oneshot_ms": stats.Summarize(x.OneShotMs)}
	}
	base := S["runc"]
	for _, other := range []string{"runc-aa", "runsc"} {
		o := S[other]
		cmp[other+"/runc"] = map[string]stats.Ratio{
			"claim_to_first_exec": stats.MedianRatio(base.ReadyMs, o.ReadyMs, true, 1),
			"run_true_oneshot":    stats.MedianRatio(base.OneShotMs, o.OneShotMs, true, 2),
			"teardown":            stats.MedianRatio(base.TeardownMs, o.TeardownMs, true, 3)}
	}
	out["summary"], out["median_ratio"] = sum, cmp
	return out
}

// ---- exec round trip in a running sandbox

func (b *bench) execLatency(n int) (any, error) {
	ctx := context.Background()
	type res struct {
		TrueMs   []float64 `json:"exec_true_ms"`
		PythonMs []float64 `json:"exec_python_ms"`
	}
	R := map[string]*res{}
	var boxes []*sandbox
	for _, a := range b.arms {
		s, _, _, err := b.start(ctx, a, false, 0, 0)
		if err != nil {
			return nil, err
		}
		boxes = append(boxes, s)
		R[a.Name] = &res{}
		s.r.Exec(ctx, s.id, nil, "", "/bin/true")
	}
	defer func() {
		for _, s := range boxes {
			s.stop()
		}
	}()
	for i := 0; i < n; i++ {
		for j := range boxes {
			s := boxes[(i+j)%len(boxes)]
			e, err := s.r.Exec(ctx, s.id, nil, "", "/bin/true")
			if err != nil {
				return nil, err
			}
			R[s.r.Name].TrueMs = append(R[s.r.Name].TrueMs, ms(e.Wall))
			if i%3 == 0 {
				e, err = s.r.Exec(ctx, s.id, nil, "", "python3", "-c", "pass")
				if err != nil {
					return nil, err
				}
				R[s.r.Name].PythonMs = append(R[s.r.Name].PythonMs, ms(e.Wall))
			}
		}
	}
	sum := map[string]any{}
	for k, v := range R {
		sum[k] = map[string]stats.Summary{"exec_true_ms": stats.Summarize(v.TrueMs), "exec_python_ms": stats.Summarize(v.PythonMs)}
	}
	cmp := map[string]any{}
	for _, o := range []string{"runc-aa", "runsc"} {
		cmp[o+"/runc"] = map[string]stats.Ratio{"exec_true": stats.MedianRatio(R["runc"].TrueMs, R[o].TrueMs, true, 4),
			"exec_python": stats.MedianRatio(R["runc"].PythonMs, R[o].PythonMs, true, 5)}
	}
	return map[string]any{"samples": R, "summary": sum, "median_ratio": cmp}, nil
}

// ---- workloads

type Sample struct {
	Arm     string  `json:"arm"`
	Task    string  `json:"task"`
	Rep     int     `json:"rep"`
	WallSec float64 `json:"wall_sec"`
	NsPerOp float64 `json:"ns_per_op,omitempty"`
	Code    int     `json:"exit_code"`
	Tail    string  `json:"output_tail"`
}

func (b *bench) workloads(n int) (any, error) {
	ctx := context.Background()
	tasks := append(work.Agent(), work.Micro()...)
	if *only != "" {
		keep := map[string]bool{}
		for _, t := range strings.Split(*only, ",") {
			keep[t] = true
		}
		var f []work.Task
		for _, t := range tasks {
			if keep[t.Name] {
				f = append(f, t)
			}
		}
		tasks = f
	}
	var samples []Sample
	for _, t := range tasks {
		for rep := 0; rep < n; rep++ {
			for _, a := range b.order(rep) {
				s, _, _, err := b.start(ctx, a, true, 0, 0)
				if err != nil {
					return nil, err
				}
				if t.Setup != "" {
					if e, err := s.sh(ctx, t.Setup); err != nil || e.Code != 0 {
						s.stop()
						return nil, fmt.Errorf("%s/%s setup: %v exit %d: %s", a.Name, t.Name, err, e.Code, tail(e.Stderr+e.Stdout, 400))
					}
				}
				e, err := s.sh(ctx, t.Run)
				s.stop()
				if err != nil {
					return nil, err
				}
				smp := Sample{Arm: a.Name, Task: t.Name, Rep: rep, WallSec: e.Wall.Seconds(), Code: e.Code, Tail: tail(e.Stdout+e.Stderr, 200)}
				if t.Kind == "micro" {
					var m struct {
						Ns float64 `json:"ns_per_op"`
					}
					json.Unmarshal([]byte(strings.TrimSpace(e.Stdout)), &m)
					smp.NsPerOp = m.Ns
				}
				if e.Code != 0 {
					log.Printf("   %s/%s rep %d exit %d: %s", a.Name, t.Name, rep, e.Code, smp.Tail)
				}
				samples = append(samples, smp)
			}
		}
		log.Printf("   %s done", t.Name)
	}
	return map[string]any{"tasks": tasks, "samples": samples, "summary": summarizeWork(tasks, samples)}, nil
}

type taskSummary struct {
	Task      string                   `json:"task"`
	Kind      string                   `json:"kind"`
	Metric    string                   `json:"metric"`
	ByArm     map[string]stats.Summary `json:"by_arm"`
	Failures  map[string]int           `json:"failures"`
	RunscRunc stats.Ratio              `json:"runsc_over_runc"`
	AAOverRun stats.Ratio              `json:"runc_aa_over_runc"`
}

func summarizeWork(tasks []work.Task, samples []Sample) []taskSummary {
	var out []taskSummary
	for i, t := range tasks {
		v := map[string][]float64{}
		fail := map[string]int{}
		for _, s := range samples {
			if s.Task != t.Name {
				continue
			}
			if s.Code != 0 {
				fail[s.Arm]++
				continue
			}
			x := s.WallSec
			if t.Kind == "micro" {
				x = s.NsPerOp
			}
			v[s.Arm] = append(v[s.Arm], x)
		}
		ts := taskSummary{Task: t.Name, Kind: t.Kind, Metric: "wall_sec", ByArm: map[string]stats.Summary{}, Failures: fail}
		if t.Kind == "micro" {
			ts.Metric = "ns_per_op"
		}
		for a, x := range v {
			ts.ByArm[a] = stats.Summarize(x)
		}
		paired := len(v["runc"]) == len(v["runsc"]) && len(v["runc"]) == len(v["runc-aa"])
		if len(v["runc"]) > 1 && len(v["runsc"]) > 1 {
			ts.RunscRunc = stats.MedianRatio(v["runc"], v["runsc"], paired, uint64(10+i))
		}
		if len(v["runc"]) > 1 && len(v["runc-aa"]) > 1 {
			ts.AAOverRun = stats.MedianRatio(v["runc"], v["runc-aa"], paired, uint64(100+i))
		}
		out = append(out, ts)
	}
	return out
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// ---- memory per sandbox

func meminfo(key string) int64 {
	b, _ := os.ReadFile("/proc/meminfo")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, key+":") {
			var v int64
			fmt.Sscan(strings.TrimSpace(strings.TrimPrefix(l, key+":")), &v)
			return v * 1024
		}
	}
	return 0
}

func pss(pid int) int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/smaps_rollup", pid))
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "Pss:") {
			var v int64
			fmt.Sscan(strings.TrimSpace(strings.TrimPrefix(l, "Pss:")), &v)
			return v * 1024
		}
	}
	return 0
}

func dropCaches() {
	exec.Command("sync").Run()
	os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0o200)
	time.Sleep(500 * time.Millisecond)
}

func (b *bench) memory(n int) (any, error) {
	ctx := context.Background()
	type res struct {
		N                int     `json:"sandboxes"`
		MemAvailDeltaMiB float64 `json:"mem_available_drop_per_sandbox_mib"`
		PssMiB           float64 `json:"pss_per_sandbox_mib"`
		Procs            int     `json:"host_processes_per_sandbox"`
		Per64GiB         float64 `json:"idle_sandboxes_per_64gib_by_pss"`
	}
	out := map[string]res{}
	for _, a := range []rt.Runtime{b.arms[0], b.arms[2]} {
		dropCaches()
		before := meminfo("MemAvailable")
		var boxes []*sandbox
		for i := 0; i < n; i++ {
			s, _, _, err := b.start(ctx, a, false, 0, 0)
			if err != nil {
				for _, s := range boxes {
					s.stop()
				}
				return nil, err
			}
			boxes = append(boxes, s)
		}
		time.Sleep(3 * time.Second)
		var roots []int
		for _, s := range boxes {
			roots = append(roots, a.StatePid(ctx, s.id))
		}
		pids := a.Pids(roots...)
		var p int64
		for _, pid := range pids {
			p += pss(pid)
		}
		dropCaches()
		after := meminfo("MemAvailable")
		for _, s := range boxes {
			s.stop()
		}
		r := res{N: n, MemAvailDeltaMiB: float64(before-after) / float64(n) / (1 << 20), PssMiB: float64(p) / float64(n) / (1 << 20),
			Procs: len(pids) / n}
		if r.PssMiB > 0 {
			r.Per64GiB = 64 * 1024 / r.PssMiB
		}
		out[a.Name] = r
		log.Printf("   %s: %+v", a.Name, r)
	}
	return out, nil
}

// ---- isolation

func (b *bench) isolation() (any, error) {
	ctx := context.Background()
	out := map[string]any{}
	for _, a := range []rt.Runtime{b.arms[0], b.arms[2]} {
		m := map[string]any{}
		var s *sandbox
		for _, c := range work.IsolationChecks() {
			if s == nil {
				var err error
				if s, _, _, err = b.start(ctx, a, true, 256<<20, 64); err != nil {
					return nil, err
				}
			}
			e, err := s.r.Exec(ctx, s.id, nil, "/work", "python3", "-c", c.Py)
			var v any
			if err == nil && e.Code == 0 && json.Unmarshal([]byte(strings.TrimSpace(e.Stdout)), &v) == nil {
				m[c.Name] = v
			} else {
				// did the check take the whole sandbox down?
				alive, _ := s.r.Exec(ctx, s.id, nil, "", "/bin/true")
				if alive.Code != 0 || err != nil {
					m[c.Name] = fmt.Sprintf("SANDBOX TERMINATED (exec exit %d: %s)", e.Code, tail(e.Stderr, 120))
					s.stop()
					s = nil
					continue
				}
				m[c.Name] = fmt.Sprintf("check failed (exit %d): %s", e.Code, tail(e.Stderr+e.Stdout, 160))
			}
		}
		if s != nil {
			s.stop()
		}
		out[a.Name] = m
	}
	return out, nil
}

// ---- environment

func environment(arms []rt.Runtime) map[string]any {
	ver := func(bin string, args ...string) string {
		o, err := exec.Command(bin, args...).CombinedOutput()
		if err != nil {
			return "unknown"
		}
		return strings.TrimSpace(strings.SplitN(string(o), "\n", 2)[0])
	}
	cpu := "unknown"
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "model name") {
				cpu = strings.TrimSpace(strings.SplitN(l, ":", 2)[1])
				break
			}
		}
	}
	un, _ := exec.Command("uname", "-r").Output()
	return map[string]any{"runc": ver(*runcBin, "--version"), "runsc": ver(*runscBin, "--version"), "runsc_flags": arms[2].Flags,
		"cpu": cpu, "vcpus": runtime.NumCPU(), "mem_total_gib": float64(meminfo("MemTotal")) / (1 << 30),
		"kernel": strings.TrimSpace(string(un)), "kvm": fileExists("/dev/kvm"), "date": time.Now().UTC().Format(time.RFC3339)}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
