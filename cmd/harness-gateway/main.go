package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/adapters/inbound/openai"
	"github.com/harness-gateway/harness-gateway/internal/adapters/outbound/codex"
	"github.com/harness-gateway/harness-gateway/internal/adapters/outbound/copilot"
	"github.com/harness-gateway/harness-gateway/internal/application"
	"github.com/harness-gateway/harness-gateway/internal/buildinfo"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and build identity")
	address := flag.String("listen", "127.0.0.1:8080", "loopback HTTP listen address")
	harnesses := flag.String("harnesses", "codex,copilot", "comma-separated enabled harnesses")
	directory := flag.String("directory", ".", "working directory for CLI requests")
	timeout := flag.Duration("timeout", 3*time.Minute, "maximum duration of a completion")
	concurrency := flag.Int("concurrency", 2, "maximum simultaneous completions")
	codexPath := flag.String("codex-path", "codex", "Codex CLI executable")
	copilotPath := flag.String("copilot-path", "copilot", "Copilot CLI executable")
	codexModel := flag.String("codex-model", "", "native Codex model (empty uses CLI default)")
	copilotModel := flag.String("copilot-model", "", "native Copilot model (empty uses CLI default)")
	flag.Parse()
	if *showVersion {
		fmt.Println(buildinfo.String())
		return
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(*address, *harnesses, *directory, *timeout, *concurrency, *codexPath, *copilotPath, *codexModel, *copilotModel, logger); err != nil {
		logger.Error("gateway stopped", "error", err)
		os.Exit(1)
	}
}

func run(address, enabled, directory string, timeout time.Duration, concurrency int, codexPath, copilotPath, codexModel, copilotModel string, logger *slog.Logger) error {
	if err := loopbackAddress(address); err != nil {
		return err
	}
	if timeout <= 0 || concurrency < 1 {
		return errors.New("timeout and concurrency must be positive")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	info, err := os.Stat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("directory must be a directory")
	}
	var backends []application.Backend
	seen := map[string]bool{}
	for _, name := range strings.Split(enabled, ",") {
		name = strings.TrimSpace(name)
		if seen[name] {
			return fmt.Errorf("duplicate harness %q", name)
		}
		seen[name] = true
		switch name {
		case "codex":
			path, err := exec.LookPath(codexPath)
			if err != nil {
				return fmt.Errorf("find codex CLI: %w", err)
			}
			path, err = filepath.Abs(path)
			if err != nil {
				return err
			}
			backends = append(backends, application.Backend{Model: application.Model{ID: name, Provider: name}, Harness: &codex.Harness{Path: path, Model: codexModel, Directory: directory}})
		case "copilot":
			path, err := exec.LookPath(copilotPath)
			if err != nil {
				return fmt.Errorf("find copilot CLI: %w", err)
			}
			path, err = filepath.Abs(path)
			if err != nil {
				return err
			}
			backends = append(backends, application.Backend{Model: application.Model{ID: name, Provider: name}, Harness: &copilot.Harness{Path: path, Model: copilotModel, Directory: directory}})
		default:
			return fmt.Errorf("unknown harness %q", name)
		}
	}
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root, cancelRequests := context.WithCancelCause(context.Background())
	defer cancelRequests(context.Canceled)
	server := &http.Server{
		Addr:              address,
		Handler:           openai.New(application.New(backends, concurrency), timeout, logger),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
		BaseContext: func(net.Listener) context.Context { return root },
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	logger.Info("Harness-Gateway listening", "url", "http://"+listener.Addr().String(), "harnesses", enabled, "directory", directory)
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	select {
	case err := <-stopped:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-signals.Done():
		cancelRequests(openai.ErrShuttingDown)
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}

func loopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if host != "localhost" && !net.ParseIP(host).IsLoopback() {
		return errors.New("this unauthenticated gateway requires a loopback listen address")
	}
	return nil
}
