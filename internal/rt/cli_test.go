package rt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRuntime writes an OCI-runtime stand-in: it appends its arguments to a
// log, prints a canned state for "state", fails "start" for the id "bad",
// and for "exec" runs the command after the container id so exit codes and
// output are real.
func fakeRuntime(t *testing.T) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls.log")
	bin = filepath.Join(dir, "fake-runtime")
	script := `#!/bin/sh
echo "$*" >> ` + log + `
# skip global flags up to the subcommand
while [ $# -gt 0 ]; do
  case "$1" in
    --root) shift 2 ;;
    --*) shift ;;
    *) break ;;
  esac
done
cmd=$1; shift
case "$cmd" in
  state) echo '{"id":"x","pid":4242,"status":"running"}' ;;
  start) [ "$1" = bad ] && { echo "container bad is broken" >&2; exit 3; } ; exit 0 ;;
  exec)
    while [ $# -gt 0 ]; do
      case "$1" in --env|--cwd) shift 2 ;; *) break ;; esac
    done
    shift # container id
    exec "$@" ;;
  run) echo "ran $*"; exit 0 ;;
  *) exit 0 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func calls(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestLifecycleCommandLines(t *testing.T) {
	bin, log := fakeRuntime(t)
	r := Runsc(bin, "/state/runsc", "--debug")
	ctx := context.Background()
	if _, err := r.Create(ctx, "c1", "/bundles/c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Cmd(ctx, "pause", "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Teardown(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--root /state/runsc --platform=systrap --network=none --debug create --bundle /bundles/c1 c1",
		"--root /state/runsc --platform=systrap --network=none --debug start c1",
		"--root /state/runsc --platform=systrap --network=none --debug pause c1",
		"--root /state/runsc --platform=systrap --network=none --debug kill c1 KILL",
		"--root /state/runsc --platform=systrap --network=none --debug delete --force c1",
	}
	got := calls(t, log)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("command lines:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if Runc(bin, "/state/runc").Flags != nil {
		t.Fatal("runc gets no extra global flags")
	}
}

// A failing lifecycle command's error carries the runtime's stderr.
func TestLifecycleErrorIncludesStderr(t *testing.T) {
	bin, _ := fakeRuntime(t)
	_, err := Runc(bin, t.TempDir()).Start(context.Background(), "bad")
	if err == nil || !strings.Contains(err.Error(), "container bad is broken") || !strings.Contains(err.Error(), "runc start") {
		t.Fatalf("got %v", err)
	}
}

// A workload's non-zero exit is data (Code), not a harness error.
func TestExecResultAndExitCode(t *testing.T) {
	bin, log := fakeRuntime(t)
	r := Runc(bin, t.TempDir())
	ctx := context.Background()
	res, err := r.Exec(ctx, "c1", []string{"A=1", "B=2"}, "/work", "sh", "-c", "echo out; echo err >&2; exit 7")
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != 7 || strings.TrimSpace(res.Stdout) != "out" || strings.TrimSpace(res.Stderr) != "err" || res.Wall <= 0 {
		t.Fatalf("%+v", res)
	}
	last := calls(t, log)[0]
	if !strings.Contains(last, "exec --env A=1 --env B=2 --cwd /work c1 sh -c") {
		t.Fatalf("exec command line %q", last)
	}
	ok, err := r.Exec(ctx, "c1", nil, "", "true")
	if err != nil || ok.Code != 0 {
		t.Fatalf("%+v %v", ok, err)
	}
}

// A context deadline kills a hung exec; it is reported as a non-zero exit.
func TestExecTimeout(t *testing.T) {
	bin, _ := fakeRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := Runc(bin, t.TempDir()).Exec(ctx, "c1", nil, "", "sleep", "30")
	if time.Since(start) > 5*time.Second {
		t.Fatal("deadline not enforced")
	}
	if err == nil && res.Code == 0 {
		t.Fatalf("a killed exec must not look like success: %+v", res)
	}
}

func TestRunDeletesAndStatePid(t *testing.T) {
	bin, log := fakeRuntime(t)
	r := Runc(bin, "/s")
	res, err := r.Run(context.Background(), "c9", "/b/c9")
	if err != nil || !strings.Contains(res.Stdout, "ran --bundle /b/c9 c9") {
		t.Fatalf("%+v %v", res, err)
	}
	got := calls(t, log)
	if got[len(got)-1] != "--root /s delete --force c9" {
		t.Fatalf("one-shot run must delete the container: %v", got)
	}
	if pid := r.StatePid(context.Background(), "c9"); pid != 4242 {
		t.Fatalf("StatePid = %d", pid)
	}
	if pid := Runc("/nonexistent/runtime", "/s").StatePid(context.Background(), "x"); pid != 0 {
		t.Fatalf("missing runtime: pid %d", pid)
	}
}
