package work

import (
	"os/exec"
	"strings"
	"testing"
)

// Task and check names are keys in results/*.json; a duplicate would merge
// two workloads' samples.
func TestNamesUniqueAndTasksComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, tk := range append(Agent(), Micro()...) {
		if seen[tk.Name] {
			t.Fatalf("duplicate task %s", tk.Name)
		}
		seen[tk.Name] = true
		if tk.Run == "" || tk.Desc == "" || (tk.Kind != "agent" && tk.Kind != "micro") {
			t.Fatalf("incomplete task %+v", tk)
		}
		if tk.Kind == "micro" && !strings.HasPrefix(tk.Run, "/corpus/bin/micro ") {
			t.Fatalf("micro task %s must run the micro-benchmark binary: %q", tk.Name, tk.Run)
		}
	}
	checks := map[string]bool{}
	for _, c := range IsolationChecks() {
		if checks[c.Name] || c.Name == "" || c.Py == "" {
			t.Fatalf("bad or duplicate check %q", c.Name)
		}
		checks[c.Name] = true
	}
}

// Every isolation check is Python that must at least parse: a syntax error
// would only show up inside the sandbox, as a failed check, after the run.
func TestIsolationChecksCompile(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	for _, c := range IsolationChecks() {
		cmd := exec.Command(py, "-c", "import ast, sys; ast.parse(sys.stdin.read())")
		cmd.Stdin = strings.NewReader(c.Py)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s does not parse: %v\n%s", c.Name, err, out)
		}
	}
}

// The status-field checks run on the host too and print one JSON value.
func TestStatusFieldCheckRuns(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	for f, want := range map[string]string{"NoNewPrivs": `"`, "NoSuchField": `"absent"`} {
		out, err := exec.Command(py, "-c", statusField(f)).Output()
		if err != nil || !strings.HasPrefix(strings.TrimSpace(string(out)), want) {
			t.Fatalf("%s: %q %v", f, out, err)
		}
	}
}
