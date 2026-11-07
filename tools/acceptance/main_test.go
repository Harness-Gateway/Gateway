package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if code := os.Getenv("HARNESS_GATEWAY_ACCEPTANCE_TEST_EXIT"); code != "" {
		status, err := strconv.Atoi(code)
		if err != nil {
			os.Exit(99)
		}
		os.Exit(status)
	}
	os.Exit(m.Run())
}

func TestInvalidSoakDurationFailsBeforeBuilding(t *testing.T) {
	for _, duration := range []time.Duration{-time.Second, time.Millisecond, time.Hour + time.Second} {
		if err := run(t.Context(), false, "codex,copilot", "", "", "", duration); err == nil || !strings.Contains(err.Error(), "soak must") {
			t.Fatalf("invalid soak duration %s did not fail immediately: %v", duration, err)
		}
	}
}

func TestNativeSelectionRequiresLiveAcceptance(t *testing.T) {
	if err := run(t.Context(), false, "codex", "codex/native-one", "", "", 0); err == nil || !strings.Contains(err.Error(), "requires --live") {
		t.Fatalf("native selection silently ignored outside live acceptance: %v", err)
	}
}

func TestGatewayExitBeforeReadiness(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []int{0, 7} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			env := append(os.Environ(), "HARNESS_GATEWAY_ACCEPTANCE_TEST_EXIT="+strconv.Itoa(code))
			gateway, err := startGateway(t.Context(), t.TempDir(), "early-exit", executable, env)
			if gateway != nil {
				t.Cleanup(func() { _ = gateway.stop() })
				t.Fatal("an exited process must not be ready")
			}
			if err == nil || !strings.Contains(err.Error(), "gateway exited before readiness") {
				t.Fatalf("missing early-exit diagnostic: %v", err)
			}
			if strings.Contains(err.Error(), "%!") || strings.Contains(err.Error(), "<nil>") {
				t.Fatalf("malformed early-exit diagnostic: %v", err)
			}
			var exitErr *exec.ExitError
			if code == 0 {
				if errors.As(err, &exitErr) {
					t.Fatalf("successful exit was reported as a process failure: %v", err)
				}
			} else if !errors.As(err, &exitErr) || exitErr.ExitCode() != code {
				t.Fatalf("process exit status was not preserved: %v", err)
			}
		})
	}
}

func TestSuppliedGatewayDoesNotNeedGoOrBuildOutput(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	output := filepath.Join(t.TempDir(), "must-not-be-built")
	got, err := resolveGateway(t.Context(), output, executable)
	if err != nil || got != executable {
		t.Fatalf("did not use the supplied executable: got=%q error=%v", got, err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("supplied-binary resolution created build output")
	}
}

func TestInvalidSuppliedGatewayDoesNotFallBackToBuild(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, supplied := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing-gateway")} {
		if _, err := resolveGateway(t.Context(), t.TempDir(), supplied); err == nil || !strings.Contains(err.Error(), "gateway binary:") {
			t.Fatalf("invalid supplied binary was not rejected directly: %v", err)
		}
	}
}
