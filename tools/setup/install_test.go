package setup

import (
	"archive/zip"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVersionedGoInstallOutsideCheckout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash setup targets Unix hosts")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash setup tests require bash")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	const module = "github.com/harness-gateway/harness-gateway"
	const version = "v0.1.0"
	proxy := filepath.Join(directory, "proxy")
	versions := filepath.Join(proxy, filepath.FromSlash(module), "@v")
	if err := os.MkdirAll(versions, 0700); err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string][]byte{
		version + ".mod":  mod,
		version + ".info": []byte(`{"Version":"v0.1.0","Time":"2026-11-06T00:00:00Z"}`),
		"list":            []byte(version + "\n"),
	} {
		if err := os.WriteFile(filepath.Join(versions, name), contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	file, err := os.Create(filepath.Join(versions, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	add := func(path string) error {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entry, err := archive.Create(module + "@" + version + "/" + filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(entry, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	// Include only the command, its internal packages, and module/license files.
	// Local credentials, caches, and workspace configuration never enter the zip.
	for _, name := range []string{"go.mod", "go.sum", "LICENSE", "api/LICENSE.openai"} {
		if err := add(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"cmd/harness-gateway", "internal"} {
		if err := filepath.WalkDir(filepath.Join(root, name), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type().IsRegular() && strings.HasSuffix(path, ".go") {
				return add(path)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	goEnv := func(key string) string {
		t.Helper()
		output, err := exec.CommandContext(t.Context(), "go", "env", key).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(output))
	}
	fileURL := func(path string) string { return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String() }
	dependencyProxy := fileURL(filepath.Join(goEnv("GOMODCACHE"), "cache", "download"))
	bin := filepath.Join(directory, "installed bin")
	command := exec.CommandContext(t.Context(), bash, filepath.Join(root, "setup.sh"), "--bin-dir", bin, "--no-path")
	command.Dir = directory // No go.mod, source checkout, or Git metadata here.
	command.Env = append(os.Environ(),
		"HOME="+directory, "GOMODCACHE="+filepath.Join(directory, "module-cache"),
		"GOCACHE="+goEnv("GOCACHE"), "GOPROXY="+fileURL(proxy)+","+dependencyProxy,
		"GOSUMDB=off", "GOPRIVATE=", "GONOPROXY=none", "GOTOOLCHAIN=local", "GOFLAGS=")
	t.Cleanup(func() {
		// Go deliberately makes module-cache trees read-only. Let Go remove
		// this private cache before testing.TempDir performs its own cleanup.
		cleanup := exec.Command("go", "clean", "-modcache")
		cleanup.Dir, cleanup.Env = directory, command.Env
		if output, err := cleanup.CombinedOutput(); err != nil {
			t.Errorf("clean private module cache: %v\n%s", err, output)
		}
	})
	// This unpublished fixture and already-verified local dependency zip cache
	// are isolated from the user's module cache and require no network access.
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("versioned installation failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Harness-Gateway v0.1.0 (module v0.1.0;") {
		t.Fatalf("installed binary lost module provenance: %s", output)
	}
	output, err = exec.CommandContext(t.Context(), filepath.Join(bin, "harness-gateway"), "-v",
		"--codex-path", filepath.Join(directory, "missing-codex"), "--copilot-path", filepath.Join(directory, "missing-copilot")).CombinedOutput()
	if err != nil || !strings.Contains(string(output), "module v0.1.0") {
		t.Fatalf("installed command needs a source checkout or harness CLI: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(directory, ".bashrc")); !os.IsNotExist(err) {
		t.Fatal("--no-path changed Bash settings")
	}
}
