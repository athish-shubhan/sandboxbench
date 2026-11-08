package oci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestNewDefaults(t *testing.T) {
	s := New(Options{Args: []string{"/bin/true"}, Rootfs: "/rootfs"})
	if !s.Root.Readonly || s.Root.Path != "/rootfs" || s.Process.Cwd != "/" || !s.Process.NoNewPrivileges {
		t.Fatalf("root/process: %+v %+v", s.Root, s.Process)
	}
	var ns []string
	for _, n := range s.Linux.Namespaces {
		ns = append(ns, n.Type)
	}
	if !slices.Contains(ns, "network") || !slices.Contains(ns, "pid") || !slices.Contains(ns, "mount") {
		t.Fatalf("namespaces %v: a sandbox needs its own (empty) network namespace", ns)
	}
	if slices.Contains(ns, "user") {
		t.Fatal("no user namespace is configured")
	}
	caps := s.Process.Capabilities.Bounding
	for _, c := range []string{"CAP_NET_RAW", "CAP_SYS_ADMIN", "CAP_SYS_PTRACE", "CAP_SYS_MODULE"} {
		if slices.Contains(caps, c) {
			t.Fatalf("%s must not be granted", c)
		}
	}
	denied := s.Linux.Seccomp.Syscalls[0]
	if denied.Action != "SCMP_ACT_ERRNO" || denied.Errno != 1 {
		t.Fatalf("deny rule: %+v", denied)
	}
	for _, sc := range []string{"mount", "unshare", "setns", "ptrace", "bpf", "kexec_load", "init_module"} {
		if !slices.Contains(denied.Names, sc) {
			t.Fatalf("%s should be denied", sc)
		}
	}
	if s.Linux.Resources != nil {
		t.Fatal("no limits requested, none should be set")
	}
	for _, m := range s.Mounts {
		if m.Destination == "/work" {
			t.Fatal("no /work mount without Options.Work")
		}
	}
}

func TestNewWorkLimitsAndBinds(t *testing.T) {
	o := Options{Args: []string{"sh"}, Rootfs: "/r", Work: "/host/work", MemLimit: 256 << 20, PidsLimit: 64,
		Env:   []string{"FOO=1"},
		Binds: map[string]string{"/opt/corpus/sub": "/h/sub", "/opt/corpus": "/h/corpus", "/a": "/h/a", "/z": "/h/z"}}
	s := New(o)
	if s.Process.Cwd != "/work" {
		t.Fatalf("cwd %s", s.Process.Cwd)
	}
	if m := s.Linux.Resources.Memory; m == nil || m.Limit != 256<<20 || m.Swap != 256<<20 {
		t.Fatalf("memory limit (swap equal to limit means no swap): %+v", m)
	}
	if p := s.Linux.Resources.Pids; p == nil || p.Limit != 64 {
		t.Fatalf("pids: %+v", p)
	}
	if s.Process.Env[len(s.Process.Env)-1] != "FOO=1" {
		t.Fatalf("extra env goes last so it overrides defaults: %v", s.Process.Env)
	}
	var binds []string
	for _, m := range s.Mounts {
		switch {
		case m.Destination == "/work":
			if m.Source != "/host/work" || !slices.Contains(m.Options, "rw") {
				t.Fatalf("work mount %+v", m)
			}
		case m.Type == "bind":
			if !slices.Contains(m.Options, "ro") {
				t.Fatalf("extra binds are read-only: %+v", m)
			}
			binds = append(binds, m.Destination)
		}
	}
	if want := []string{"/a", "/opt/corpus", "/opt/corpus/sub", "/z"}; !slices.Equal(binds, want) {
		t.Fatalf("bind order %v, want %v (sorted: parent before child)", binds, want)
	}
}

// The same options must give byte-identical config.json (the bind map used
// to be iterated in random order).
func TestWriteIsDeterministic(t *testing.T) {
	o := Options{Args: []string{"x"}, Rootfs: "/r", Binds: map[string]string{"/b1": "1", "/b2": "2", "/b3": "3", "/b4": "4", "/b5": "5"}}
	dir := t.TempDir()
	var first []byte
	for i := 0; i < 20; i++ {
		d := filepath.Join(dir, "bundle")
		if err := Write(d, New(o)); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(d, "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = b
			var back Spec
			if err := json.Unmarshal(b, &back); err != nil || back.OCIVersion != "1.0.2" {
				t.Fatalf("config.json does not round-trip: %v", err)
			}
		} else if string(b) != string(first) {
			t.Fatalf("run %d wrote a different config.json", i)
		}
	}
}
