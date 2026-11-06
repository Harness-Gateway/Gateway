package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/testutil"
)

func TestInstalledCLIVersionProbes(t *testing.T) {
	for _, name := range []string{"codex", "copilot"} {
		t.Run(name, func(t *testing.T) {
			path := testutil.InstalledCLI(t, name)
			if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(path), ".cmd") {
				t.Fatalf("npm smoke test must exercise the real .cmd entry point, resolved %q", path)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			version, err := queryHarnessVersion(ctx, name, path)
			if err != nil || version == "" {
				t.Fatalf("installed %s version query failed: version=%q error=%v", name, version, err)
			}
			if want := os.Getenv("HARNESS_GATEWAY_TEST_" + strings.ToUpper(name) + "_VERSION"); want != "" && version != want {
				t.Fatalf("tested %s %s, expected pinned version %s", name, version, want)
			}
			t.Logf("tested %s %s through %s", name, version, path)
			bounded, err := probeHarnessVersion(t.Context(), name, path)
			if err != nil {
				if !errors.Is(err, context.DeadlineExceeded) || bounded != "" {
					t.Fatalf("bounded startup probe failed unexpectedly: version=%q error=%v", bounded, err)
				}
				t.Logf("%s exceeded the startup budget; the panel correctly reports unavailable without blocking startup", name)
			} else if bounded != version {
				t.Fatalf("startup probe changed the version: got %q want %q", bounded, version)
			}
		})
	}
}
