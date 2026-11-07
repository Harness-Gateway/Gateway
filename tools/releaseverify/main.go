// Command releaseverify checks public Go/Bash release installs without credentials.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/process"
)

const modulePath = "github.com/harness-gateway/harness-gateway"

var releaseVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type verification struct {
	Version    string `json:"version"`
	Revision   string `json:"revision"`
	ModuleSum  string `json:"module_sum"`
	SHA256     string `json:"sha256"`
	GoBinary   string `json:"go_binary"`
	BashBinary string `json:"bash_binary"`
	HTTPChecks int    `json:"http_checks"`
}

type moduleInfo struct {
	Path, Version, Dir, Sum string
	Origin                  struct{ Hash, Ref string }
}

func main() {
	version := flag.String("version", "", "published stable version, e.g. v0.1.7 (required)")
	revision := flag.String("revision", "", "expected full tag commit SHA (required)")
	output := flag.String("output-dir", "", "parent for retained binaries, logs and verification.json (default: system temp)")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 10*time.Minute)
	defer timeout()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "releaseverify does not accept positional arguments")
		os.Exit(2)
	}
	result, err := verify(ctx, *version, *revision, *output)
	if err != nil {
		fmt.Fprintln(os.Stderr, "releaseverify:", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func verify(ctx context.Context, version, revision, parent string) (result verification, runErr error) {
	if !releaseVersion.MatchString(version) || !revisionPattern.MatchString(revision) {
		return result, errors.New("provide a stable --version vMAJOR.MINOR.PATCH and its full lowercase --revision SHA")
	}
	if runtime.GOOS == "windows" {
		return result, errors.New("Bash release verification requires Linux or macOS; native Windows installation is covered by Validate CI")
	}
	repo, err := filepath.Abs(".")
	if err != nil {
		return result, err
	}
	if _, err := os.Stat(filepath.Join(repo, "go.mod")); err != nil {
		return result, errors.New("run releaseverify from the repository root")
	}
	if parent != "" {
		parent, err = filepath.Abs(parent)
		if err != nil {
			return result, err
		}
		if err := os.MkdirAll(parent, 0700); err != nil {
			return result, err
		}
	}
	root, err := os.MkdirTemp(parent, "harness-gateway-release-")
	if err != nil {
		return result, err
	}
	fmt.Fprintln(os.Stderr, "releaseverify: retained output", root)
	work, err := os.MkdirTemp("", "harness-gateway-public-install-")
	if err != nil {
		return result, err
	}
	env := publicEnvironment(os.Environ(), work)
	defer func() {
		// Module-cache trees are read-only. Ask Go to clean only our private cache.
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, cleanErr := runCommand(cleanup, work, env, "go", "clean", "-modcache")
		runErr = errors.Join(runErr, cleanErr, os.RemoveAll(work))
	}()
	goBin, bashBin := filepath.Join(root, "go-bin"), filepath.Join(root, "bash-bin")
	for _, directory := range []string{goBin, bashBin} {
		if err := os.Mkdir(directory, 0700); err != nil {
			return result, err
		}
	}
	if _, err := runCommand(ctx, work, append(env, "GOBIN="+goBin), "go", "install", modulePath+"/cmd/harness-gateway@"+version); err != nil {
		return result, err
	}
	metadata, err := runCommand(ctx, work, env, "go", "mod", "download", "-json", modulePath+"@"+version)
	if err != nil {
		return result, err
	}
	var module moduleInfo
	if err := json.Unmarshal(metadata, &module); err != nil {
		return result, err
	}
	if err := validateModule(module, version, revision); err != nil {
		return result, err
	}
	lookup := filepath.Join(work, "module-cache", "cache", "download", "sumdb", "sum.golang.org", "lookup", filepath.FromSlash(modulePath+"@"+version))
	lookupData, err := os.ReadFile(lookup)
	if err != nil {
		return result, fmt.Errorf("public checksum database receipt: %w", err)
	}
	if !strings.Contains(string(lookupData), modulePath+" "+version+" "+module.Sum+"\n") {
		return result, errors.New("checksum database receipt does not contain the verified module sum")
	}
	scriptURL := "https://raw.githubusercontent.com/Harness-Gateway/harness-gateway/" + version + "/setup.sh"
	script, err := download(ctx, scriptURL)
	if err != nil {
		return result, err
	}
	moduleScript, err := os.ReadFile(filepath.Join(module.Dir, "setup.sh"))
	if err != nil {
		return result, err
	}
	if !bytes.Equal(script, moduleScript) {
		return result, errors.New("tagged setup.sh differs from the checksum-verified module")
	}
	scriptPath := filepath.Join(work, "setup.sh")
	if err := os.WriteFile(scriptPath, script, 0600); err != nil {
		return result, err
	}
	// Exercise the tagged script's default version, including its --no-path path.
	if _, err := runCommand(ctx, work, env, "bash", scriptPath, "--bin-dir", bashBin, "--no-path"); err != nil {
		return result, err
	}
	if _, err := os.Stat(filepath.Join(work, "home", ".bashrc")); !os.IsNotExist(err) {
		return result, errors.New("--no-path changed Bash configuration")
	}
	result = verification{Version: version, Revision: revision, ModuleSum: module.Sum, GoBinary: filepath.Join(goBin, "harness-gateway"), BashBinary: filepath.Join(bashBin, "harness-gateway")}
	var digest [32]byte
	for i, binary := range []string{result.GoBinary, result.BashBinary} {
		info, err := buildinfo.ReadFile(binary)
		if err != nil {
			return result, err
		}
		if info.Main.Path != modulePath || info.Main.Version != version || info.Main.Replace != nil {
			return result, fmt.Errorf("wrong installed module identity in %s", binary)
		}
		versionOutput, err := runCommand(ctx, work, env, binary, "--version")
		if err != nil {
			return result, err
		}
		if !strings.HasPrefix(string(versionOutput), "Harness-Gateway "+version+" (module "+version+";") {
			return result, fmt.Errorf("wrong version output: %s", versionOutput)
		}
		data, err := os.ReadFile(binary)
		if err != nil {
			return result, err
		}
		hash := sha256.Sum256(data)
		if i == 0 {
			digest = hash
		} else if hash != digest {
			return result, errors.New("Go and Bash installations produced different executable bytes")
		}
	}
	result.SHA256 = fmt.Sprintf("%x", digest)
	fixtures := make(map[string]string)
	for _, harness := range []string{"codex", "copilot"} {
		fixtures[harness] = filepath.Join(work, harness+"-fixture")
		if _, err := runCommand(ctx, repo, os.Environ(), "go", "test", "-c", "-o", fixtures[harness], "./internal/adapters/outbound/"+harness); err != nil {
			return result, err
		}
	}
	for _, binary := range []string{result.GoBinary, result.BashBinary} {
		checks, err := smoke(ctx, root, binary, version, fixtures)
		if err != nil {
			return result, err
		}
		result.HTTPChecks += checks
	}
	manifest, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return result, err
	}
	if err := os.WriteFile(filepath.Join(root, "verification.json"), append(manifest, '\n'), 0600); err != nil {
		return result, err
	}
	return result, nil
}

func validateModule(module moduleInfo, version, revision string) error {
	if module.Path != modulePath || module.Version != version || module.Origin.Hash != revision || module.Origin.Ref != "refs/tags/"+version || !strings.HasPrefix(module.Sum, "h1:") || module.Dir == "" {
		return errors.New("public module version, tag revision, origin ref or checksum does not match the requested release")
	}
	return nil
}

func publicEnvironment(inherited []string, work string) []string {
	// Clear persistent go env settings and private/proxy shortcuts for this install.
	overrides := []string{"GOENV=off", "GOPROXY=https://proxy.golang.org", "GOSUMDB=sum.golang.org", "GOPRIVATE=", "GONOSUMDB=", "GONOPROXY=", "GOINSECURE=", "GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=", "GOBIN=", "GOMODCACHE=" + filepath.Join(work, "module-cache"), "HOME=" + filepath.Join(work, "home")}
	names := make(map[string]bool)
	for _, value := range overrides {
		name, _, _ := strings.Cut(value, "=")
		names[name] = true
	}
	var env []string
	for _, value := range inherited {
		name, _, _ := strings.Cut(value, "=")
		if !names[name] {
			env = append(env, value)
		}
	}
	return append(env, overrides...)
}

func runCommand(ctx context.Context, directory string, env []string, name string, args ...string) ([]byte, error) {
	fmt.Fprintln(os.Stderr, "releaseverify:", name, strings.Join(args, " "))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env, cmd.Stderr, cmd.WaitDelay = directory, env, os.Stderr, 2*time.Second
	var output bytes.Buffer
	cmd.Stdout = &output
	stop, err := process.Start(cmd)
	if err != nil {
		return nil, err
	}
	stopOnCancel := context.AfterFunc(ctx, func() { _ = stop() })
	err = cmd.Wait()
	stopOnCancel()
	err = errors.Join(err, stop(), ctx.Err())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return output.Bytes(), nil
}

func download(ctx context.Context, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tagged setup script: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 64*1024 {
		return nil, errors.New("tagged setup script exceeds 64 KiB")
	}
	return data, nil
}
