package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/harness-gateway/harness-gateway/internal/testutil"
)

func TestInstalledCLIVersionProbes(t *testing.T) {
	for _, name := range []string{"codex", "copilot"} {
		t.Run(name, func(t *testing.T) {
			path := testutil.InstalledCLI(t, name)
			if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(path), ".cmd") {
				t.Fatalf("npm smoke test must exercise the real .cmd entry point, resolved %q", path)
			}
			version, err := probeHarnessVersion(t.Context(), name, path)
			if err != nil || version == "" {
				t.Fatalf("installed %s version query failed: version=%q error=%v", name, version, err)
			}
			if want := os.Getenv("HARNESS_GATEWAY_TEST_" + strings.ToUpper(name) + "_VERSION"); want != "" && version != want {
				t.Fatalf("tested %s %s, expected pinned version %s", name, version, want)
			}
			t.Logf("tested %s %s through %s", name, version, path)
		})
	}
}
