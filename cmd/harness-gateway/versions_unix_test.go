//go:build unix

package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestVersionProbeStopsDescendants(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses Linux procfs to inspect surviving fixture processes")
	}
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	for _, mode := range []string{"child", "pipe-child"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			t.Setenv("HARNESS_GATEWAY_VERSION_TEST", mode)
			t.Setenv("HARNESS_GATEWAY_VERSION_PIDS", directory)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if version, err := probeHarnessVersion(ctx, "codex", os.Args[0]); version != "" || err == nil {
				t.Fatalf("misbehaving version probe succeeded: %q, %v", version, err)
			}
			pids, err := os.ReadDir(directory)
			if err != nil || len(pids) != 2 {
				t.Fatalf("expected a parent and child process: %d, %v", len(pids), err)
			}
			for _, pid := range pids {
				deadline := time.Now().Add(time.Second)
				for {
					_, err := os.Readlink(filepath.Join("/proc", pid.Name(), "exe"))
					if os.IsNotExist(err) {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("version probe left PID %s running", pid.Name())
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
		})
	}
}
