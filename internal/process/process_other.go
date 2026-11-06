//go:build !unix && !windows

package process

import (
	"errors"
	"os/exec"
)

func start(cmd *exec.Cmd) (func() error, error) {
	return nil, errors.New("CLI process-tree ownership requires Unix or Windows")
}
