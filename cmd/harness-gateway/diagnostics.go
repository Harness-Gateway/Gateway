package main

import (
	"log"
	"log/slog"
	"strings"
)

// Route libraries using log.Printf through the configured gateway handler.
// Only the SDK's known already-closed writer diagnostic is downgraded; other
// library diagnostics remain visible, including in structured JSON mode.
func routeLibraryDiagnostics(logger *slog.Logger) func() {
	writer, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetFlags(0)
	log.SetPrefix("")
	log.SetOutput(libraryDiagnostics{logger})
	return func() { log.SetOutput(writer); log.SetFlags(flags); log.SetPrefix(prefix) }
}

type libraryDiagnostics struct{ logger *slog.Logger }

func (d libraryDiagnostics) Write(data []byte) (int, error) {
	message := strings.TrimSpace(string(data))
	if strings.HasPrefix(message, "Failed to close JSON-RPC writer: close tcp ") && strings.HasSuffix(message, ": use of closed network connection") {
		d.logger.Debug("SDK connection already closed", "component", "copilot-sdk", "diagnostic", message)
	} else {
		d.logger.Error("Library diagnostic", "component", "library", "diagnostic", message)
	}
	return len(data), nil
}
