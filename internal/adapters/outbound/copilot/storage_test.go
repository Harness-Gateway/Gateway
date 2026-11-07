package copilot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRequestStateRemovedOnEveryExit(t *testing.T) {
	for _, mode := range []string{"normal", "hold", "silent", "crash", "create-error", "send-error", "session-error", "detach-error", "detach-hold", "detach-unsupported", "emit-error"} {
		t.Run(mode, func(t *testing.T) {
			fixtureMode := mode
			if mode == "emit-error" {
				fixtureMode = "normal"
			}
			h, _, _ := testHarness(t, fixtureMode)
			profile, temporary := t.TempDir(), t.TempDir()
			for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
				t.Setenv(key, temporary)
			}
			record := filepath.Join(t.TempDir(), "state-directory")
			t.Setenv("HARNESS_GATEWAY_COPILOT_STATE_RECORD", record)
			t.Setenv("HARNESS_GATEWAY_COPILOT_PROFILE", profile)
			sentinel := filepath.Join(profile, "user-session")
			if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			emitErr := errors.New("fixture response write failed")
			_, err := h.Complete(ctx, testRequest(), func(string) error {
				if mode == "hold" {
					cancel()
				}
				if mode == "emit-error" {
					return emitErr
				}
				return nil
			})
			wantSuccess := mode == "normal" || mode == "detach-unsupported"
			if wantSuccess && err != nil || !wantSuccess && err == nil {
				t.Fatalf("unexpected completion result: %v", err)
			}
			if mode == "hold" && !errors.Is(err, context.Canceled) || mode == "silent" && !errors.Is(err, context.DeadlineExceeded) || mode == "emit-error" && !errors.Is(err, emitErr) {
				t.Fatalf("request failure was lost: %v", err)
			}
			data, err := os.ReadFile(record)
			if err != nil {
				t.Fatalf("fixture never persisted session state: %v", err)
			}
			state := string(data)
			if !strings.HasPrefix(state, temporary+string(os.PathSeparator)) {
				t.Fatalf("session used profile instead of request-owned state: %s", state)
			}
			if _, err := os.Stat(state); !os.IsNotExist(err) {
				t.Fatalf("request retained session directory: %v", err)
			}
			entries, err := os.ReadDir(temporary)
			if err != nil || len(entries) != 0 {
				t.Fatalf("temporary state leaked: %v %v", entries, err)
			}
			entries, err = os.ReadDir(profile)
			if err != nil || len(entries) != 1 || entries[0].Name() != "user-session" {
				t.Fatalf("user profile was changed: %v %v", entries, err)
			}
			if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
				t.Fatalf("user session was modified: %v", err)
			}
		})
	}
}

func TestStateRemovedBeforeSessionCreation(t *testing.T) {
	for _, mode := range []string{"cancelled", "missing-cli", "startup-crash"} {
		t.Run(mode, func(t *testing.T) {
			h, _, _ := testHarness(t, mode)
			temporary := t.TempDir()
			for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
				t.Setenv(key, temporary)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if mode == "cancelled" {
				cancel()
			} else if mode == "missing-cli" {
				h.Path = filepath.Join(t.TempDir(), "missing")
			}
			if _, err := h.Complete(ctx, testRequest(), func(string) error { return nil }); err == nil {
				t.Fatal("expected startup failure")
			}
			entries, err := os.ReadDir(temporary)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed startup retained state: %v %v", entries, err)
			}
		})
	}
}

func TestStateDirectoryCreationFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, missing)
	}
	h := Harness{Path: filepath.Join(t.TempDir(), "missing-cli")}
	_, err := h.Complete(t.Context(), testRequest(), func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "create copilot state directory") {
		t.Fatalf("storage failure was not reported: %v", err)
	}
}

func TestStateCleanupFailurePreservesCause(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix directory permissions without a root override")
	}
	for _, mode := range []string{"normal", "hold"} {
		t.Run(mode, func(t *testing.T) {
			h, _, _ := testHarness(t, mode)
			temporary := t.TempDir()
			for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
				t.Setenv(key, temporary)
			}
			t.Cleanup(func() {
				if err := os.Chmod(temporary, 0700); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_, err := h.Complete(ctx, testRequest(), func(string) error {
				// Prevent unlinking the owned directory, after it was created.
				if err := os.Chmod(temporary, 0500); err != nil {
					return err
				}
				if mode == "hold" {
					cancel()
				}
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), "remove copilot state directory") || !errors.Is(err, os.ErrPermission) {
				t.Fatalf("cleanup failure was hidden: %v", err)
			}
			if mode == "hold" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cleanup failure lost the cancellation cause: %v", err)
			}
		})
	}
}
