package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
