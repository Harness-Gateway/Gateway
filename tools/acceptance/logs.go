package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// Logs live separately from disposable executables so a failed run can retain
// all gateway diagnostics without retaining gateway/fixture binaries.
func createLogsDirectory(parent string) (string, error) {
	if parent != "" {
		absolute, err := filepath.Abs(parent)
		if err != nil {
			return "", err
		}
		parent = absolute
		if err := os.MkdirAll(parent, 0700); err != nil {
			return "", err
		}
	}
	return os.MkdirTemp(parent, "harness-gateway-acceptance-logs-")
}

func finishLogs(directory string, keep bool, runErr error) error {
	if keep || runErr != nil {
		fmt.Fprintln(os.Stderr, "acceptance: logs retained in", directory)
		return runErr
	}
	return os.RemoveAll(directory)
}
