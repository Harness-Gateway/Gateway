package copilot

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/harness-gateway/harness-gateway/internal/adapters/inbound/openai"
	"github.com/harness-gateway/harness-gateway/internal/application"
	"github.com/harness-gateway/harness-gateway/internal/domain"
	"github.com/harness-gateway/harness-gateway/internal/testutil"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv("HARNESS_GATEWAY_COPILOT_TEST"); mode != "" {
		if len(os.Args) == 3 && os.Args[1] == "--no-auto-update" && os.Args[2] == "--version" {
			fmt.Println("GitHub Copilot CLI 0.0.0-fixture.")
			os.Exit(0)
		}
		if err := fixture(mode); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// The test binary impersonates the CLI. Production guards frame sizes but
// delegates JSON-RPC methods and session events to the official SDK.
func fixture(mode string) error {
	if mode == "descendant-child" {
		marker := os.Getenv("HARNESS_GATEWAY_COPILOT_CHILD")
		if err := os.WriteFile(marker+".tmp", []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			return err
		}
		if err := os.Rename(marker+".tmp", marker); err != nil {
			return err
		}
		time.Sleep(30 * time.Second)
		return nil
	}
	if strings.HasPrefix(mode, "descendant-") {
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "HARNESS_GATEWAY_COPILOT_TEST=descendant-child")
		if err := child.Start(); err != nil {
			return err
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(os.Getenv("HARNESS_GATEWAY_COPILOT_CHILD")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				_ = child.Process.Kill()
				_ = child.Wait()
				return errors.New("fixture child did not start")
			}
			time.Sleep(5 * time.Millisecond)
		}
		mode = strings.TrimPrefix(mode, "descendant-")
	}
	if mode == "startup-hold" {
		file, err := os.OpenFile(os.Getenv("HARNESS_GATEWAY_ACCEPTANCE_STARTUP"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if errors.Is(err, os.ErrExist) {
			mode = "acceptance"
		} else if err != nil {
			return err
		} else if err := file.Close(); err != nil {
			return err
		}
	}
	if directory := os.Getenv("HARNESS_GATEWAY_ACCEPTANCE_PIDS"); directory != "" {
		pid := strconv.Itoa(os.Getpid())
		if err := os.WriteFile(filepath.Join(directory, pid), []byte(pid), 0600); err != nil {
			return err
		}
	}
	if err := os.WriteFile(os.Getenv("HARNESS_GATEWAY_COPILOT_PID"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		return err
	}
	if mode == "oversized-frame" || mode == "oversized-header" || mode == "partial-frame" {
		switch mode {
		case "oversized-frame":
			fmt.Fprint(os.Stdout, "Content-Length: 134217728\r\n\r\n")
		case "oversized-header":
			fmt.Fprint(os.Stdout, strings.Repeat("x", maxProtocolHeaderBytes+1))
		case "partial-frame":
			fmt.Fprint(os.Stdout, "Content-Length: 512\r\n\r\n{")
		}
		time.Sleep(30 * time.Second)
		return nil
	}
	reader := bufio.NewReader(os.Stdin)
	write := func(value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(data), data)
		return err
	}
	var sessionID string
	var contract map[string]any
	sequence := 0
	event := func(data sdk.SessionEventData, agentID *string) error {
		sequence++
		return write(map[string]any{"jsonrpc": "2.0", "method": "session.event", "params": map[string]any{
			"sessionId": sessionID,
			"event":     sdk.SessionEvent{ID: fmt.Sprint(sequence), Timestamp: time.Now().UTC(), Data: data, AgentID: agentID},
		}})
	}
	for {
		length := 0
		for {
			line, err := reader.ReadString('\n')
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if line == "\r\n" {
				break
			}
			if strings.HasPrefix(line, "Content-Length:") {
				length, err = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:")))
				if err != nil {
					return err
				}
			}
		}
		if length <= 0 || length > 1024*1024 {
			return errors.New("invalid frame length")
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(reader, data); err != nil {
			return err
		}
		var request struct {
			ID     json.RawMessage            `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(data, &request); err != nil {
			return err
		}
		log, err := os.OpenFile(os.Getenv("HARNESS_GATEWAY_COPILOT_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, writeErr := fmt.Fprintln(log, request.Method)
		closeErr := log.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return err
		}
		if len(request.ID) == 0 {
			continue
		}
		reply := func(result any) error {
			return write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}
		reject := func() error {
			return write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32000, "message": "fixture failure"}})
		}
		switch request.Method {
		case "connect":
			if _, exists := request.Params["token"]; exists {
				return errors.New("private bridge token must not reach the stdio CLI")
			}
			if mode == "startup-hold" {
				continue
			}
			if mode == "startup-crash" {
				return errors.New("fixture startup crash")
			}
			if err := reply(map[string]any{"ok": true, "protocolVersion": 3, "version": "fixture"}); err != nil {
				return err
			}
		case "session.create":
			if mode == "contract" {
				var captured struct {
					Model, ReasoningEffort string
					SystemMessage          struct{ Content string }
				}
				data, err := json.Marshal(request.Params)
				if err != nil {
					return err
				}
				if err := json.Unmarshal(data, &captured); err != nil {
					return err
				}
				contract = map[string]any{"model": captured.Model, "reasoning_effort": captured.ReasoningEffort, "instructions": captured.SystemMessage.Content}
			}
			if mode == "invalid-params" {
				if err := write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32602, "message": "private invalid model"}}); err != nil {
					return err
				}
				continue
			}
			if expected := os.Getenv("HARNESS_GATEWAY_COPILOT_EFFORT"); expected != "" {
				if expected == "omitted" {
					if _, exists := request.Params["reasoningEffort"]; exists {
						return errors.New("reasoning effort default was overridden")
					}
				} else {
					var effort string
					if err := json.Unmarshal(request.Params["reasoningEffort"], &effort); err != nil || effort != expected {
						return fmt.Errorf("wrong reasoning effort: %q", effort)
					}
				}
			}
			if expected := os.Getenv("HARNESS_GATEWAY_COPILOT_MODEL"); expected != "" {
				var model string
				if err := json.Unmarshal(request.Params["model"], &model); err != nil || model != expected {
					return fmt.Errorf("wrong native model: %q", model)
				}
			}
			if err := json.Unmarshal(request.Params["sessionId"], &sessionID); err != nil {
				return err
			}
			if record := os.Getenv("HARNESS_GATEWAY_COPILOT_STATE_RECORD"); record != "" {
				directory := os.Getenv("HARNESS_GATEWAY_COPILOT_PROFILE")
				if raw := request.Params["configDir"]; len(raw) != 0 {
					if err := json.Unmarshal(raw, &directory); err != nil {
						return err
					}
				}
				state := filepath.Join(directory, "session-state", sessionID)
				if err := os.MkdirAll(state, 0700); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(state, "events.jsonl"), []byte("fixture conversation"), 0600); err != nil {
					return err
				}
				if err := os.WriteFile(record, []byte(directory), 0600); err != nil {
					return err
				}
			}
			if mode == "create-error" {
				if err := reject(); err != nil {
					return err
				}
				continue
			}
			if string(request.Params["streaming"]) != "true" || string(request.Params["availableTools"]) != "[]" {
				return errors.New("missing streaming or empty tool allowlist")
			}
			if string(request.Params["enableSessionStore"]) != "false" {
				return errors.New("shared session store must be disabled")
			}
			if err := reply(map[string]any{"sessionId": sessionID, "workspacePath": nil}); err != nil {
				return err
			}
		case "models.list":
			if mode == "catalog-hold" || mode == "catalog-gate" {
				if path := os.Getenv("HARNESS_GATEWAY_ACCEPTANCE_CATALOG"); path != "" {
					if err := os.WriteFile(path, []byte("started"), 0600); err != nil {
						return err
					}
				}
				if mode == "catalog-hold" {
					continue
				}
				for {
					_, err := os.Stat(os.Getenv("HARNESS_GATEWAY_ACCEPTANCE_CATALOG_RELEASE"))
					if err == nil {
						break
					}
					if !errors.Is(err, os.ErrNotExist) {
						return err
					}
					time.Sleep(5 * time.Millisecond)
				}
				release, err := os.ReadFile(os.Getenv("HARNESS_GATEWAY_ACCEPTANCE_CATALOG_RELEASE"))
				if err != nil {
					return err
				}
				if string(release) == "error" {
					mode = "catalog-error"
				}
			}
			if mode == "catalog-error" {
				if err := reject(); err != nil {
					return err
				}
				continue
			}
			if err := reply(map[string]any{"models": []any{
				map[string]any{"id": "native-one", "policy": map[string]string{"state": "enabled"}},
				map[string]any{"id": "blocked", "policy": map[string]string{"state": "disabled"}},
				map[string]any{"id": "native-two"},
			}}); err != nil {
				return err
			}
		case "session.send":
			if mode == "acceptance" {
				if err := json.Unmarshal(request.Params["prompt"], &mode); err != nil {
					return err
				}
			}
			if mode == "send-error" {
				if err := reject(); err != nil {
					return err
				}
				continue
			}
			if err := reply(map[string]string{"messageId": "fixture-user"}); err != nil {
				return err
			}
			if path := os.Getenv("HARNESS_GATEWAY_ACCEPTANCE_TURN"); path != "" {
				if err := os.WriteFile(path, []byte("started"), 0600); err != nil {
					return err
				}
			}
			if mode == "silent" {
				continue
			}
			if mode == "crash" || mode == "hold" {
				if err := event(&sdk.AssistantMessageDeltaData{MessageID: "fixture-message", DeltaContent: "ready"}, nil); err != nil {
					return err
				}
				if mode == "crash" {
					return errors.New("fixture crash after accepting request")
				}
				continue
			}
			if mode == "session-error" || mode == "provider-invalid-request" {
				data := &sdk.SessionErrorData{ErrorType: "query", Message: "fixture query failed"}
				if mode == "provider-invalid-request" {
					data.ProviderErrorType = ptr("invalid_request_error")
					data.StatusCode = ptr(int32(400))
				}
				if err := event(data, nil); err != nil {
					return err
				}
				continue
			}
			if mode == "escaped" {
				if err := event(&sdk.AssistantMessageData{MessageID: "escaped", Content: strings.Repeat("\x00", 3*1024*1024)}, nil); err != nil {
					return err
				}
				if err := event(&sdk.SessionIdleData{}, nil); err != nil {
					return err
				}
				continue
			}
			if err := event(&sdk.AssistantMessageDeltaData{MessageID: "subagent", DeltaContent: "hidden"}, ptr("subagent")); err != nil {
				return err
			}
			response, first, second := "hello world", "hello ", "world"
			if mode == "contract" {
				var prompt string
				if err := json.Unmarshal(request.Params["prompt"], &prompt); err != nil {
					return err
				}
				contract["input"] = prompt
				data, err := json.Marshal(contract)
				if err != nil {
					return err
				}
				response, first, second = string(data), string(data[:1]), string(data[1:])
			}
			if mode != "fallback" {
				if err := event(&sdk.AssistantMessageDeltaData{MessageID: "fixture-message", DeltaContent: first}, nil); err != nil {
					return err
				}
				if err := event(&sdk.AssistantMessageDeltaData{MessageID: "fixture-message", DeltaContent: second}, nil); err != nil {
					return err
				}
			}
			for i := 0; i < 2; i++ {
				if err := event(&sdk.AssistantMessageData{MessageID: "fixture-message", Content: response}, nil); err != nil {
					return err
				}
			}
			if err := event(&sdk.AssistantUsageData{Model: "fixture", InputTokens: ptr(int64(12)), OutputTokens: ptr(int64(3)), FinishReason: ptr("length")}, nil); err != nil {
				return err
			}
			if err := event(&sdk.SessionIdleData{}, nil); err != nil {
				return err
			}
		case "ping":
			if err := reply(map[string]any{"ok": true, "protocolVersion": 3}); err != nil {
				return err
			}
		case "session.detach":
			if mode == "detach-hold" {
				continue
			}
			if mode == "detach-unsupported" {
				if err := write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "legacy CLI has no detach method"}}); err != nil {
					return err
				}
				continue
			}
			if mode == "detach-error" {
				if err := reject(); err != nil {
					return err
				}
				continue
			}
			if err := reply(map[string]bool{"success": true}); err != nil {
				return err
			}
		case "session.delete":
			return errors.New("session.delete must not access the default CLI profile")
		case "session.options.update":
			if err := reply(map[string]bool{"success": true}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected method %q", request.Method)
		}
	}
}

func ptr[T any](value T) *T { return &value }

func testHarness(t *testing.T, mode string) (*Harness, string, string) {
	t.Helper()
	directory := t.TempDir()
	log, pid := filepath.Join(directory, "methods"), filepath.Join(directory, "pid")
	t.Setenv("HARNESS_GATEWAY_COPILOT_TEST", mode)
	t.Setenv("HARNESS_GATEWAY_COPILOT_LOG", log)
	t.Setenv("HARNESS_GATEWAY_COPILOT_PID", pid)
	// Race-instrumented child processes should exit immediately after their checks.
	t.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &Harness{Path: testutil.CLIEntryPoint(t, path)}, log, pid
}

func testRequest() domain.Request {
	return domain.Request{Messages: []domain.Message{{Role: "system", Text: "Be brief"}, {Role: "user", Text: "hello"}}}
}

func TestCompletionAndCleanup(t *testing.T) {
	for _, mode := range []string{"normal", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			h, log, _ := testHarness(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var output strings.Builder
			result, err := h.Complete(ctx, testRequest(), func(text string) error { output.WriteString(text); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if output.String() != "hello world" {
				t.Fatalf("lost, duplicated, or leaked text: %q", output.String())
			}
			if result.Usage == nil || result.Usage.InputTokens != 12 || result.Usage.TotalTokens != 15 || result.FinishReason != "length" {
				t.Fatalf("bad metadata: %+v", result)
			}
			methods, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(methods), "session.detach\n") || strings.Contains(string(methods), "session.delete\n") {
				t.Fatalf("session must detach without deleting from the default profile: %s", methods)
			}
		})
	}
}

func TestCrashReturnsBeforeDeadline(t *testing.T) {
	h, log, _ := testHarness(t, "crash")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := h.Complete(ctx, testRequest(), func(string) error { return nil })
	if err == nil || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "connection lost") {
		t.Fatalf("expected prompt connection failure, got %v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("waited until the request deadline: %v", err)
	}
	methods, readErr := os.ReadFile(log)
	if readErr != nil || !strings.Contains(string(methods), "session.send\n") {
		t.Fatalf("fixture did not accept the request: %s, %v", methods, readErr)
	}
}

func TestCrashHTTPResponse(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(strconv.FormatBool(stream), func(t *testing.T) {
			h, _, _ := testHarness(t, "crash")
			gateway := application.New([]application.Backend{{Model: application.Model{ID: "copilot"}, Harness: h}}, 1)
			handler := openai.New(gateway, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
			body := fmt.Sprintf(`{"model":"copilot","messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("POST", "http://127.0.0.1/v1/chat/completions", strings.NewReader(body)))
			if !strings.Contains(w.Body.String(), `"code":"harness_error"`) || strings.Contains(w.Body.String(), "[DONE]") {
				t.Fatal(w.Body.String())
			}
			if stream && (w.Code != 200 || !strings.Contains(w.Body.String(), `"content":"ready"`)) {
				t.Fatalf("expected partial stream and error: %d %s", w.Code, w.Body.String())
			}
			if !stream && w.Code != 502 {
				t.Fatalf("expected prompt 502, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestFailures(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{{"startup-crash", "start copilot"}, {"create-error", "create copilot session"}, {"send-error", "send copilot request"}, {"session-error", "fixture query failed"}} {
		t.Run(tc.mode, func(t *testing.T) {
			h, _, _ := testHarness(t, tc.mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := h.Complete(ctx, testRequest(), func(string) error { return nil })
			if err == nil || !strings.Contains(err.Error(), tc.want) || ctx.Err() != nil {
				t.Fatalf("expected %q promptly, got %v", tc.want, err)
			}
		})
	}
}

func TestMissingExecutable(t *testing.T) {
	h := &Harness{Path: filepath.Join(t.TempDir(), "missing")}
	_, err := h.Complete(context.Background(), testRequest(), func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "find copilot") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCancellation(t *testing.T) {
	h, _, _ := testHarness(t, "hold")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := h.Complete(ctx, testRequest(), func(string) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestCancellationDuringInitialization(t *testing.T) {
	h, log, _ := testHarness(t, "startup-hold")
	t.Setenv("HARNESS_GATEWAY_ACCEPTANCE_STARTUP", filepath.Join(t.TempDir(), "startup"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := h.Complete(ctx, testRequest(), func(string) error { return nil }); done <- err }()
	for {
		data, err := os.ReadFile(log)
		if err == nil && strings.Contains(string(data), "connect\n") {
			break
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("fixture did not accept initialization")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("initialization cancellation was lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("initialization did not stop after cancellation")
	}
}

func TestCancellationDuringSessionCleanupPreservesCause(t *testing.T) {
	h, log, _ := testHarness(t, "detach-hold")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := h.Complete(ctx, testRequest(), func(string) error { return nil }); done <- err }()
	for {
		data, err := os.ReadFile(log)
		if err == nil && strings.Contains(string(data), "session.detach\n") {
			break
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("fixture did not start session detach")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation during session cleanup was lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("session cleanup did not stop after cancellation")
	}
}

func TestEmitFailure(t *testing.T) {
	h, _, _ := testHarness(t, "hold")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	expected := errors.New("client write failed")
	_, err := h.Complete(ctx, testRequest(), func(string) error { return expected })
	if !errors.Is(err, expected) {
		t.Fatalf("expected client write failure, got %v", err)
	}
}

func TestConnectionMonitor(t *testing.T) {
	h, _, _ := testHarness(t, "hold")
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	_, err := h.Complete(ctx, testRequest(), func(string) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("healthy IPC should keep waiting for inference until the request deadline: %v", err)
	}
}

func TestModelDiscovery(t *testing.T) {
	h, log, _ := testHarness(t, "catalog")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	models, err := h.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "native-one,native-two" {
		t.Fatalf("wrong model catalog: %v", models)
	}
	methods, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(methods) != "connect\nmodels.list\n" {
		t.Fatalf("discovery should not create inference sessions: %s", methods)
	}
}

func TestModelDiscoveryFailure(t *testing.T) {
	h, _, _ := testHarness(t, "catalog-error")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := h.Models(ctx); err == nil || !strings.Contains(err.Error(), "model discovery") {
		t.Fatalf("catalog error hidden: %v", err)
	}
}

func TestModelOverride(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(strconv.FormatBool(explicit), func(t *testing.T) {
			h, _, _ := testHarness(t, "normal")
			h.Model = "configured-default"
			request := testRequest()
			expected := h.Model
			if explicit {
				request.Model = "native-selection"
				expected = request.Model
			}
			t.Setenv("HARNESS_GATEWAY_COPILOT_MODEL", expected)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := h.Complete(ctx, request, func(string) error { return nil }); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReasoningEffort(t *testing.T) {
	for _, effort := range []string{"", "low", "high"} {
		t.Run("effort="+effort, func(t *testing.T) {
			h, _, _ := testHarness(t, "normal")
			wanted := effort
			if wanted == "" {
				wanted = "omitted"
			}
			t.Setenv("HARNESS_GATEWAY_COPILOT_EFFORT", wanted)
			request := testRequest()
			request.ReasoningEffort = effort
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := h.Complete(ctx, request, func(string) error { return nil }); err != nil {
				t.Fatal(err)
			}
		})
	}
}
