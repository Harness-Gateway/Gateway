//go:build !unix

package main

import (
	"os/exec"
	"time"
)

func configureVersionProcess(command *exec.Cmd) {
	command.WaitDelay = 200 * time.Millisecond
}

func stopVersionProcess(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	return command.Process.Kill()
}
