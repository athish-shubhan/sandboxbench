package main

// Snapshot experiment: is restoring a warm sandbox from a checkpoint faster
// than starting one cold and warming it up? This is the question REAP and
// FaaSnap ask for functions; for agent sandboxes the warm state is an
// interpreter with the task's libraries already imported.
//
// The sandbox's init process is a "warm executor": it imports numpy, pandas
// and matplotlib (what a data-analysis agent's first step loads), writes
// /work/ready, then writes a heartbeat counter to /work/hb every 5 ms. The
// host watches /work through the bind mount.
//
//   - cold:    create + start, until /work/ready appears (import time included);
//   - restore: runsc checkpoint of a warm sandbox, then runsc restore into a
//              new container, until the heartbeat advances (the restored
//              process is running again with its imports in memory);
//   - pause / resume latency for both runtimes (cgroup freezer for runc).
//
// runc checkpoint needs CRIU, which is not installable here, so only gVisor
// has a restore arm.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"sandboxbench/internal/rt"
	"sandboxbench/internal/stats"
)

const warmExecutor = `
import os, time
t0 = time.time()
for m in ("json", "sqlite3", "numpy", "pandas", "matplotlib"):
    __import__(m)
open("/work/ready.tmp", "w").write("%.3f" % (time.time() - t0))
os.replace("/work/ready.tmp", "/work/ready")
i = 0
while True:
    i += 1
    open("/work/hb.tmp", "w").write(str(i))
    os.replace("/work/hb.tmp", "/work/hb")
    time.sleep(0.005)
`

func waitFile(path string, timeout time.Duration) (time.Duration, error) {
	t := time.Now()
	for time.Since(t) < timeout {
		if _, err := os.Stat(path); err == nil {
			return time.Since(t), nil
		}
		time.Sleep(500 * time.Microsecond)
	}
	return 0, fmt.Errorf("%s did not appear in %s", path, timeout)
}

func readInt(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return -1
	}
	return n
}

// waitAdvance waits until the heartbeat counter changes from its current value.
func waitAdvance(path string, timeout time.Duration) (time.Duration, error) {
	t := time.Now()
	start := readInt(path)
	for time.Since(t) < timeout {
		if v := readInt(path); v != start && v >= 0 {
			return time.Since(t), nil
		}
		time.Sleep(500 * time.Microsecond)
	}
	return 0, fmt.Errorf("heartbeat did not advance in %s", timeout)
}

func dirSize(dir string) int64 {
	var n int64
	filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

type snapResult struct {
	Runtime        string               `json:"runtime"`
	ColdReadyMs    stats.Summary        `json:"cold_create_start_to_ready_ms"`
	ImportSec      []float64            `json:"import_sec_inside"`
	CheckpointMs   *stats.Summary       `json:"checkpoint_ms,omitempty"`
	ImageMiB       float64              `json:"checkpoint_image_mib,omitempty"`
	RestoreCmdMs   *stats.Summary       `json:"restore_command_ms,omitempty"`
	RestoreReadyMs *stats.Summary       `json:"restore_to_running_ms,omitempty"`
	PauseMs        stats.Summary        `json:"pause_ms"`
	ResumeMs       stats.Summary        `json:"resume_ms"`
	Errors         []string             `json:"errors,omitempty"`
	Raw            map[string][]float64 `json:"raw"`
}

func (b *bench) snapshot(n int) (any, error) {
	ctx := context.Background()
	var out []snapResult
	arms := []rt.Runtime{b.arms[0], b.arms[2]} // runc, runsc
	for _, r := range arms {
		res := snapResult{Runtime: r.Name, Raw: map[string][]float64{}}
		add := func(k string, v float64) { res.Raw[k] = append(res.Raw[k], v) }
		for i := 0; i < n; i++ {
			id := b.newID(r)
			bundle, wd, err := b.bundle(r, id, true, 2<<30, 0, "python3", "-c", warmExecutor)
			if err != nil {
				return nil, err
			}
			t := time.Now()
			if _, err := r.Create(ctx, id, bundle); err != nil {
				res.Errors = append(res.Errors, err.Error())
				continue
			}
			if _, err := r.Start(ctx, id); err != nil {
				res.Errors = append(res.Errors, err.Error())
				r.Teardown(ctx, id)
				continue
			}
			if _, err := waitFile(filepath.Join(wd, "ready"), 60*time.Second); err != nil {
				res.Errors = append(res.Errors, err.Error())
				r.Teardown(ctx, id)
				continue
			}
			add("cold", ms(time.Since(t)))
			if b, err := os.ReadFile(filepath.Join(wd, "ready")); err == nil {
				f, _ := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
				res.ImportSec = append(res.ImportSec, f)
			}
			waitAdvance(filepath.Join(wd, "hb"), 5*time.Second)

			// pause / resume
			tp := time.Now()
			if _, err := r.Cmd(ctx, "pause", id); err == nil {
				add("pause", ms(time.Since(tp)))
				tr := time.Now()
				if _, err := r.Cmd(ctx, "resume", id); err == nil {
					add("resume", ms(time.Since(tr)))
				} else {
					res.Errors = append(res.Errors, err.Error())
				}
			} else {
				res.Errors = append(res.Errors, err.Error())
			}

			if r.Name == "runsc" {
				img := filepath.Join(*state, "images", id)
				os.RemoveAll(img)
				os.MkdirAll(img, 0o755)
				tc := time.Now()
				if _, err := r.Cmd(ctx, "checkpoint", "--image-path="+img, id); err != nil {
					res.Errors = append(res.Errors, err.Error())
					r.Teardown(ctx, id)
					continue
				}
				add("checkpoint", ms(time.Since(tc)))
				add("image_mib", float64(dirSize(img))/(1<<20))
				r.Delete(ctx, id)
				// restore into a new container with the same bundle (same /work mount)
				id2 := id + "-r"
				tr := time.Now()
				if _, err := r.Cmd(ctx, "restore", "--detach", "--bundle", bundle, "--image-path="+img, id2); err != nil {
					res.Errors = append(res.Errors, err.Error())
					os.RemoveAll(img)
					continue
				}
				add("restore_cmd", ms(time.Since(tr)))
				if _, err := waitAdvance(filepath.Join(wd, "hb"), 30*time.Second); err != nil {
					res.Errors = append(res.Errors, err.Error())
				} else {
					add("restore_ready", ms(time.Since(tr)))
				}
				r.Teardown(ctx, id2)
				os.RemoveAll(img)
			} else {
				r.Teardown(ctx, id)
			}
			os.RemoveAll(bundle)
			os.RemoveAll(wd)
		}
		res.ColdReadyMs = stats.Summarize(res.Raw["cold"])
		res.PauseMs = stats.Summarize(res.Raw["pause"])
		res.ResumeMs = stats.Summarize(res.Raw["resume"])
		if v := res.Raw["checkpoint"]; len(v) > 0 {
			s1, s2, s3 := stats.Summarize(v), stats.Summarize(res.Raw["restore_cmd"]), stats.Summarize(res.Raw["restore_ready"])
			res.CheckpointMs, res.RestoreCmdMs, res.RestoreReadyMs = &s1, &s2, &s3
			res.ImageMiB = stats.Median(res.Raw["image_mib"])
		}
		out = append(out, res)
	}
	return out, nil
}
