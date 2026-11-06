// Package process starts CLI processes with owned, terminable process trees.
package process

import (
	"os/exec"
	"sync"
)

// Start starts cmd and returns an idempotent, concurrency-safe tree terminator.
// The caller must call the terminator even after cmd.Wait, and must reap cmd.
// Pipe ownership, protocol handling and context policy remain with the caller.
func Start(cmd *exec.Cmd) (func() error, error) {
	stop, err := start(cmd)
	if err != nil {
		return nil, err
	}
	return sync.OnceValue(stop), nil
}
