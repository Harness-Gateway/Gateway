package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/domain"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv("HARNESS_GATEWAY_CODEX_TEST"); mode != "" {
		if len(os.Args) == 2 && os.Args[1] == "--version" {
			fmt.Println("codex-cli 0.0.0-fixture")
			os.Exit(0)
		}
		fakeServer(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeServer(mode string) {
	if mode == "startup-hold" {
		file, err := os.OpenFile(os.Getenv("HARNESS_GATEWAY_ACCEPTANCE_STARTUP"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if errors.Is(err, os.ErrExist) {
			mode = "acceptance"
		} else if err != nil {
			os.Exit(18)
		} else if err := file.Close(); err != nil {
			os.Exit(19)
		}
	}
	if directory := os.Getenv("HARNESS_GATEWAY_ACCEPTANCE_PIDS"); directory != "" {
		pid := strconv.Itoa(os.Getpid())
		if err := os.WriteFile(filepath.Join(directory, pid), []byte(pid), 0600); err != nil {
			os.Exit(15)
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	write := func(value any) {
		if err := encoder.Encode(value); err != nil {
			os.Exit(3)
		}
	}
	notify := func(method string, params any) { write(map[string]any{"method": method, "params": params}) }
	for scanner.Scan() {
		var input struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
			Result map[string]any `json:"result"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &input); err != nil {
			os.Exit(4)
		}
		switch input.Method {
		case "initialize":
			if mode == "startup-hold" {
				continue
			}
			if mode == "invalid" {
				fmt.Println("not-json")
				return
			}
			write(map[string]any{"id": input.ID, "result": map[string]any{}})
		case "thread/start":
			if expected := os.Getenv("HARNESS_GATEWAY_CODEX_MODEL"); expected != "" && input.Params["model"] != expected {
				os.Exit(10)
			}
			if input.Params["ephemeral"] != true || input.Params["sandbox"] != "read-only" || input.Params["approvalPolicy"] != "never" {
				os.Exit(5)
			}
			if input.Params["developerInstructions"] != "Be brief" {
				os.Exit(6)
			}
			write(map[string]any{"id": input.ID, "result": map[string]any{"thread": map[string]string{"id": "thread-test"}}})
		case "model/list":
			if input.Params["includeHidden"] != false {
				os.Exit(11)
			}
			if mode == "catalog-hold" {
				if path := os.Getenv("HARNESS_GATEWAY_ACCEPTANCE_CATALOG"); path != "" {
					if err := os.WriteFile(path, []byte("started"), 0600); err != nil {
						os.Exit(21)
					}
				}
				continue
			}
			if mode == "catalog-error" {
				write(map[string]any{"id": input.ID, "error": map[string]string{"message": "fixture catalog failure"}})
				continue
			}
			if mode == "catalog-invalid" {
				write(map[string]any{"id": input.ID, "result": map[string]any{}})
				continue
			}
			cursor, _ := input.Params["cursor"].(string)
			if mode == "catalog-loop" {
				write(map[string]any{"id": input.ID, "result": map[string]any{"data": []any{}, "nextCursor": "again"}})
				continue
			}
			if cursor == "" {
				write(map[string]any{"id": input.ID, "result": map[string]any{"data": []any{map[string]any{"id": "picker-id", "model": "native-one", "inputModalities": []string{"text"}}, map[string]any{"model": "hidden", "hidden": true}}, "nextCursor": "page-two"}})
			} else if cursor == "page-two" {
				write(map[string]any{"id": input.ID, "result": map[string]any{"data": []any{map[string]any{"model": "native-two"}, map[string]any{"model": "audio-only", "inputModalities": []string{"audio"}}}, "nextCursor": nil}})
			} else {
				os.Exit(12)
			}
		case "turn/start":
			if mode == "acceptance" {
				inputs, _ := input.Params["input"].([]any)
				if len(inputs) != 1 {
					os.Exit(16)
				}
				text, _ := inputs[0].(map[string]any)
				mode, _ = text["text"].(string)
			}
			if expected := os.Getenv("HARNESS_GATEWAY_CODEX_EFFORT"); expected != "" {
				if expected == "omitted" {
					if _, exists := input.Params["effort"]; exists {
						os.Exit(13)
					}
				} else if input.Params["effort"] != expected {
					os.Exit(14)
				}
			}
			if input.Params["threadId"] != "thread-test" {
				os.Exit(7)
			}
			if path := os.Getenv("HARNESS_GATEWAY_ACCEPTANCE_TURN"); path != "" {
				if err := os.WriteFile(path, []byte("started"), 0600); err != nil {
					os.Exit(20)
				}
			}
			if mode == "cancel" || mode == "hold" || mode == "crash" || mode == "silent" {
				write(map[string]any{"id": input.ID, "result": map[string]any{}})
				if mode == "silent" {
					continue
				}
				notify("item/agentMessage/delta", map[string]string{"itemId": "message-test", "delta": "ready"})
				if mode == "crash" {
					os.Exit(17)
				}
				continue
			}
			if mode == "approval" {
				write(map[string]any{"id": "approval-1", "method": "item/commandExecution/requestApproval", "params": map[string]any{}})
				if !scanner.Scan() {
					os.Exit(8)
				}
				var reply struct {
					Result struct {
						Decision string `json:"decision"`
					} `json:"result"`
				}
				if err := json.Unmarshal(scanner.Bytes(), &reply); err != nil || reply.Result.Decision != "decline" {
					os.Exit(9)
				}
			}
			if mode != "fallback" && mode != "failure" {
				notify("item/agentMessage/delta", map[string]string{"itemId": "message-test", "delta": "hello "})
				notify("item/agentMessage/delta", map[string]string{"itemId": "message-test", "delta": "world"})
			}
			if mode != "failure" {
				notify("item/completed", map[string]any{"item": map[string]string{"id": "message-test", "type": "agentMessage", "text": "hello world"}})
			}
			notify("thread/tokenUsage/updated", map[string]any{"tokenUsage": map[string]any{"total": map[string]int{"inputTokens": 12, "outputTokens": 3, "totalTokens": 15}}})
			turn := map[string]any{"status": "completed"}
			if mode == "failure" {
				turn = map[string]any{"status": "failed", "error": map[string]string{"message": "test failure"}}
			}
			notify("turn/completed", map[string]any{"turn": turn})
			// Notifications may precede the response to turn/start.
			write(map[string]any{"id": input.ID, "result": map[string]any{}})
		}
	}
}

func TestModelDiscovery(t *testing.T) {
	h := testHarness(t, "catalog")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	models, err := h.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "native-one,native-two" {
		t.Fatalf("wrong catalog: %v", models)
	}
}

func TestModelDiscoveryFailures(t *testing.T) {
	for _, mode := range []string{"catalog-error", "catalog-invalid", "catalog-loop"} {
		t.Run(mode, func(t *testing.T) {
			h := testHarness(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := h.Models(ctx); err == nil {
				t.Fatal("accepted a broken catalog")
			}
		})
	}
}

func TestModelOverride(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			h := testHarness(t, "normal")
			h.Model = "configured-default"
			request := testRequest()
			expected := h.Model
			if explicit {
				request.Model = "native-selection"
				expected = request.Model
			}
			t.Setenv("HARNESS_GATEWAY_CODEX_MODEL", expected)
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
			h := testHarness(t, "normal")
			wanted := effort
			if wanted == "" {
				wanted = "omitted"
			}
			t.Setenv("HARNESS_GATEWAY_CODEX_EFFORT", wanted)
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

func testHarness(t *testing.T, mode string) *Harness {
	t.Helper()
	t.Setenv("HARNESS_GATEWAY_CODEX_TEST", mode)
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &Harness{Path: path}
}

func testRequest() domain.Request {
	return domain.Request{Messages: []domain.Message{{Role: "system", Text: "Be brief"}, {Role: "user", Text: "hello"}}}
}

func TestProtocolAndTextDeduplication(t *testing.T) {
	for _, mode := range []string{"normal", "fallback", "approval"} {
		t.Run(mode, func(t *testing.T) {
			h := testHarness(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var output strings.Builder
			result, err := h.Complete(ctx, testRequest(), func(text string) error { output.WriteString(text); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if output.String() != "hello world" {
				t.Fatalf("duplicated or missing text: %q", output.String())
			}
			if result.Usage == nil || result.Usage.TotalTokens != 15 {
				t.Fatalf("lost usage: %+v", result)
			}
		})
	}
}

func TestProtocolFailures(t *testing.T) {
	for _, mode := range []string{"invalid", "failure"} {
		t.Run(mode, func(t *testing.T) {
			h := testHarness(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := h.Complete(ctx, testRequest(), func(string) error { return nil }); err == nil {
				t.Fatal("protocol failure was swallowed")
			}
		})
	}
}

func TestCancellationStopsProcess(t *testing.T) {
	h := testHarness(t, "cancel")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := h.Complete(ctx, testRequest(), func(string) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation: %v", err)
	}
}

func TestCancellationDuringInitialization(t *testing.T) {
	h := testHarness(t, "startup-hold")
	marker := filepath.Join(t.TempDir(), "startup")
	t.Setenv("HARNESS_GATEWAY_ACCEPTANCE_STARTUP", marker)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := h.Complete(ctx, testRequest(), func(string) error { return nil }); done <- err }()
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("fixture did not start")
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

func TestEmitFailureStopsProcess(t *testing.T) {
	h := testHarness(t, "cancel")
	expected := errors.New("client write failed")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := h.Complete(ctx, testRequest(), func(string) error { return expected }); !errors.Is(err, expected) {
		t.Fatalf("expected emit error: %v", err)
	}
}
