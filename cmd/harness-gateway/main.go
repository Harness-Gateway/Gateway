package main

import (
	"context"
	"errors"
	"fmt"
	"io"
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

	"github.com/spf13/pflag"

	"github.com/harness-gateway/harness-gateway/internal/adapters/inbound/openai"
	"github.com/harness-gateway/harness-gateway/internal/adapters/outbound/codex"
	"github.com/harness-gateway/harness-gateway/internal/adapters/outbound/copilot"
	"github.com/harness-gateway/harness-gateway/internal/application"
	"github.com/harness-gateway/harness-gateway/internal/buildinfo"
)

func main() {
	options, err := parseOptions(os.Args[1:], os.Stderr)
	if errors.Is(err, pflag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\nRun 'harness-gateway --help' for usage.\n", err)
		os.Exit(2)
	}
	if options.version {
		fmt.Println(buildinfo.String())
		return
	}
	logger := newLogger(os.Stderr, options.logFormat)
	if err := run(options, logger); err != nil {
		logger.Error("gateway stopped", "error", err)
		os.Exit(1)
	}
}

type options struct {
	address, harnesses, directory string
	logFormat                     string
	codexPath, copilotPath        string
	codexModel, copilotModel      string
	timeout                       time.Duration
	concurrency                   int
	version                       bool
}

func parseOptions(args []string, output io.Writer) (options, error) {
	var options options
	flags := pflag.NewFlagSet("harness-gateway", pflag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() {
		fmt.Fprintln(output, "Usage: harness-gateway [options]\n\nOptions:")
		flags.PrintDefaults()
	}
	help := flags.BoolP("help", "h", false, "show this help")
	flags.BoolVarP(&options.version, "version", "v", false, "print version and build identity")
	flags.StringVarP(&options.address, "listen", "l", "127.0.0.1:8080", "loopback HTTP listen address")
	flags.StringVarP(&options.harnesses, "harnesses", "H", "codex,copilot", "comma-separated enabled harnesses")
	flags.StringVarP(&options.directory, "directory", "d", ".", "working directory for CLI requests")
	flags.DurationVarP(&options.timeout, "timeout", "t", 3*time.Minute, "maximum duration of a completion")
	flags.IntVarP(&options.concurrency, "concurrency", "c", 2, "maximum simultaneous completions")
	flags.StringVar(&options.logFormat, "log-format", "text", "log format: text or json (JSON disables the terminal startup panel)")
	flags.StringVar(&options.codexPath, "codex-path", "codex", "Codex CLI executable")
	flags.StringVar(&options.copilotPath, "copilot-path", "copilot", "Copilot CLI executable")
	flags.StringVar(&options.codexModel, "codex-model", "", "native Codex model (empty uses CLI default)")
	flags.StringVar(&options.copilotModel, "copilot-model", "", "native Copilot model (empty uses CLI default)")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if *help {
		flags.Usage()
		return options, pflag.ErrHelp
	}
	if flags.NArg() != 0 {
		return options, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if options.logFormat != "text" && options.logFormat != "json" {
		return options, errors.New("log-format must be text or json")
	}
	return options, nil
}

func run(options options, logger *slog.Logger) error {
	restoreDiagnostics := routeLibraryDiagnostics(logger)
	defer restoreDiagnostics()
	if err := loopbackAddress(options.address); err != nil {
		return err
	}
	if options.timeout <= 0 || options.concurrency < 1 {
		return errors.New("timeout and concurrency must be positive")
	}
	directory, err := filepath.Abs(options.directory)
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
	var harnesses []harnessStatus
	seen := map[string]bool{}
	for _, name := range strings.Split(options.harnesses, ",") {
		name = strings.TrimSpace(name)
		if seen[name] {
			return fmt.Errorf("duplicate harness %q", name)
		}
		seen[name] = true
		switch name {
		case "codex":
			path, err := exec.LookPath(options.codexPath)
			if err != nil {
				return fmt.Errorf("find codex CLI: %w", err)
			}
			path, err = filepath.Abs(path)
			if err != nil {
				return err
			}
			backends = append(backends, application.Backend{Model: application.Model{ID: name, Provider: name}, Harness: &codex.Harness{Path: path, Model: options.codexModel, Directory: directory}})
			harnesses = append(harnesses, harnessStatus{name: name, executable: path, model: options.codexModel})
		case "copilot":
			path, err := exec.LookPath(options.copilotPath)
			if err != nil {
				return fmt.Errorf("find copilot CLI: %w", err)
			}
			path, err = filepath.Abs(path)
			if err != nil {
				return err
			}
			backends = append(backends, application.Backend{Model: application.Model{ID: name, Provider: name}, Harness: &copilot.Harness{Path: path, Model: options.copilotModel, Directory: directory}})
			harnesses = append(harnesses, harnessStatus{name: name, executable: path, model: options.copilotModel})
		default:
			return fmt.Errorf("unknown harness %q", name)
		}
	}
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	harnesses = identifyHarnessVersions(signals, harnesses)
	if signals.Err() != nil {
		return nil
	}
	for _, harness := range harnesses {
		if harness.versionError != nil {
			logger.Warn("Harness CLI version unavailable", "harness", harness.name, "executable", harness.executable, "error", harness.versionError)
		}
	}
	root, cancelRequests := context.WithCancelCause(context.Background())
	defer cancelRequests(context.Canceled)
	server := &http.Server{
		Addr:              options.address,
		Handler:           openai.New(application.New(backends, options.concurrency), options.timeout, logger),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
		BaseContext: func(net.Listener) context.Context { return root },
	}
	listener, err := net.Listen("tcp", options.address)
	if err != nil {
		return err
	}
	startup := startupStatus{baseURL: "http://" + listener.Addr().String(), directory: directory, timeout: options.timeout, concurrency: options.concurrency, harnesses: harnesses}
	if err := reportStartup(os.Stderr, logger, options.logFormat, startup); err != nil {
		return errors.Join(err, listener.Close())
	}
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	select {
	case err := <-stopped:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-signals.Done():
		logger.Info("Harness-Gateway shutting down")
		cancelRequests(openai.ErrShuttingDown)
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		logger.Info("Harness-Gateway stopped")
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
