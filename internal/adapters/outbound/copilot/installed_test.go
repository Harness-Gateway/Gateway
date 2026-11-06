package copilot

import (
	"context"
	"testing"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/testutil"
)

func TestInstalledCLIHandshake(t *testing.T) {
	path := testutil.InstalledCLI(t, "copilot")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	h := Harness{Path: path, Directory: t.TempDir()}
	_, connection, stop, err := h.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stop(); err != nil {
			t.Error(err)
		}
	}()
	// Verify an actual SDK/runtime exchange, without session creation, model
	// inference, a login operation, or account credentials supplied by CI.
	if _, err := connection.Ping(ctx, nil); err != nil {
		t.Fatal(err)
	}
}
