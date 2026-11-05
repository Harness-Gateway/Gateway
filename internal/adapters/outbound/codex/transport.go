package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/harness-gateway/gateway/internal/buildinfo"
)

const processWait = 2 * time.Second

func (h *Harness) connect(ctx context.Context) (*connection, func(), error) {
	path := h.Path
	if path == "" {
		path = "codex"
	}
	processCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(processCtx, path, "app-server", "--listen", "stdio://")
	cmd.Dir = h.Directory
	configureProcess(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		cancel()
		return nil, nil, err
	}
	// Diagnostics are not protocol messages and can contain local configuration.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("start codex: %w", err)
	}
	stop := func() {
		_ = stdin.Close()
		cancel()
		// Every operation intentionally stops its owned process after finishing.
		_ = cmd.Wait()
	}
	output, readError := make(chan message), make(chan error, 1)
	go func() {
		defer close(output)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
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
	c := &connection{ctx: processCtx, input: json.NewEncoder(stdin), output: output, readError: readError}
	if _, err := c.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "harness_gateway", "title": "Harness-Gateway", "version": buildinfo.Version}}, nil); err != nil {
		stop()
		return nil, nil, err
	}
	if err := c.input.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		stop()
		return nil, nil, err
	}
	return c, stop, nil
}

func (h *Harness) Models(ctx context.Context) ([]string, error) {
	c, stop, err := h.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer stop()
	var ids []string
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
