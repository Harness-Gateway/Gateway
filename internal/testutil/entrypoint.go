package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// InstalledCLI gates real-package transport smoke tests. They perform version
// queries/handshakes only, never model inference or interactive authentication.
func InstalledCLI(t testing.TB, name string) string {
	t.Helper()
	if os.Getenv("HARNESS_GATEWAY_TEST_INSTALLED_CLIS") != "1" {
		t.Skip("set HARNESS_GATEWAY_TEST_INSTALLED_CLIS=1 to test installed CLI packages without inference")
	}
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// CLIEntryPoint optionally routes existing protocol fixtures through a real
// Windows batch shim. Ordinary test runs continue exercising native executables.
func CLIEntryPoint(t testing.TB, executable string) string {
	t.Helper()
	extension := os.Getenv("HARNESS_GATEWAY_TEST_CLI_WRAPPER")
	if extension == "" {
		return executable
	}
	return BatchEntryPoint(t, executable, extension)
}

func BatchEntryPoint(t testing.TB, executable, extension string) string {
	t.Helper()
	if runtime.GOOS != "windows" || (!strings.EqualFold(extension, ".cmd") && !strings.EqualFold(extension, ".bat")) {
		t.Fatalf("invalid Windows batch fixture extension %q on %s", extension, runtime.GOOS)
	}
	// Check literal percent expansion, delayed expansion, Unicode, spaces, and
	// cmd.exe metacharacters in the resolved executable's directory and name.
	directory := filepath.Join(t.TempDir(), "CLI tools & (Ω)! %HG_SHIM_SENTINEL%")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HG_SHIM_SENTINEL", "must-not-expand")
	t.Setenv("_HARNESS_GATEWAY_FIXTURE_EXECUTABLE", executable)
	path := filepath.Join(directory, "harness & (entry)! %HG_SHIM_SENTINEL%"+extension)
	body := "@echo off\r\nsetlocal DisableDelayedExpansion\r\n\"%_HARNESS_GATEWAY_FIXTURE_EXECUTABLE%\" %*\r\nexit /b %errorlevel%\r\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
