package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/testutil"
)

func TestMain(tests *testing.M) {
	if path := os.Getenv("HARNESS_GATEWAY_SOAK_METRICS"); path != "" {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- observeResources(ctx, path) }()
		main()
		cancel()
		if err := <-done; err != nil {
			fmt.Fprintln(os.Stderr, "resource sampler:", err)
			os.Exit(13)
		}
		os.Exit(0)
	}
	if mode := os.Getenv("HARNESS_GATEWAY_VERSION_TEST"); mode != "" {
		bundled := len(os.Args) == 3 && os.Args[1] == "--no-auto-update" && os.Args[2] == "--version"
		if !bundled && (len(os.Args) != 2 || os.Args[1] != "--version") {
			os.Exit(9)
		}
		if directory := os.Getenv("HARNESS_GATEWAY_VERSION_PIDS"); directory != "" {
			pid := strconv.Itoa(os.Getpid())
			if err := os.WriteFile(filepath.Join(directory, pid), []byte(mode), 0600); err != nil {
				os.Exit(10)
			}
		}
		switch mode {
		case "copilot-cache":
			if bundled {
				fmt.Println("GitHub Copilot CLI 1.0.105.")
			} else {
				fmt.Println("GitHub Copilot CLI 1.0.107.")
			}
		case "ok":
			fmt.Println("codex-cli 1.2.3")
		case "bare":
			fmt.Println("v4.5.6")
		case "failure":
			os.Exit(7)
		case "invalid":
			fmt.Println("unexpected output")
		case "oversized":
			fmt.Print(strings.Repeat("x", maxVersionOutput+1024))
		case "hang":
			time.Sleep(time.Minute)
		case "child", "pipe-child":
			child := exec.Command(os.Args[0], "--version")
			child.Env = append(os.Environ(), "HARNESS_GATEWAY_VERSION_TEST=hang")
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
			if err := child.Start(); err != nil {
				os.Exit(11)
			}
			if directory := os.Getenv("HARNESS_GATEWAY_VERSION_PIDS"); directory != "" {
				deadline := time.Now().Add(5 * time.Second)
				for {
					if _, err := os.Stat(filepath.Join(directory, strconv.Itoa(child.Process.Pid))); err == nil {
						break
					}
					if time.Now().After(deadline) {
						_ = child.Process.Kill()
						_ = child.Wait()
						os.Exit(12)
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			if mode == "child" {
				time.Sleep(time.Minute)
			}
		}
		os.Exit(0)
	}
	os.Exit(tests.Run())
}

func TestParseInstalledHarnessVersions(t *testing.T) {
	for _, test := range []struct{ name, output, want string }{
		{"codex", "codex-cli 0.166.0\n", "0.166.0"},
		{"copilot", "GitHub Copilot CLI 1.0.105.\nRun 'copilot update' to check for updates.\n", "1.0.105"},
		{"copilot", "GitHub Copilot CLI 1.2.3-preview.4+build.5.\n", "1.2.3-preview.4+build.5"},
		{"codex", "\x1b[32mcodex-cli v1.2.3\x1b[0m\r\n", "1.2.3"},
		{"codex", "Warning: Node.js 26.5.0\ncodex-cli 0.166.0\n", "0.166.0"},
		{"copilot", "v1.2.3\n", "1.2.3"},
	} {
		t.Run(test.output, func(t *testing.T) {
			got, err := parseHarnessVersion(test.name, test.output)
			if err != nil || got != test.want {
				t.Fatalf("version=%q, error=%v; want %q", got, err, test.want)
			}
		})
	}
	if _, err := parseHarnessVersion("codex", "Warning: Node.js 26.5.0\n"); err == nil {
		t.Fatal("reported an unrelated runtime version as the harness version")
	}
}

func TestCopilotVersionMatchesBundledRuntime(t *testing.T) {
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	t.Setenv("HARNESS_GATEWAY_VERSION_TEST", "copilot-cache")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	version, err := queryHarnessVersion(ctx, "copilot", testutil.CLIEntryPoint(t, os.Args[0]))
	if err != nil || version != "1.0.105" {
		t.Fatalf("version probe selected an auto-updated runtime instead of the bundled CLI: %q %v", version, err)
	}
}

func TestVersionProbeSuccessAndFailures(t *testing.T) {
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	for _, mode := range []string{"ok", "failure", "invalid", "oversized", "hang"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HARNESS_GATEWAY_VERSION_TEST", mode)
			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()
			version, err := probeHarnessVersion(ctx, "codex", testutil.CLIEntryPoint(t, os.Args[0]))
			if mode == "ok" {
				if version != "1.2.3" || err != nil {
					t.Fatalf("version=%q, error=%v", version, err)
				}
				return
			}
			if version != "" || err == nil {
				t.Fatalf("failed version probe returned version=%q, error=%v", version, err)
			}
			if mode == "hang" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("hanging CLI did not respect cancellation: %v", err)
			}
			if mode == "oversized" && !errors.Is(err, errVersionOutputLimit) {
				t.Fatalf("oversized version output was not bounded: %v", err)
			}
		})
	}
}

func TestVersionQueriesStartTogetherAndPreserveConfiguration(t *testing.T) {
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	t.Setenv("HARNESS_GATEWAY_VERSION_TEST", "hang")
	directory := t.TempDir()
	t.Setenv("HARNESS_GATEWAY_VERSION_PIDS", directory)
	path := testutil.CLIEntryPoint(t, os.Args[0])
	harnesses := []harnessStatus{{name: "codex", executable: path, model: "native-one"}, {name: "copilot", executable: path}}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result := identifyHarnessVersions(ctx, harnesses)
	pids, err := os.ReadDir(directory)
	if err != nil || len(pids) != 2 {
		t.Fatalf("version checks did not start in parallel: %d processes, %v", len(pids), err)
	}
	for i, harness := range result {
		if harness.name != harnesses[i].name || harness.model != harnesses[i].model || !errors.Is(harness.versionError, context.DeadlineExceeded) || harness.installedVersion() != "unavailable" {
			t.Fatalf("version query lost configuration or failure state: %+v", result)
		}
		if harnesses[i].versionError != nil {
			t.Fatal("query modified caller-owned configuration")
		}
	}
}
