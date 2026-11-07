package copilot

import (
	"bytes"
	"context"
	"os"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

func eventConsumers(t *testing.T) (int, string) {
	t.Helper()
	var profile bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&profile, 2); err != nil {
		t.Fatal(err)
	}
	return bytes.Count(profile.Bytes(), []byte("(*Session).processEvents(")), profile.String()
}

func assertEventConsumersReleased(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		count, profile := eventConsumers(t)
		if count <= baseline {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("requests retained SDK event consumers: %d -> %d\n%s", baseline, count, profile)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLegacyCLIWithoutDetachStillReleasesConsumers(t *testing.T) {
	h, log, _ := testHarness(t, "detach-unsupported")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	baseline, _ := eventConsumers(t)
	if _, err := h.Complete(ctx, testRequest(), func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	methods, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(methods), "session.detach\n") || strings.Contains(string(methods), "session.delete\n") {
		t.Fatalf("legacy session must detach without touching the default profile: %s %v", methods, err)
	}
	assertEventConsumersReleased(t, baseline)
}

func TestDetachFailuresAreBoundedAndReleaseConsumers(t *testing.T) {
	for _, mode := range []string{"detach-error", "detach-hold"} {
		t.Run(mode, func(t *testing.T) {
			h, _, _ := testHarness(t, mode)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			baseline, _ := eventConsumers(t)
			started := time.Now()
			if _, err := h.Complete(ctx, testRequest(), func(string) error { return nil }); err == nil || !strings.Contains(err.Error(), "detach copilot session") {
				t.Fatalf("missing detach failure: %v", err)
			}
			if time.Since(started) >= 4*time.Second {
				t.Fatal("detach ignored the cleanup budget")
			}
			assertEventConsumersReleased(t, baseline)
		})
	}
}

func TestSuccessfulCompletionsReleaseSDKEventConsumers(t *testing.T) {
	baseline, _ := eventConsumers(t)
	h, _, _ := testHarness(t, "normal")
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		_, err := h.Complete(ctx, testRequest(), func(string) error { return nil })
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	assertEventConsumersReleased(t, baseline)
}
