// Package process starts CLI processes with owned, terminable process trees.
package process

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Start starts cmd and returns an idempotent, concurrency-safe tree terminator.
// The caller must call the terminator even after cmd.Wait, and must reap cmd.
// Pipe ownership, protocol handling and context policy remain with the caller.
func Start(cmd *exec.Cmd) (func() error, error) {
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	if strings.EqualFold(filepath.Ext(cmd.Path), ".js") {
		// JavaScript entry points need the same explicit interpreter for CLI
		// operations and version probes, including on Windows without shebangs.
		node, err := exec.LookPath("node")
		if err != nil {
			return nil, fmt.Errorf("find Node.js for CLI entry point: %w", err)
		}
		var args []string
		if len(cmd.Args) > 0 {
			args = cmd.Args[1:]
		}
		cmd.Args = append([]string{node, cmd.Path}, args...)
		cmd.Path = node
	}
	stop, err := start(cmd)
	if err != nil {
		return nil, err
	}
	return sync.OnceValue(stop), nil
}
