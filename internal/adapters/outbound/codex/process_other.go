//go:build !unix

package codex

import "os/exec"

func configureProcess(cmd *exec.Cmd) { cmd.WaitDelay = processWait }
