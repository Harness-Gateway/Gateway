package main

import (
	"bytes"
	"encoding/json"
	"log"
	"log/slog"
	"strings"
	"testing"
)

const closedSDKWriter = "Failed to close JSON-RPC writer: close tcp 127.0.0.1:1234->127.0.0.1:4321: use of closed network connection"

func TestLibraryDiagnosticsJSONAndSeverity(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	restore := routeLibraryDiagnostics(logger)
	defer restore()
	for _, message := range []string{closedSDKWriter, "Failed to close JSON-RPC writer: permission denied", "unexpected protocol error\nwith details"} {
		log.Print(message)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("diagnostics broke JSON record boundaries: %s", output.String())
	}
	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		want := "ERROR"
		if i == 0 {
			want = "DEBUG"
		}
		if record["level"] != want || record["diagnostic"] == nil {
			t.Fatalf("unexpected diagnostic severity or missing detail: %s", line)
		}
	}
}

func TestExpectedSDKCloseDoesNotPolluteDefaultLogs(t *testing.T) {
	var output bytes.Buffer
	restore := routeLibraryDiagnostics(newLogger(&output, "json"))
	log.Print(closedSDKWriter)
	restore()
	if output.Len() != 0 {
		t.Fatalf("benign teardown appeared in default logs: %s", output.String())
	}
}

func TestLibraryDiagnosticRoutingRestoresPreviousConfiguration(t *testing.T) {
	writer, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	var output bytes.Buffer
	restore := routeLibraryDiagnostics(newLogger(&output, "text"))
	restore()
	if log.Writer() != writer || log.Flags() != flags || log.Prefix() != prefix {
		t.Fatal("standard logger configuration was not restored")
	}
}
