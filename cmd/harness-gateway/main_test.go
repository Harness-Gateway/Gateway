package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

func TestHelpDocumentsShortAndLongOptions(t *testing.T) {
	for _, argument := range []string{"-h", "--help"} {
		t.Run(argument, func(t *testing.T) {
			var output bytes.Buffer
			if _, err := parseOptions([]string{argument}, &output); !errors.Is(err, pflag.ErrHelp) {
				t.Fatalf("help returned %v", err)
			}
			for _, want := range []string{"Usage: harness-gateway [options]", "-h, --help", "-l, --listen string", `default "127.0.0.1:8080"`, "-t, --timeout duration", "default 3m0s", "-v, --version", "-H, --harnesses", "-d, --directory", "-c, --concurrency", "--codex-path", "--copilot-path", "--codex-model", "--copilot-model"} {
				if !strings.Contains(output.String(), want) {
					t.Errorf("help missing %q:\n%s", want, &output)
				}
			}
			if strings.Contains(output.String(), "  -listen ") || strings.Contains(output.String(), "remain supported") {
				t.Errorf("help advertised single-dash long options:\n%s", &output)
			}
		})
	}
}

func TestShortAndLongOptionsAreEquivalent(t *testing.T) {
	directory := t.TempDir()
	want := options{
		address: "127.0.0.1:8090", harnesses: "codex", directory: directory,
		timeout: 45 * time.Second, concurrency: 4, version: true,
		codexPath: "missing-codex", copilotPath: "missing-copilot",
		codexModel: "test-codex-model", copilotModel: "test-copilot-model",
	}
	for name, args := range map[string][]string{
		"short":        {"-l", want.address, "-H", want.harnesses, "-d", directory, "-t", "45s", "-c", "4", "-v"},
		"long":         {"--listen", want.address, "--harnesses", want.harnesses, "--directory", directory, "--timeout", "45s", "--concurrency", "4", "--version"},
		"equals":       {"--listen=" + want.address, "--harnesses=" + want.harnesses, "--directory=" + directory, "--timeout=45s", "--concurrency=4", "--version=true"},
		"short-equals": {"-l=" + want.address, "-H=" + want.harnesses, "-d=" + directory, "-t=45s", "-c=4", "-v=true"},
	} {
		t.Run(name, func(t *testing.T) {
			args = append(args, "--codex-path", want.codexPath, "--copilot-path", want.copilotPath,
				"--codex-model", want.codexModel, "--copilot-model", want.copilotModel)
			got, err := parseOptions(args, io.Discard)
			if err != nil || got != want {
				t.Fatalf("parsed options: %+v, %v; want %+v", got, err, want)
			}
		})
	}
}

func TestOptionDefaults(t *testing.T) {
	want := options{address: "127.0.0.1:8080", harnesses: "codex,copilot", directory: ".",
		timeout: 3 * time.Minute, concurrency: 2, codexPath: "codex", copilotPath: "copilot"}
	got, err := parseOptions(nil, io.Discard)
	if err != nil || got != want {
		t.Fatalf("default options: %+v, %v; want %+v", got, err, want)
	}
}

func TestInvalidOptions(t *testing.T) {
	for _, args := range [][]string{
		{"-listen", "127.0.0.1:8090"}, {"-version"}, {"--unknown"}, {"-z"},
		{"--listen"}, {"--timeout", "invalid"}, {"--concurrency", "invalid"},
		{"unexpected"}, {"--listen", "127.0.0.1:8090", "unexpected"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, err := parseOptions(args, io.Discard); err == nil || errors.Is(err, pflag.ErrHelp) {
				t.Fatalf("invalid options %v returned %v", args, err)
			}
		})
	}
}

func TestUnauthenticatedListenerRequiresLoopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8080", "[::1]:8080", "localhost:8080"} {
		if err := loopbackAddress(address); err != nil {
			t.Errorf("rejected local address %q: %v", address, err)
		}
	}
	for _, address := range []string{":8080", "0.0.0.0:8080", "[::]:8080", "192.168.1.1:8080", "example.com:8080", "invalid"} {
		if err := loopbackAddress(address); err == nil {
			t.Errorf("accepted nonlocal address %q", address)
		}
	}
}
