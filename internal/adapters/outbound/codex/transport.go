package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/buildinfo"
	"github.com/harness-gateway/harness-gateway/internal/domain"
	"github.com/harness-gateway/harness-gateway/internal/process"
)

const processWait = 2 * time.Second

// A UTF-8 byte can expand to six bytes (\u0000) in a JSON string. Reserve
// another MiB for the protocol envelope, while still bounding each wire frame.
const maxProtocolFrameBytes = 6*domain.MaxResponseBytes + 1024*1024

func (h *Harness) connect(ctx context.Context) (*connection, func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	path := h.Path
	if path == "" {
		path = "codex"
	}
	processCtx, cancel := context.WithCancel(ctx)
	cmd := exec.Command(path, "app-server", "--listen", "stdio://")
	cmd.Dir = h.Directory
	cmd.WaitDelay = processWait
	stdinChild, stdin, err := os.Pipe()
	if err != nil {
		cancel()
		return nil, nil, err
	}
	stdout, stdoutChild, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdinChild.Close()
		cancel()
		return nil, nil, err
	}
	// Own pipe ends so Wait cannot truncate a fast-exiting CLI's last frames.
	// Nil stderr discards diagnostics without another copying goroutine/pipe.
	cmd.Stdin, cmd.Stdout = stdinChild, stdoutChild
	kill, err := process.Start(cmd)
	_ = stdinChild.Close()
	_ = stdoutChild.Close()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		cancel()
		return nil, nil, fmt.Errorf("start codex: %w", err)
	}
	processDone := make(chan struct{})
	go func() {
		// The protocol reader reports EOF/errors after draining final output.
		// Wait status can be a deliberate termination during owned cleanup.
		_ = cmd.Wait()
		_ = kill() // Release descendants retaining stdout when the parent exits.
		close(processDone)
	}()
	output, readError := make(chan message), make(chan error, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(output)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), maxProtocolFrameBytes)
		for scanner.Scan() {
			var event message
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				readError <- fmt.Errorf("invalid codex protocol message: %w", err)
				return
			}
			select {
			case output <- event:
			case <-processCtx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			readError <- err
		} else {
			readError <- io.ErrUnexpectedEOF
		}
	}()
	stopAll := sync.OnceValue(func() error {
		cancel()
		_ = stdin.Close() // Interrupt blocked stdin writes as well as reads.
		killErr := kill()
		_ = stdout.Close()
		select {
		case <-processDone:
		case <-time.After(processWait):
			killErr = errors.Join(killErr, errors.New("timed out waiting for codex process to exit"))
		}
		<-readerDone
		return killErr
	})
	stopOnCancel := context.AfterFunc(processCtx, func() { _ = stopAll() })
	stop := func() error { stopOnCancel(); return stopAll() }
	c := &connection{ctx: processCtx, input: json.NewEncoder(stdin), output: output, readError: readError}
	if _, err := c.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "harness_gateway", "title": "Harness-Gateway", "version": buildinfo.Version}}, nil); err != nil {
		return nil, nil, errors.Join(err, stop())
	}
	if err := c.input.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return nil, nil, errors.Join(err, stop())
	}
	return c, stop, nil
}

func (h *Harness) Models(ctx context.Context) (ids []string, err error) {
	c, stop, err := h.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, stop()) }()
	cursor := ""
	seen := map[string]bool{}
	for {
		params := map[string]any{"limit": 100, "includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		data, err := c.call("model/list", params, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Data []struct {
				Model           string   `json:"model"`
				Hidden          bool     `json:"hidden"`
				InputModalities []string `json:"inputModalities"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("decode codex model catalog: %w", err)
		}
		if page.Data == nil {
			return nil, fmt.Errorf("codex model catalog omitted data")
		}
		for _, model := range page.Data {
			text := len(model.InputModalities) == 0
			for _, modality := range model.InputModalities {
				if modality == "text" {
					text = true
				}
			}
			if !model.Hidden && text && model.Model != "" {
				ids = append(ids, model.Model)
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return ids, nil
		}
		cursor = *page.NextCursor
		if seen[cursor] {
			return nil, fmt.Errorf("codex model catalog repeated a pagination cursor")
		}
		seen[cursor] = true
	}
}
