package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/process"
)

var listeningURL = regexp.MustCompile(`"url":"(http://127\.0\.0\.1:[0-9]+)"`)
var scalarAsset = regexp.MustCompile(`src="(/docs/assets/scalar-[a-zA-Z0-9.\-]+\.js)"`)

func smoke(ctx context.Context, root, binary, version string, fixtures map[string]string) (checks int, runErr error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	log, err := os.Create(filepath.Join(root, filepath.Base(filepath.Dir(binary))+".log"))
	if err != nil {
		return 0, err
	}
	defer log.Close()
	cmd := exec.Command(binary, "--listen", "127.0.0.1:0", "--log-format", "json", "--codex-path", fixtures["codex"], "--copilot-path", fixtures["copilot"], "--directory", root)
	cmd.Dir, cmd.Stdout, cmd.Stderr, cmd.WaitDelay = root, log, log, 2*time.Second
	cmd.Env = append(os.Environ(), "HARNESS_GATEWAY_CODEX_TEST=acceptance", "HARNESS_GATEWAY_COPILOT_TEST=acceptance",
		"HARNESS_GATEWAY_COPILOT_PID="+filepath.Join(root, "copilot-pid"),
		"HARNESS_GATEWAY_COPILOT_LOG="+filepath.Join(root, "copilot-methods.log"))
	stop, err := process.Start(cmd)
	if err != nil {
		return 0, err
	}
	defer func() { runErr = errors.Join(runErr, stop()) }()
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	defer func() {
		select {
		case <-done:
			runErr = errors.Join(runErr, fmt.Errorf("smoke gateway exited before requested shutdown: %v", waitErr))
			return
		default:
		}
		_ = cmd.Process.Signal(os.Interrupt)
		timer := time.NewTimer(6 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
			runErr = errors.Join(runErr, waitErr)
		case <-timer.C:
			stopErr := stop()
			<-done
			runErr = errors.Join(runErr, errors.New("smoke gateway did not stop gracefully"), stopErr, waitErr)
		}
	}()
	ready, cancelReady := context.WithTimeout(ctx, 10*time.Second)
	defer cancelReady()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return 0, fmt.Errorf("smoke gateway exited before readiness: %v", waitErr)
		case <-ready.Done():
			return 0, fmt.Errorf("smoke gateway startup: %w", ready.Err())
		case <-ticker.C:
			data, err := os.ReadFile(log.Name())
			if err != nil {
				return 0, err
			}
			match := listeningURL.FindSubmatch(data)
			if len(match) == 0 {
				continue
			}
			return smokeHTTP(ctx, string(match[1]), version)
		}
	}
}

func smokeHTTP(ctx context.Context, base, version string) (int, error) {
	client := &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	checks := 0
	request := func(method, path, body string, status int, headers map[string]string) ([]byte, http.Header, error) {
		r, err := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		if err != nil {
			return nil, nil, err
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		for key, value := range headers {
			r.Header.Set(key, value)
		}
		response, err := client.Do(r)
		if err != nil {
			return nil, nil, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 16*1024*1024+1))
		if err != nil {
			return nil, nil, err
		}
		if len(data) > 16*1024*1024 {
			return nil, nil, errors.New("smoke response exceeded 16 MiB")
		}
		if response.StatusCode != status {
			return nil, nil, fmt.Errorf("%s %s: HTTP %d, expected %d", method, path, response.StatusCode, status)
		}
		checks++
		return data, response.Header, nil
	}
	health, _, err := request("GET", "/health", "", 200, nil)
	if err != nil {
		return checks, err
	}
	var state struct{ Status string }
	if err := json.Unmarshal(health, &state); err != nil || state.Status != "ok" {
		return checks, errors.New("invalid health response")
	}
	_, headers, err := request("GET", "/", "", 307, nil)
	if err != nil {
		return checks, err
	}
	if headers.Get("Location") != "/docs" {
		return checks, errors.New("root did not redirect to /docs")
	}
	docs, _, err := request("GET", "/docs", "", 200, nil)
	if err != nil {
		return checks, err
	}
	asset := scalarAsset.FindSubmatch(docs)
	if len(asset) == 0 {
		return checks, errors.New("documentation did not reference the bundled Scalar asset")
	}
	for _, path := range []string{string(asset[1]), "/docs/init.js"} {
		data, _, err := request("GET", path, "", 200, nil)
		if err != nil {
			return checks, err
		}
		if len(data) == 0 {
			return checks, fmt.Errorf("empty documentation asset %s", path)
		}
	}
	schema, _, err := request("GET", "/openapi.json", "", 200, nil)
	if err != nil {
		return checks, err
	}
	var document struct{ Info struct{ Version string } }
	if err := json.Unmarshal(schema, &document); err != nil || document.Info.Version != strings.TrimPrefix(version, "v") {
		return checks, errors.New("wrong OpenAPI release version")
	}
	for _, method := range []string{"GET", "HEAD"} {
		if _, _, err := request(method, "/v1/models", "", 403, map[string]string{"Sec-Fetch-Site": "cross-site"}); err != nil {
			return checks, err
		}
	}
	catalog, headers, err := request("GET", "/v1/models", "", 200, nil)
	if err != nil {
		return checks, err
	}
	if headers.Get("Cache-Control") != "no-store" {
		return checks, errors.New("model catalog missing no-store")
	}
	var models struct{ Data []struct{ ID string } }
	if err := json.Unmarshal(catalog, &models); err != nil {
		return checks, err
	}
	found := make(map[string]bool)
	for _, model := range models.Data {
		found[model.ID] = true
	}
	for _, model := range []string{"codex", "copilot", "codex/native-one", "copilot/native-one"} {
		if !found[model] {
			return checks, fmt.Errorf("catalog omitted %s", model)
		}
	}
	cached, _, err := request("GET", "/v1/models", "", 200, nil)
	if err != nil {
		return checks, err
	}
	if string(cached) != string(catalog) {
		return checks, errors.New("warm model catalog changed")
	}
	head, headers, err := request("HEAD", "/v1/models", "", 200, nil)
	if err != nil {
		return checks, err
	}
	if len(head) != 0 || headers.Get("Cache-Control") != "no-store" {
		return checks, errors.New("invalid model HEAD response")
	}
	for _, method := range []string{"GET", "HEAD"} {
		if _, _, err := request(method, "/v1/models", "", 403, map[string]string{"Origin": "https://foreign.example"}); err != nil {
			return checks, err
		}
	}
	for _, model := range []string{"codex", "copilot", "codex/native-one", "copilot/native-one"} {
		for _, stream := range []bool{false, true} {
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"system","content":"Be brief"},{"role":"user","content":"normal"}],"stream":%t}`, model, stream)
			data, headers, err := request("POST", "/v1/chat/completions", body, 200, nil)
			if err != nil {
				return checks, err
			}
			if stream {
				if !strings.HasPrefix(headers.Get("Content-Type"), "text/event-stream") {
					return checks, errors.New("stream missing SSE content type")
				}
				if err := checkStream(data, model); err != nil {
					return checks, err
				}
			} else {
				var completion struct {
					Model   string
					Choices []struct{ Message struct{ Content string } }
				}
				if err := json.Unmarshal(data, &completion); err != nil {
					return checks, err
				}
				if completion.Model != model || len(completion.Choices) != 1 || completion.Choices[0].Message.Content != "hello world" {
					return checks, fmt.Errorf("invalid buffered completion for %s", model)
				}
			}
		}
	}
	for _, test := range []struct {
		path, body string
		status     int
	}{
		{"/v1/chat/completions", `{"model":"codex","messages":[{"role":"user","content":"normal"}],"temperature":0.5}`, 400},
		{"/v1/chat/completions", `{"model":"missing","messages":[{"role":"user","content":"normal"}]}`, 404},
		{"/health", "", 405},
	} {
		if _, _, err := request("POST", test.path, test.body, test.status, nil); err != nil {
			return checks, err
		}
	}
	return checks, nil
}

func checkStream(data []byte, model string) error {
	var text strings.Builder
	done, finished := false, false
	for _, frame := range strings.Split(string(data), "\n\n") {
		if frame == "" {
			continue
		}
		if done || !strings.HasPrefix(frame, "data: ") {
			return errors.New("invalid SSE framing")
		}
		payload := strings.TrimPrefix(frame, "data: ")
		if payload == "[DONE]" {
			if !finished {
				return errors.New("SSE ended without a finish chunk")
			}
			done = true
			continue
		}
		var chunk struct {
			Model   string
			Choices []struct {
				Delta        struct{ Content string }
				FinishReason *string `json:"finish_reason"`
			}
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return err
		}
		if chunk.Model != model {
			return errors.New("wrong SSE model or backend error frame")
		}
		for _, choice := range chunk.Choices {
			if finished {
				return errors.New("SSE choice arrived after completion")
			}
			text.WriteString(choice.Delta.Content)
			if choice.FinishReason != nil {
				finished = true
			}
		}
	}
	if !done || text.String() != "hello world" {
		return errors.New("incomplete SSE response")
	}
	return nil
}
