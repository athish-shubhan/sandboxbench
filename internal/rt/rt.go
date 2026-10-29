// Package rt drives OCI runtimes through their CLIs (runc, runsc), the way a
// containerd shim would, and times each lifecycle stage.
package rt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runtime is one OCI runtime configuration.
type Runtime struct {
	Name  string   // label in results, e.g. "runc", "runsc"
	Bin   string   // path to the binary
	Root  string   // state directory (--root)
	Flags []string // global flags placed before the subcommand
}

func Runc(bin, root string) Runtime {
	return Runtime{Name: "runc", Bin: bin, Root: root}
}

// Runsc is gVisor with the systrap platform (no KVM needed) and no network
// stack inside the sandbox.
func Runsc(bin, root string, extra ...string) Runtime {
	return Runtime{Name: "runsc", Bin: bin, Root: root, Flags: append([]string{"--platform=systrap", "--network=none"}, extra...)}
}

func (r Runtime) cmd(ctx context.Context, args ...string) *exec.Cmd {
	a := append([]string{"--root", r.Root}, r.Flags...)
	return exec.CommandContext(ctx, r.Bin, append(a, args...)...)
}

var devnull, _ = os.OpenFile(os.DevNull, os.O_RDWR, 0)

// quiet runs a lifecycle command whose stdio must not be a pipe: a detached
// container inherits it and would keep the pipe open forever.
func (r Runtime) quiet(ctx context.Context, args ...string) (time.Duration, error) {
	c := r.cmd(ctx, args...)
	c.Stdin, c.Stdout = devnull, devnull
	var errb bytes.Buffer
	ef, err := os.CreateTemp("", "rt-stderr-")
	if err != nil {
		return 0, err
	}
	defer os.Remove(ef.Name())
	defer ef.Close()
	c.Stderr = ef
	t := time.Now()
	err = c.Run()
	d := time.Since(t)
	if err != nil {
		b, _ := os.ReadFile(ef.Name())
		errb.Write(b)
		return d, fmt.Errorf("%s %s: %v: %s", r.Name, args[0], err, strings.TrimSpace(errb.String()))
	}
	return d, nil
}

// Cmd runs any runtime subcommand (pause, resume, checkpoint, restore ...).
func (r Runtime) Cmd(ctx context.Context, args ...string) (time.Duration, error) {
	return r.quiet(ctx, args...)
}

func (r Runtime) Create(ctx context.Context, id, bundle string) (time.Duration, error) {
	return r.quiet(ctx, "create", "--bundle", bundle, id)
}

func (r Runtime) Start(ctx context.Context, id string) (time.Duration, error) {
	return r.quiet(ctx, "start", id)
}

func (r Runtime) Kill(ctx context.Context, id string) (time.Duration, error) {
	return r.quiet(ctx, "kill", id, "KILL")
}

func (r Runtime) Delete(ctx context.Context, id string) (time.Duration, error) {
	return r.quiet(ctx, "delete", "--force", id)
}

// Teardown kills and deletes, returning the total time.
func (r Runtime) Teardown(ctx context.Context, id string) (time.Duration, error) {
	t := time.Now()
	r.Kill(ctx, id)
	_, err := r.Delete(ctx, id)
	return time.Since(t), err
}

// ExecResult is one command run inside a running container.
type ExecResult struct {
	Wall   time.Duration
	Code   int
	Stdout string
	Stderr string
}

// Exec runs args inside the container (cwd and env from the bundle, plus extra env).
func (r Runtime) Exec(ctx context.Context, id string, env []string, cwd string, args ...string) (ExecResult, error) {
	a := []string{"exec"}
	for _, e := range env {
		a = append(a, "--env", e)
	}
	if cwd != "" {
		a = append(a, "--cwd", cwd)
	}
	a = append(a, id)
	a = append(a, args...)
	c := r.cmd(ctx, a...)
	var o, e bytes.Buffer
	c.Stdin, c.Stdout, c.Stderr = devnull, &o, &e
	t := time.Now()
	err := c.Run()
	res := ExecResult{Wall: time.Since(t), Stdout: o.String(), Stderr: e.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		res.Code = ee.ExitCode()
		return res, nil // a non-zero exit of the workload is data, not a harness error
	}
	return res, err
}

// Run is the one-shot path: create + start + wait + delete in one call.
func (r Runtime) Run(ctx context.Context, id, bundle string) (ExecResult, error) {
	c := r.cmd(ctx, "run", "--bundle", bundle, id)
	var o, e bytes.Buffer
	c.Stdin, c.Stdout, c.Stderr = devnull, &o, &e
	t := time.Now()
	err := c.Run()
	res := ExecResult{Wall: time.Since(t), Stdout: o.String(), Stderr: e.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		res.Code = ee.ExitCode()
		err = nil
	}
	r.Delete(context.Background(), id)
	return res, err
}

// StatePid is the pid the runtime reports for a container (the container's
// init process for runc, the sandbox process for runsc).
func (r Runtime) StatePid(ctx context.Context, id string) int {
	out, err := r.cmd(ctx, "state", id).Output()
	if err != nil {
		return 0
	}
	var st struct {
		Pid int `json:"pid"`
	}
	json.Unmarshal(out, &st)
	return st.Pid
}

// Pids lists host PIDs belonging to the runtime's containers: the given
// root pids (from StatePid), processes whose command line mentions the
// runtime's state root (runsc's gofer and sandbox), and all their
// descendants.
func (r Runtime) Pids(extra ...int) []int {
	ents, _ := os.ReadDir("/proc")
	parent := map[int]int{}
	var roots []int
	for _, e := range ents {
		var pid int
		if _, err := fmt.Sscan(e.Name(), &pid); err != nil {
			continue
		}
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue
		}
		cl := strings.ReplaceAll(string(b), "\x00", " ")
		if strings.Contains(cl, r.Root) && !strings.Contains(cl, " exec ") && !strings.Contains(cl, " create ") {
			roots = append(roots, pid)
		}
		st, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		s := string(st)
		if i := strings.LastIndexByte(s, ')'); i > 0 {
			var state string
			var ppid int
			fmt.Sscan(s[i+2:], &state, &ppid)
			parent[pid] = ppid
		}
	}
	in := map[int]bool{}
	for _, p := range append(roots, extra...) {
		if p > 0 {
			in[p] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for p, pp := range parent {
			if !in[p] && in[pp] {
				in[p] = true
				changed = true
			}
		}
	}
	var out []int
	for p := range in {
		out = append(out, p)
	}
	return out
}
