// Command acceptance runs Spring Boot acceptance against isolated gateways.
// The default suite uses Go CLI fixtures; -live also uses authenticated CLIs.
package main

import (
	"context"
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
	"syscall"
	"time"
)

func main() {
	live := flag.Bool("live", false, "also run authenticated Codex/Copilot inference (uses account allowance)")
	models := flag.String("models", "codex,copilot", "comma-separated model IDs for live acceptance")
	gatewayBinary := flag.String("gateway-binary", "", "existing gateway executable to test instead of building it")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "acceptance does not accept positional arguments")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, *live, *models, *gatewayBinary); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func command(ctx context.Context, name string, args ...string) error {
	fmt.Fprintln(os.Stderr, "acceptance:", name, args)
	cmd := exec.CommandContext(ctx, name, args...)
	configureCommand(cmd)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func run(ctx context.Context, live bool, models, gatewayBinary string) error {
	if _, err := os.Stat("examples/spring-ai/pom.xml"); err != nil {
		return errors.New("run acceptance from the repository root")
	}
	directory, err := os.MkdirTemp("", "harness-gateway-acceptance-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	gateway, err := resolveGateway(ctx, directory, gatewayBinary)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "acceptance: testing gateway executable", gateway)
	paths := map[string]string{}
	for _, harness := range []string{"codex", "copilot"} {
		paths[harness] = filepath.Join(directory, harness+"-fixture")
		if err := command(ctx, "go", "test", "-c", "-o", paths[harness], "./internal/adapters/outbound/"+harness); err != nil {
			return err
		}
	}
	pids := filepath.Join(directory, "pids")
	if err := os.Mkdir(pids, 0700); err != nil {
		return err
	}
	env := append(os.Environ(),
		"HARNESS_GATEWAY_CODEX_TEST=acceptance",
		"HARNESS_GATEWAY_COPILOT_TEST=acceptance",
		"HARNESS_GATEWAY_COPILOT_LOG="+filepath.Join(directory, "copilot-methods"),
		"HARNESS_GATEWAY_COPILOT_PID="+filepath.Join(directory, "copilot-pid"),
		"HARNESS_GATEWAY_ACCEPTANCE_PIDS="+pids)
	failure, err := startGateway(ctx, directory, "failure", gateway, env,
		"--codex-path", paths["codex"], "--copilot-path", paths["copilot"], "-t", "4s", "-c", "1")
	if err != nil {
		return err
	}
	err = command(ctx, "mvn", "--batch-mode", "--file", "examples/spring-ai/pom.xml",
		"-Dtest=GatewayFailureTest,GatewayStressTest", "-Dgateway.failure.url="+failure.url+"/v1",
		"-Dgateway.failure.pids="+pids, "-Dgateway.failure.directory="+directory,
		"-Dgateway.failure.executable="+gateway,
		"-Dgateway.failure.codex="+paths["codex"], "-Dgateway.failure.copilot="+paths["copilot"],
		"-Dsurefire.reportNameSuffix=failure", "test")
	stopErr := failure.stop()
	if err := errors.Join(err, stopErr); err != nil {
		return err
	}
	if live {
		server, err := startGateway(ctx, directory, "live", gateway, os.Environ())
		if err != nil {
			return err
		}
		err = command(ctx, "mvn", "--batch-mode", "--file", "examples/spring-ai/pom.xml",
			"-Dtest=GatewayTest", "-Dgateway.url="+server.url+"/v1", "-Dgateway.models="+models,
			"-Dsurefire.reportNameSuffix=live", "test")
		if err := errors.Join(err, server.stop()); err != nil {
			return err
		}
	}
	fmt.Fprintln(os.Stderr, "acceptance: passed; owned gateways stopped")
	return nil
}

func resolveGateway(ctx context.Context, directory, supplied string) (string, error) {
	if supplied != "" {
		path, err := filepath.Abs(supplied)
		if err != nil {
			return "", fmt.Errorf("gateway binary: %w", err)
		}
		path, err = exec.LookPath(path)
		if err != nil {
			return "", fmt.Errorf("gateway binary: %w", err)
		}
		return path, nil
	}
	gateway := filepath.Join(directory, "gateway")
	if err := command(ctx, "go", "build", "-o", gateway, "./cmd/harness-gateway"); err != nil {
		return "", err
	}
	return gateway, nil
}

type gatewayProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error // Read only after done closes.
	log  *os.File
	url  string
}

var listeningURL = regexp.MustCompile(`url=(http://127\.0\.0\.1:[0-9]+)`)

func startGateway(ctx context.Context, directory, name, executable string, env []string, args ...string) (*gatewayProcess, error) {
	log, err := os.Create(filepath.Join(directory, name+".log"))
	if err != nil {
		return nil, err
	}
	p := &gatewayProcess{cmd: exec.Command(executable, append([]string{"-l", "127.0.0.1:0"}, args...)...), done: make(chan struct{}), log: log}
	configureCommand(p.cmd)
	p.cmd.Env = env
	p.cmd.Stdout, p.cmd.Stderr = log, io.MultiWriter(log, os.Stderr)
	if err := p.cmd.Start(); err != nil {
		_ = log.Close()
		return nil, err
	}
	go func() { p.err = p.cmd.Wait(); close(p.done) }()
	ready, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			_ = log.Close()
			if p.err == nil {
				return nil, errors.New("gateway exited before readiness with status 0")
			}
			return nil, fmt.Errorf("gateway exited before readiness: %w", p.err)
		case <-ready.Done():
			return nil, errors.Join(fmt.Errorf("gateway readiness: %w", ready.Err()), p.stop())
		case <-ticker.C:
			data, err := os.ReadFile(log.Name())
			if err != nil {
				return nil, errors.Join(err, p.stop())
			}
			match := listeningURL.FindSubmatch(data)
			if len(match) == 0 {
				continue
			}
			p.url = string(match[1])
			request, err := http.NewRequestWithContext(ready, http.MethodGet, p.url+"/health", nil)
			if err != nil {
				return nil, errors.Join(err, p.stop())
			}
			response, err := client.Do(request)
			if err != nil {
				continue
			}
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return p, nil
			}
		}
	}
}

func (p *gatewayProcess) stop() error {
	defer p.log.Close()
	select {
	case <-p.done:
		return fmt.Errorf("gateway exited unexpectedly: %v", p.err)
	default:
	}
	if err := p.cmd.Process.Signal(os.Interrupt); err != nil {
		_ = killCommand(p.cmd)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
		return p.err
	case <-timer.C:
		_ = killCommand(p.cmd)
		<-p.done
		return errors.New("gateway did not stop gracefully within 5s")
	}
}
