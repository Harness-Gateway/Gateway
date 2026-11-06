package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func start(cmd *exec.Cmd) (func() error, error) {
	if err := prepareBatchCommand(cmd); err != nil {
		return nil, err
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	// Assign before any CLI code can spawn children. Job membership is inherited
	// by descendants; closing our non-inherited job handle terminates them all.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	cmd.SysProcAttr.HideWindow = true
	if err := cmd.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	abort := func(err error) (func() error, error) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = windows.CloseHandle(job)
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return abort(err)
	}
	err = windows.AssignProcessToJobObject(job, process)
	_ = windows.CloseHandle(process)
	if err != nil {
		return abort(err)
	}
	if err := resumeProcess(uint32(cmd.Process.Pid)); err != nil {
		return abort(err)
	}
	return func() error { return windows.CloseHandle(job) }, nil
}

func prepareBatchCommand(cmd *exec.Cmd) error {
	ext := filepath.Ext(cmd.Path)
	if !strings.EqualFold(ext, ".cmd") && !strings.EqualFold(ext, ".bat") {
		return nil
	}
	if strings.ContainsAny(cmd.Path, "\x00\r\n\"") {
		return fmt.Errorf("invalid Windows batch CLI path")
	}
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.CmdLine != "" {
		return fmt.Errorf("custom Windows command lines are unsupported for batch CLI entry points")
	}
	var arguments strings.Builder
	var args []string
	if len(cmd.Args) > 0 {
		args = cmd.Args[1:]
	}
	for _, arg := range args {
		// Only fixed CLI startup flags cross cmd.exe. Prompts, model names and
		// other request data use stdin. Reject values requiring another shell
		// expansion pass rather than silently changing or interpreting them.
		if strings.ContainsAny(arg, "\x00\r\n\"%!") {
			return fmt.Errorf("batch CLI startup arguments cannot contain quotes, percent signs, exclamation marks or control line breaks")
		}
		arguments.WriteString(` "`)
		arguments.WriteString(arg)
		// Preserve trailing backslashes when a shim forwards %* to a native
		// executable whose argv uses Windows' standard quotation rules.
		arguments.WriteString(strings.Repeat(`\`, len(arg)-len(strings.TrimRight(arg, `\`))))
		arguments.WriteByte('"')
	}
	directory, err := windows.GetSystemDirectory()
	if err != nil {
		return fmt.Errorf("find Windows command interpreter: %w", err)
	}
	const variable = "_HARNESS_GATEWAY_BATCH_ENTRYPOINT"
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	// Expanding the path from an environment value avoids interpreting literal
	// percent signs in installation paths as environment-variable references.
	cmd.Env = append(cmd.Env, variable+"="+cmd.Path)
	interpreter := filepath.Join(directory, "cmd.exe")
	payload := `"%` + variable + `%"` + arguments.String()
	cmd.Path = interpreter
	cmd.Args = []string{interpreter, "/d", "/s", "/v:off", "/c", payload}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// /s removes the outer quotes; the inner quotes protect the executable
	// path and each argument. Disable AutoRun and delayed expansion explicitly.
	cmd.SysProcAttr.CmdLine = syscall.EscapeArg(interpreter) + ` /d /s /v:off /c "` + payload + `"`
	return nil
}

func resumeProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		count, err := windows.ResumeThread(thread)
		_ = windows.CloseHandle(thread)
		if err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
	}
	return fmt.Errorf("cannot resume suspended CLI process %d: %w", pid, err)
}
