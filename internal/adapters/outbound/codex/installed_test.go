package codex

import (
	"context"
	"testing"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/testutil"
)

func TestInstalledCLIHandshake(t *testing.T) {
	path := testutil.InstalledCLI(t, "codex")
	t.Setenv("CODEX_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	h := Harness{Path: path, Directory: t.TempDir()}
	// connect only initializes the app-server protocol; it creates no thread,
	// lists no models, and sends no inference or authentication request.
	_, stop, err := h.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}
