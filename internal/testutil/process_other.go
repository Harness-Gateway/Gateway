//go:build !unix && !windows

package testutil

import "errors"

func ProcessRunning(pid int) (bool, error) {
	return false, errors.New("process assertions require Unix or Windows")
}
