package copilot

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func bridgeDescriptors(t *testing.T) map[string]string {
	t.Helper()
	files, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, file := range files {
		target, err := os.Readlink("/proc/self/fd/" + file.Name())
		if err == nil {
			result[file.Name()] = target
		}
	}
	return result
}

func TestBridgeDescriptorCleanup(t *testing.T) {
	h, _, _ := testHarness(t, "normal")
	if _, err := h.Complete(t.Context(), testRequest(), func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	baseline := bridgeDescriptors(t)
	for round := 0; round < 3; round++ {
		for _, mode := range []string{"normal", "hold", "crash", "invalid-params", "oversized-frame", "catalog-error"} {
			t.Setenv("HARNESS_GATEWAY_COPILOT_TEST", mode)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			if mode == "catalog-error" {
				_, _ = h.Models(ctx)
			} else {
				_, _ = h.Complete(ctx, testRequest(), func(string) error {
					if mode == "hold" {
						cancel()
					}
					return nil
				})
			}
			cancel()
			after := bridgeDescriptors(t)
			var extra []string
			for fd, target := range after {
				if baseline[fd] != target {
					extra = append(extra, fmt.Sprintf("%s=%s", fd, target))
				}
			}
			if len(extra) != 0 {
				t.Fatalf("bridge retained descriptors after %s round %d: %s", mode, round, strings.Join(extra, ", "))
			}
		}
	}
}
