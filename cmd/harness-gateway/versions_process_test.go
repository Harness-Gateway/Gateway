package main

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/testutil"
)

func TestVersionProbeStopsDescendants(t *testing.T) {
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	for _, mode := range []string{"child", "pipe-child"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			t.Setenv("HARNESS_GATEWAY_VERSION_TEST", mode)
			t.Setenv("HARNESS_GATEWAY_VERSION_PIDS", directory)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if version, err := probeHarnessVersion(ctx, "codex", testutil.CLIEntryPoint(t, os.Args[0])); version != "" || err == nil {
				t.Fatalf("misbehaving version probe succeeded: %q, %v", version, err)
			}
			pids, err := os.ReadDir(directory)
			if err != nil || len(pids) != 2 {
				t.Fatalf("expected a parent and child process: %d, %v", len(pids), err)
			}
			for _, pid := range pids {
				id, err := strconv.Atoi(pid.Name())
				if err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(time.Second)
				for {
					running, err := testutil.ProcessRunning(id)
					if err != nil {
						t.Fatal(err)
					}
					if !running {
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
