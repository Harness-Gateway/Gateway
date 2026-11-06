package setup

import (
	"archive/zip"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/harness-gateway/harness-gateway/internal/buildinfo"
)

const modulePath = "github.com/harness-gateway/harness-gateway"
const moduleVersion = "v" + buildinfo.Version

type installFixture struct {
	root, directory, bin string
	env                  []string
}

func newInstallFixture(t *testing.T) installFixture {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	const module = modulePath
	const version = moduleVersion
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
		version + ".info": []byte(fmt.Sprintf(`{"Version":%q,"Time":"2026-11-06T00:00:00Z"}`, version)),
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
	// Include only the command, its packages and embedded assets, and module/license files.
	// Local credentials, caches, and workspace configuration never enter the zip.
	for _, name := range []string{"go.mod", "go.sum", "LICENSE", "api/LICENSE.openai"} {
		if err := add(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"cmd/harness-gateway", "internal", "api"} {
		if err := filepath.WalkDir(filepath.Join(root, name), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			docsAsset := strings.HasPrefix(relative, "internal/adapters/inbound/openai/docs/") &&
				(strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".html") || entry.Name() == "LICENSE.scalar")
			if entry.Type().IsRegular() && (strings.HasSuffix(path, ".go") || relative == "api/openapi.json" || docsAsset) {
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
	fileURL := func(path string) string {
		path = filepath.ToSlash(path)
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		return (&url.URL{Scheme: "file", Path: path}).String()
	}
	dependencyProxy := fileURL(filepath.Join(goEnv("GOMODCACHE"), "cache", "download"))
	bin := filepath.Join(directory, "installed bin")
	env := append(os.Environ(),
		"GOBIN="+bin, "GOMODCACHE="+filepath.Join(directory, "module-cache"),
		"GOCACHE="+goEnv("GOCACHE"), "GOPROXY="+fileURL(proxy)+","+dependencyProxy,
		"GOSUMDB=off", "GOPRIVATE=", "GONOPROXY=none", "GONOSUMDB=", "GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=")
	t.Cleanup(func() {
		// Go deliberately makes module-cache trees read-only. Let Go remove
		// this private cache before testing.TempDir performs its own cleanup.
		cleanup := exec.Command("go", "clean", "-modcache")
		cleanup.Dir, cleanup.Env = directory, env
		if output, err := cleanup.CombinedOutput(); err != nil {
			t.Errorf("clean private module cache: %v\n%s", err, output)
		}
	})
	return installFixture{root, directory, bin, env}
}

func TestVersionedGoInstallOutsideCheckout(t *testing.T) {
	f := newInstallFixture(t)
	command := exec.CommandContext(t.Context(), "go", "install", modulePath+"/cmd/harness-gateway@"+moduleVersion)
	command.Dir, command.Env = f.directory, f.env
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("versioned Go installation failed: %v\n%s", err, output)
	}
	f.verify(t)
}

func TestVersionedBashInstallOutsideCheckout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash setup targets Unix hosts; native Go installation is tested separately")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash setup tests require bash")
	}
	f := newInstallFixture(t)
	command := exec.CommandContext(t.Context(), bash, filepath.Join(f.root, "setup.sh"), "--bin-dir", f.bin, "--no-path")
	command.Dir, command.Env = f.directory, append(f.env, "HOME="+f.directory)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("versioned installation failed: %v\n%s", err, output)
	}
	f.verify(t)
	if _, err := os.Stat(filepath.Join(f.directory, ".bashrc")); !os.IsNotExist(err) {
		t.Fatal("--no-path changed Bash settings")
	}
}

func (f installFixture) verify(t *testing.T) {
	t.Helper()
	name := "harness-gateway"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	command := exec.CommandContext(t.Context(), filepath.Join(f.bin, name), "--version",
		"--codex-path", filepath.Join(f.directory, "missing-codex"), "--copilot-path", filepath.Join(f.directory, "missing-copilot"))
	command.Dir = f.directory
	output, err := command.CombinedOutput()
	want := "Harness-Gateway " + moduleVersion + " (module " + moduleVersion + ";"
	if err != nil || !strings.Contains(string(output), want) {
		t.Fatalf("installed command lost provenance or needs checkout/CLIs: %v\n%s", err, output)
	}
}
