package rt

import (
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestBelongsTo(t *testing.T) {
	const root = "/run/sandboxbench/runc"
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"runc", "--root", root, "start", "x"}, true},
		{[]string{"runsc-sandbox", "--root=" + root, "boot"}, true},
		{[]string{"runsc-gofer", "--log=" + root + "/x/gofer.log"}, true},
		{[]string{"runc", "--root", root + "/", "kill", "x"}, true},
		// the A/A arm's root shares the prefix and must not match
		{[]string{"runc", "--root", "/run/sandboxbench/runc-aa", "start", "x"}, false},
		{[]string{"runsc-sandbox", "--root=/run/sandboxbench/runc-aa"}, false},
		{[]string{"x", "/run/sandboxbench/runc-aa:" + root}, true},
		// short-lived CLI invocations are not sandbox processes
		{[]string{"runc", "--root", root, "exec", "x", "true"}, false},
		{[]string{"runc", "--root", root, "create", "--bundle", "/b", "x"}, false},
		{[]string{"bash"}, false},
	}
	for _, c := range cases {
		if got := belongsTo(c.args, root); got != c.want {
			t.Errorf("belongsTo(%q) = %v, want %v", c.args, got, c.want)
		}
	}
	if belongsTo([]string{"/x"}, "") {
		t.Error("an empty root must match nothing")
	}
}

// Pids walks the real /proc: a process that names one arm's state root, and
// its child, belong to that arm and not to an arm whose root is a prefix.
func TestPidsSeparatesArmsWithSharedPrefix(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	base := t.TempDir()
	runc := Runtime{Name: "runc", Root: filepath.Join(base, "runc")}
	aa := Runtime{Name: "runc-aa", Root: filepath.Join(base, "runc-aa")}
	// sh's $0 is the root path, so it appears in /proc/<pid>/cmdline; the
	// sleep is its child.
	cmd := exec.Command("sh", "-c", "sleep 30 & wait", "--root="+aa.Root)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	var got []int
	for i := 0; i < 100; i++ { // wait for sh to fork the sleep
		if got = aa.Pids(); len(got) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !slices.Contains(got, cmd.Process.Pid) || len(got) < 2 {
		t.Fatalf("runc-aa arm: got pids %v, want sh %d and its child", got, cmd.Process.Pid)
	}
	if p := runc.Pids(); slices.Contains(p, cmd.Process.Pid) {
		t.Fatalf("runc arm counted the runc-aa arm's process %d: %v", cmd.Process.Pid, p)
	}
	if p := runc.Pids(cmd.Process.Pid); !slices.Contains(p, cmd.Process.Pid) || len(p) < 2 {
		t.Fatalf("an explicit root pid and its descendants are always included: %v", p)
	}
}
