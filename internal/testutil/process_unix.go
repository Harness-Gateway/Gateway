//go:build unix

// Package testutil contains shared subprocess assertions for integration fixtures.
package testutil

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

func ProcessRunning(pid int) (bool, error) {
	if err := syscall.Kill(pid, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return false, nil
		}
		return false, err
	}
	if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		if _, state, ok := strings.Cut(string(stat), ") "); ok && strings.HasPrefix(state, "Z") {
			return false, nil
		}
	}
	return true, nil
}
