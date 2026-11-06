package process

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type commandRecord struct {
	Args        []string
	Input, CWD  string
	Environment string
}

func TestMain(tests *testing.M) {
	if os.Getenv("HARNESS_GATEWAY_PROCESS_FIXTURE") == "1" {
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(90)
		}
		directory, _ := os.Getwd()
		if err := json.NewEncoder(os.Stdout).Encode(commandRecord{os.Args[1:], string(input), directory, os.Getenv("HG_PRESERVED_ENV")}); err != nil {
			os.Exit(91)
		}
		code, _ := strconv.Atoi(os.Getenv("HG_FIXTURE_EXIT"))
		os.Exit(code)
	}
	os.Exit(tests.Run())
}

func fixtureCommand(t *testing.T, executable string, args ...string) *exec.Cmd {
	t.Helper()
	t.Setenv("HARNESS_GATEWAY_PROCESS_FIXTURE", "1")
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	cmd := exec.Command(executable, args...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "HG_PRESERVED_ENV=literal ^&%! Ω")
	// Request text never belongs to the shell's command line.
	cmd.Stdin = strings.NewReader("a prompt with \"quotes\", %PATH%, !x! & echo injected > must-not-exist\r\nΩ")
	cmd.WaitDelay = time.Second
	return cmd
}

func runFixture(t *testing.T, cmd *exec.Cmd, wantArgs []string, wantExit int) {
	t.Helper()
	var output, diagnostics bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &diagnostics
	stop, err := Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	stopOnCancel := context.AfterFunc(ctx, func() { _ = stop() })
	err = cmd.Wait()
	stopOnCancel()
	if cleanupErr := stop(); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	if wantExit == 0 && err != nil {
		t.Fatalf("command failed: %v; stderr=%s", err, diagnostics.String())
	}
	if wantExit != 0 {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != wantExit {
			t.Fatalf("lost exit status: %v; stderr=%s", err, diagnostics.String())
		}
	}
	var got commandRecord
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatalf("invalid fixture output: %v; stdout=%q stderr=%q", err, output.String(), diagnostics.String())
	}
	wantDirectory, wantErr := os.Stat(cmd.Dir)
	gotDirectory, gotErr := os.Stat(got.CWD)
	if wantErr != nil || gotErr != nil || !os.SameFile(wantDirectory, gotDirectory) {
		t.Fatalf("working directory changed: got=%q want=%q errors=%v/%v", got.CWD, cmd.Dir, gotErr, wantErr)
	}
	if !reflect.DeepEqual(got.Args, wantArgs) || got.Input != "a prompt with \"quotes\", %PATH%, !x! & echo injected > must-not-exist\r\nΩ" || got.Environment != "literal ^&%! Ω" {
		t.Fatalf("command context or data changed: %+v; expected args=%q directory=%q", got, wantArgs, cmd.Dir)
	}
	if _, err := os.Stat(filepath.Join(cmd.Dir, "must-not-exist")); !os.IsNotExist(err) {
		t.Fatal("request text was interpreted by a shell")
	}
}

func TestNativeCommandPreservesArgumentsAndContext(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--flag", "two words", "", `quote"inside`, "%PATH%", "!bang!", "^&|<>()", `C:\trailing space\`, "Ω"}
	runFixture(t, fixtureCommand(t, executable, args...), args, 0)
}

func TestJavaScriptEntryPoint(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("JavaScript entry-point integration requires Node.js")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HG_NATIVE_FIXTURE", executable)
	path := filepath.Join(t.TempDir(), "CLI tools & (Ω)! %.JS")
	const script = `const fs = require('node:fs');
const {spawnSync} = require('node:child_process');
const result = spawnSync(process.env.HG_NATIVE_FIXTURE, process.argv.slice(2), {
  input: fs.readFileSync(0), cwd: process.cwd(), env: process.env
});
if (result.error) { console.error(result.error); process.exit(92); }
process.stdout.write(result.stdout);
process.stderr.write(result.stderr);
process.exit(result.status === null ? 93 : result.status);
`
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--flag", `quotes " and %PATH% & !`, `trailing\`, "Ω"}
	runFixture(t, fixtureCommand(t, path, args...), args, 0)
}
