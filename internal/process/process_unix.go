//go:build unix

package process

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

func start(cmd *exec.Cmd) (func() error, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pid := cmd.Process.Pid
	return func() error {
		err := syscall.Kill(-pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if runtime.GOOS == "darwin" && errors.Is(err, syscall.EPERM) {
			// Darwin's killpg skips zombies and can report EPERM while an
			// exited group leader is being reaped. Confirm disappearance rather
			// than suppressing a genuine permission failure on a live group.
			deadline := time.Now().Add(250 * time.Millisecond)
			for time.Now().Before(deadline) {
				probe := syscall.Kill(-pid, 0)
				if errors.Is(probe, syscall.ESRCH) {
					return nil
				}
				if !errors.Is(probe, syscall.EPERM) {
					break
				}
				time.Sleep(time.Millisecond)
			}
		}
		if err != nil {
			return fmt.Errorf("terminate CLI process group %d: %w", pid, err)
		}
		return nil
	}, nil
}
