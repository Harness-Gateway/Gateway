package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/harness-gateway/harness-gateway/internal/testutil"
)

func TestWindowsBatchCommand(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, extension := range []string{".cmd", ".bat", ".CMD", ".BAT"} {
		for _, lookup := range []bool{false, true} {
			t.Run(extension+"/PATH="+map[bool]string{false: "false", true: "true"}[lookup], func(t *testing.T) {
				t.Setenv("_HARNESS_GATEWAY_BATCH_ENTRYPOINT", "must-not-run.cmd")
				path := testutil.BatchEntryPoint(t, executable, extension)
				if lookup {
					t.Setenv("PATH", filepath.Dir(path)+string(os.PathListSeparator)+os.Getenv("PATH"))
					t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
					path = strings.TrimSuffix(filepath.Base(path), extension)
				}
				args := []string{"--flag", "two words", "", "a&b", "(x)|<y>", "a^b", `C:\trailing space\`, "Ω"}
				runFixture(t, fixtureCommand(t, path, args...), args, 0)
			})
		}
	}
}

func TestWindowsBatchRejectsInvalidExecutablePath(t *testing.T) {
	cmd := exec.Command(filepath.Join(t.TempDir(), `invalid" & echo injected & ".cmd`))
	if _, err := Start(cmd); err == nil || cmd.Process != nil {
		t.Fatalf("invalid executable path was allowed to start a shell: %v", err)
	}
}

func TestWindowsBatchExitStatus(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HG_FIXTURE_EXIT", "7")
	path := testutil.BatchEntryPoint(t, executable, ".cmd")
	args := []string{"--version"}
	runFixture(t, fixtureCommand(t, path, args...), args, 7)
}

func TestWindowsBatchRejectsAmbiguousStartupArguments(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{`a" & echo injected`, "%PATH%", "!PATH!", "\r", "\n", "\x00"} {
		t.Run(arg, func(t *testing.T) {
			path := testutil.BatchEntryPoint(t, executable, ".cmd")
			cmd := exec.Command(path, arg)
			if _, err := Start(cmd); err == nil || cmd.Process != nil {
				t.Fatalf("ambiguous argument was allowed to start a shell: %v", err)
			}
		})
	}
}
