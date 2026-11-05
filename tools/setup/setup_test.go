package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type setupFixture struct {
	bash, script, home, log string
}

func newSetupFixture(t *testing.T) setupFixture {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash setup tests require bash")
	}
	script, err := filepath.Abs("../../setup.sh")
	if err != nil {
		t.Fatal(err)
	}
	home, commands := t.TempDir(), t.TempDir()
	log := filepath.Join(home, "go-calls")
	goFixture := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$INSTALL_LOG"
[[ "${INSTALL_FAIL:-}" != 1 ]] || exit 9
mkdir -p -- "$GOBIN"
cat > "$GOBIN/harness-gateway" <<'BIN'
#!/usr/bin/env bash
printf '%s\n' 'Harness-Gateway fixture'
BIN
chmod +x "$GOBIN/harness-gateway"
`
	if err := os.WriteFile(filepath.Join(commands, "go"), []byte(goFixture), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GOBIN", "")
	t.Setenv("INSTALL_LOG", log)
	t.Setenv("INSTALL_FAIL", "")
	t.Setenv("PATH", commands+string(os.PathListSeparator)+os.Getenv("PATH"))
	return setupFixture{bash, script, home, log}
}

func (f setupFixture) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	command := exec.CommandContext(t.Context(), f.bash, append([]string{f.script}, args...)...)
	command.Dir = f.home
	output, err := command.CombinedOutput()
	return string(output), err
}

func TestSetupDefaultVersionAndIdempotentBashPath(t *testing.T) {
	f := newSetupFixture(t)
	rc := filepath.Join(f.home, ".bashrc")
	original := "# existing configuration\nexport SETUP_SENTINEL=keep\n"
	if err := os.WriteFile(rc, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if output, err := f.run(t); err != nil {
			t.Fatalf("setup failed: %v\n%s", err, output)
		}
	}
	contents, err := os.ReadFile(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(contents), original) || strings.Count(string(contents), "# Harness-Gateway command path") != 1 {
		t.Fatalf("setup replaced user settings or duplicated PATH: %s", contents)
	}
	calls, err := os.ReadFile(f.log)
	if err != nil {
		t.Fatal(err)
	}
	want := "install github.com/harness-gateway/gateway/cmd/harness-gateway@v0.1.0\n"
	if string(calls) != want+want {
		t.Fatalf("wrong Go install command: %s", calls)
	}
	command := exec.CommandContext(t.Context(), f.bash, "-c", `source "$1"; command -v harness-gateway; printf '%s\n' "$SETUP_SENTINEL"`, "bash", rc)
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "/.local/bin/harness-gateway\nkeep\n") {
		t.Fatalf("new Bash session could not resolve the command: %v\n%s", err, output)
	}
}

func TestSetupQuotesCustomPathsAndVersion(t *testing.T) {
	f := newSetupFixture(t)
	bin := filepath.Join(f.home, "bin with 'quotes' $dollar `touch PWN`")
	if output, err := f.run(t, "--version", "dev", "--bin-dir", bin); err != nil {
		t.Fatalf("setup failed: %v\n%s", err, output)
	}
	rc := filepath.Join(f.home, ".bashrc")
	command := exec.CommandContext(t.Context(), f.bash, "-c", `source "$1"; command -v harness-gateway`, "bash", rc)
	command.Dir = f.home
	output, err := command.CombinedOutput()
	canonical, canonicalErr := filepath.EvalSymlinks(bin)
	if canonicalErr != nil || err != nil || strings.TrimSpace(string(output)) != filepath.Join(canonical, "harness-gateway") {
		t.Fatalf("custom PATH did not round-trip: %v, %v\n%s", err, canonicalErr, output)
	}
	if _, err := os.Stat(filepath.Join(f.home, "PWN")); !os.IsNotExist(err) {
		t.Fatal("the bin directory was interpreted as shell code")
	}
	calls, err := os.ReadFile(f.log)
	if err != nil || !strings.Contains(string(calls), "cmd/harness-gateway@dev") {
		t.Fatalf("version override was lost: %v\n%s", err, calls)
	}
}

func TestSetupNoPathAndExistingGoBin(t *testing.T) {
	f := newSetupFixture(t)
	bin := filepath.Join(f.home, "custom-go-bin")
	t.Setenv("GOBIN", bin)
	if output, err := f.run(t, "--no-path"); err != nil {
		t.Fatalf("setup failed: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(bin, "harness-gateway")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".bashrc")); !os.IsNotExist(err) {
		t.Fatal("--no-path modified Bash startup settings")
	}
}

func TestSetupFailureDoesNotChangeBashSettings(t *testing.T) {
	for _, mode := range []string{"missing-go", "install-failed", "missing-value", "unknown-option", "colon-path", "version-whitespace"} {
		t.Run(mode, func(t *testing.T) {
			f := newSetupFixture(t)
			var args []string
			switch mode {
			case "missing-go":
				t.Setenv("PATH", "")
			case "install-failed":
				t.Setenv("INSTALL_FAIL", "1")
			case "missing-value":
				args = []string{"--version"}
			case "unknown-option":
				args = []string{"--unknown"}
			case "colon-path":
				args = []string{"--bin-dir", filepath.Join(f.home, "bad:path")}
			case "version-whitespace":
				args = []string{"--version", "bad version"}
			}
			if output, err := f.run(t, args...); err == nil {
				t.Fatalf("expected failure, got %s", output)
			}
			if _, err := os.Stat(filepath.Join(f.home, ".bashrc")); !os.IsNotExist(err) {
				t.Fatal("failed installation modified Bash startup settings")
			}
		})
	}
}

func TestSetupHelpDoesNotInstall(t *testing.T) {
	f := newSetupFixture(t)
	if output, err := f.run(t, "--help"); err != nil || !strings.Contains(output, "--no-path") {
		t.Fatalf("help failed: %v\n%s", err, output)
	}
	if _, err := os.Stat(f.log); !os.IsNotExist(err) {
		t.Fatal("help ran Go install")
	}
}
