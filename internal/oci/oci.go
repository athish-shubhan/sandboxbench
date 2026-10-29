// Package oci writes the OCI runtime bundles both runtimes run. The same
// config.json is handed to runc and runsc, so any difference measured is the
// runtime, not the container definition.
package oci

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Minimal subset of the OCI runtime spec (github.com/opencontainers/runtime-spec).
type Spec struct {
	OCIVersion string  `json:"ociVersion"`
	Process    Process `json:"process"`
	Root       Root    `json:"root"`
	Hostname   string  `json:"hostname"`
	Mounts     []Mount `json:"mounts"`
	Linux      Linux   `json:"linux"`
}

type Process struct {
	Terminal        bool          `json:"terminal"`
	User            User          `json:"user"`
	Args            []string      `json:"args"`
	Env             []string      `json:"env"`
	Cwd             string        `json:"cwd"`
	Capabilities    *Capabilities `json:"capabilities,omitempty"`
	NoNewPrivileges bool          `json:"noNewPrivileges"`
	Rlimits         []Rlimit      `json:"rlimits,omitempty"`
}

type User struct {
	UID int `json:"uid"`
	GID int `json:"gid"`
}

type Capabilities struct {
	Bounding    []string `json:"bounding"`
	Effective   []string `json:"effective"`
	Permitted   []string `json:"permitted"`
	Inheritable []string `json:"inheritable"`
	Ambient     []string `json:"ambient"`
}

type Rlimit struct {
	Type string `json:"type"`
	Hard uint64 `json:"hard"`
	Soft uint64 `json:"soft"`
}

type Root struct {
	Path     string `json:"path"`
	Readonly bool   `json:"readonly"`
}

type Mount struct {
	Destination string   `json:"destination"`
	Type        string   `json:"type"`
	Source      string   `json:"source"`
	Options     []string `json:"options,omitempty"`
}

type Linux struct {
	Namespaces    []Namespace `json:"namespaces"`
	Resources     *Resources  `json:"resources,omitempty"`
	Seccomp       *Seccomp    `json:"seccomp,omitempty"`
	MaskedPaths   []string    `json:"maskedPaths,omitempty"`
	ReadonlyPaths []string    `json:"readonlyPaths,omitempty"`
	CgroupsPath   string      `json:"cgroupsPath,omitempty"`
}

type Namespace struct {
	Type string `json:"type"`
}

type Resources struct {
	Memory *Memory `json:"memory,omitempty"`
	Pids   *Pids   `json:"pids,omitempty"`
	CPU    *CPU    `json:"cpu,omitempty"`
}

type Memory struct {
	Limit int64 `json:"limit"`
	Swap  int64 `json:"swap"`
}

type Pids struct {
	Limit int64 `json:"limit"`
}

type CPU struct {
	Quota  int64  `json:"quota,omitempty"`
	Period uint64 `json:"period,omitempty"`
}

type Seccomp struct {
	DefaultAction string           `json:"defaultAction"`
	Architectures []string         `json:"architectures"`
	Syscalls      []SeccompSyscall `json:"syscalls"`
}

type SeccompSyscall struct {
	Names  []string `json:"names"`
	Action string   `json:"action"`
	Errno  uint     `json:"errnoRet,omitempty"`
}

// Options are the knobs a benchmark run varies.
type Options struct {
	Args      []string
	Rootfs    string            // host directory used read-only as /
	Work      string            // host directory bind-mounted read-write at /work ("" = none)
	Binds     map[string]string // extra read-only binds: container path -> host path
	MemLimit  int64             // bytes, 0 = none
	PidsLimit int64             // 0 = none
	Env       []string
}

// defaultCaps is the Docker default capability set minus NET_RAW.
var defaultCaps = []string{"CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_FSETID", "CAP_FOWNER", "CAP_MKNOD", "CAP_SETGID",
	"CAP_SETUID", "CAP_SETFCAP", "CAP_SETPCAP", "CAP_NET_BIND_SERVICE", "CAP_SYS_CHROOT", "CAP_KILL", "CAP_AUDIT_WRITE"}

// deniedSyscalls is a short deny list in the spirit of Docker's default
// profile: kernel keyring, module and kexec loading, mount and namespace
// creation, BPF, perf, ptrace. Default action allows the rest.
var deniedSyscalls = []string{"add_key", "request_key", "keyctl", "init_module", "finit_module", "delete_module",
	"kexec_load", "kexec_file_load", "mount", "umount2", "pivot_root", "unshare", "setns", "bpf", "perf_event_open",
	"ptrace", "process_vm_readv", "process_vm_writev", "open_by_handle_at", "swapon", "swapoff", "reboot", "acct",
	"settimeofday", "clock_settime", "userfaultfd"}

func New(o Options) Spec {
	env := append([]string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/work", "LANG=C.UTF-8",
		"PIP_NO_INDEX=1", "PIP_DISABLE_PIP_VERSION_CHECK=1", "PYTHONDONTWRITEBYTECODE="}, o.Env...)
	caps := &Capabilities{Bounding: defaultCaps, Effective: defaultCaps, Permitted: defaultCaps}
	s := Spec{
		OCIVersion: "1.0.2",
		Process:    Process{User: User{0, 0}, Args: o.Args, Env: env, Cwd: "/", Capabilities: caps, NoNewPrivileges: true},
		Root:       Root{Path: o.Rootfs, Readonly: true},
		Hostname:   "sandbox",
		Mounts: []Mount{
			{Destination: "/proc", Type: "proc", Source: "proc"},
			{Destination: "/dev", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "strictatime", "mode=755", "size=65536k"}},
			{Destination: "/dev/pts", Type: "devpts", Source: "devpts", Options: []string{"nosuid", "noexec", "newinstance", "ptmxmode=0666", "mode=0620"}},
			{Destination: "/dev/shm", Type: "tmpfs", Source: "shm", Options: []string{"nosuid", "noexec", "nodev", "mode=1777", "size=65536k"}},
			{Destination: "/dev/mqueue", Type: "mqueue", Source: "mqueue", Options: []string{"nosuid", "noexec", "nodev"}},
			{Destination: "/sys", Type: "sysfs", Source: "sysfs", Options: []string{"nosuid", "noexec", "nodev", "ro"}},
			{Destination: "/tmp", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "nodev", "mode=1777", "size=1g"}},
			{Destination: "/run", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "nodev", "mode=755", "size=64m"}},
		},
		Linux: Linux{
			Namespaces: []Namespace{{"pid"}, {"network"}, {"ipc"}, {"uts"}, {"mount"}},
			Seccomp: &Seccomp{DefaultAction: "SCMP_ACT_ALLOW", Architectures: []string{"SCMP_ARCH_X86_64"},
				Syscalls: []SeccompSyscall{{Names: deniedSyscalls, Action: "SCMP_ACT_ERRNO", Errno: 1}}},
			MaskedPaths:   []string{"/proc/kcore", "/proc/keys", "/proc/timer_list", "/sys/firmware"},
			ReadonlyPaths: []string{"/proc/bus", "/proc/fs", "/proc/irq", "/proc/sys", "/proc/sysrq-trigger"},
		},
	}
	if o.Work != "" {
		s.Mounts = append(s.Mounts, Mount{Destination: "/work", Type: "bind", Source: o.Work, Options: []string{"rbind", "rw"}})
		s.Process.Cwd = "/work"
	}
	for dst, src := range o.Binds {
		s.Mounts = append(s.Mounts, Mount{Destination: dst, Type: "bind", Source: src, Options: []string{"rbind", "ro"}})
	}
	if o.MemLimit > 0 || o.PidsLimit > 0 {
		s.Linux.Resources = &Resources{}
		if o.MemLimit > 0 {
			s.Linux.Resources.Memory = &Memory{Limit: o.MemLimit, Swap: o.MemLimit}
		}
		if o.PidsLimit > 0 {
			s.Linux.Resources.Pids = &Pids{Limit: o.PidsLimit}
		}
	}
	return s
}

// Write creates dir/config.json.
func Write(dir string, s Spec) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), b, 0o644)
}
