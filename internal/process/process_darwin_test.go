//go:build darwin

package process

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestStopWhileExitedLeaderIsBeingReaped(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	stop, err := Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		info, err := unix.SysctlKinfoProc("kern.proc.pid", cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		// Darwin's SZOMB state is 5: keep the exited leader unreaped until
		// the group terminator encounters the kernel's zombie-only group.
		if info.Proc.P_stat == 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture never entered the zombie state")
		}
		time.Sleep(time.Millisecond)
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("expected Darwin's zombie-group EPERM, got %v", err)
	}
	reaped := make(chan error, 1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		reaped <- cmd.Wait()
	}()
	err = stop()
	if waitErr := <-reaped; waitErr != nil {
		t.Fatal(waitErr)
	}
	if err != nil {
		t.Fatalf("reaped group was reported as a cleanup failure: %v", err)
	}
	if err := stop(); err != nil {
		t.Fatalf("repeated cleanup changed its result: %v", err)
	}
}
