package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

const versionProbeTimeout = 2 * time.Second
const maxVersionOutput = 4096

var errVersionOutputLimit = errors.New("version output exceeds 4 KiB")
var versionNumber = regexp.MustCompile(`v?([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?)`)
var versionANSI = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]")

// Only enabled, already-resolved executable paths are queried. Checks run in
// parallel, have no terminal input, and never perform a model/authentication probe.
func identifyHarnessVersions(ctx context.Context, harnesses []harnessStatus) []harnessStatus {
	result := append([]harnessStatus(nil), harnesses...)
	var workers sync.WaitGroup
	for i := range result {
		workers.Go(func() {
			result[i].version, result[i].versionError = probeHarnessVersion(ctx, result[i].name, result[i].executable)
		})
	}
	workers.Wait()
	return result
}

func probeHarnessVersion(parent context.Context, name, executable string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, versionProbeTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "--version")
	configureVersionProcess(command)
	var output versionOutput
	command.Stdout, command.Stderr = &output, io.Discard
	runErr := command.Run()
	cleanupErr := stopVersionProcess(command)
	if errors.Is(cleanupErr, os.ErrProcessDone) {
		cleanupErr = nil
	}
	if output.exceeded {
		runErr = errors.Join(errVersionOutputLimit, runErr)
	}
	if err := errors.Join(ctx.Err(), runErr, cleanupErr); err != nil {
		return "", fmt.Errorf("--version failed: %w", err)
	}
	return parseHarnessVersion(name, string(output.data))
}

func parseHarnessVersion(name, output string) (string, error) {
	var bare string
	for _, line := range strings.Split(versionANSI.ReplaceAllString(output, ""), "\n") {
		line = strings.TrimSpace(line)
		match := versionNumber.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, name+" ") || strings.HasPrefix(lower, name+"-cli ") || strings.HasPrefix(lower, "github "+name+" ") {
			return match[1], nil
		}
		if line == match[0] {
			bare = match[1]
		}
	}
	if bare != "" {
		return bare, nil
	}
	return "", errors.New("unrecognized --version output")
}

type versionOutput struct {
	data     []byte
	exceeded bool
}

func (output *versionOutput) Write(data []byte) (int, error) {
	remaining := maxVersionOutput - len(output.data)
	if len(data) > remaining {
		output.exceeded = true
		output.data = append(output.data, data[:remaining]...)
		return remaining, errVersionOutputLimit
	}
	output.data = append(output.data, data...)
	return len(data), nil
}
