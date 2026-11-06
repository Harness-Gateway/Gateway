package codex

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/adapters/inbound/openai"
	"github.com/harness-gateway/harness-gateway/internal/application"
	"github.com/harness-gateway/harness-gateway/internal/domain"
	"github.com/harness-gateway/harness-gateway/internal/testutil"
)

func TestDescendantCleanupAndPromptCrashDetection(t *testing.T) {
	for _, mode := range []string{"normal", "cancel", "crash", "startup-crash", "catalog", "catalog-error"} {
		t.Run(mode, func(t *testing.T) {
			h := testHarness(t, "descendant-"+mode)
			marker := filepath.Join(t.TempDir(), "child-pid")
			t.Setenv("HARNESS_GATEWAY_CODEX_CHILD", marker)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			ready := false
			var err error
			if strings.HasPrefix(mode, "catalog") {
				_, err = h.Models(ctx)
			} else {
				_, err = h.Complete(ctx, testRequest(), func(text string) error {
					ready = true
					if mode == "cancel" {
						cancel()
					}
					return nil
				})
			}
			data, readErr := os.ReadFile(marker)
			if readErr != nil {
				t.Fatal(readErr)
			}
			pid, parseErr := strconv.Atoi(string(data))
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			t.Cleanup(func() {
				if running, _ := testutil.ProcessRunning(pid); running {
					if child, e := os.FindProcess(pid); e == nil {
						_ = child.Kill()
					}
				}
			})
			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("parent crash/cleanup waited for the request timeout: %v", err)
			}
			if mode == "normal" || mode == "catalog" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("fixture failure was lost")
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation was lost: %v", err)
			}
			if mode == "crash" && !ready {
				t.Fatal("buffered output was lost before detecting the crash")
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				running, e := testutil.ProcessRunning(pid)
				if e != nil {
					t.Fatal(e)
				}
				if !running {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("descendant %d survived %s", pid, mode)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestParentCrashWithInheritedPipeReturnsHTTPError(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(strconv.FormatBool(stream), func(t *testing.T) {
			h := testHarness(t, "descendant-crash")
			t.Setenv("HARNESS_GATEWAY_CODEX_CHILD", filepath.Join(t.TempDir(), "child-pid"))
			server := openai.New(application.New([]application.Backend{{Model: application.Model{ID: "codex"}, Harness: h}}, 1), 5*time.Second, nil)
			body := `{"model":"codex","messages":[{"role":"system","content":"Be brief"},{"role":"user","content":"hello"}],"stream":` + strconv.FormatBool(stream) + `}`
			response := httptest.NewRecorder()
			server.ServeHTTP(response, httptest.NewRequest("POST", "http://127.0.0.1/v1/chat/completions", strings.NewReader(body)))
			want := 502
			if stream {
				want = 200
			}
			if response.Code != want || !strings.Contains(response.Body.String(), "harness_error") || strings.Contains(response.Body.String(), "[DONE]") {
				t.Fatalf("crash was not reported correctly: %d %s", response.Code, response.Body.String())
			}
			if stream && !strings.Contains(response.Body.String(), `"content":"ready"`) {
				t.Fatal("lost partial text")
			}
		})
	}
}

func TestEscapedTextFitsDecodedResponseBudget(t *testing.T) {
	h := testHarness(t, "escaped")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	var text strings.Builder
	_, err := h.Complete(ctx, testRequest(), func(delta string) error { text.WriteString(delta); return nil })
	if err != nil || text.String() != strings.Repeat("\x00", 3*1024*1024) {
		t.Fatalf("escaped text was truncated or rejected: bytes=%d, err=%v", text.Len(), err)
	}
}

func TestProtocolFrameStillBounded(t *testing.T) {
	h := testHarness(t, "oversized-frame")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	_, err := h.Complete(ctx, testRequest(), func(string) error { t.Fatal("oversized frame emitted text"); return nil })
	if err == nil || !strings.Contains(err.Error(), "token too long") {
		t.Fatalf("unexpected frame error: %v", err)
	}
}

func TestInvalidParametersClassification(t *testing.T) {
	for _, mode := range []string{"invalid-params", "initialize-invalid-params"} {
		t.Run(mode, func(t *testing.T) {
			h := testHarness(t, mode)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_, err := h.Complete(ctx, testRequest(), func(string) error { return nil })
			var inputError *domain.RequestError
			var rpcError *protocolError
			if errors.As(err, &inputError) != (mode == "invalid-params") || !errors.As(err, &rpcError) || rpcError.Code != -32602 {
				t.Fatalf("lost error classification or cause: %v", err)
			}
		})
	}
}

func TestBlockedStdinReturnsHTTPTimeoutAndReleasesCapacity(t *testing.T) {
	h := testHarness(t, "blocked-stdin")
	marker := filepath.Join(t.TempDir(), "blocked-stdin")
	t.Setenv("HARNESS_GATEWAY_CODEX_BLOCKED", marker)
	server := openai.New(application.New([]application.Backend{{Model: application.Model{ID: "codex", Provider: "codex"}, Harness: h}}, 1), time.Second, nil)
	body := `{"model":"codex","messages":[{"role":"system","content":"Be brief"},{"role":"user","content":"` + strings.Repeat("x", 512*1024) + `"}]}`
	r := httptest.NewRequest("POST", "http://127.0.0.1/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != 504 || !strings.Contains(w.Body.String(), "harness_timeout") {
		t.Fatalf("status=%d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("fixture never reached the blocked stdin state: %v", err)
	}
	t.Setenv("HARNESS_GATEWAY_CODEX_TEST", "normal")
	r = httptest.NewRequest("POST", "http://127.0.0.1/v1/chat/completions", strings.NewReader(`{"model":"codex","messages":[{"role":"system","content":"Be brief"},{"role":"user","content":"hello"}]}`))
	w = httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "hello world") {
		t.Fatalf("capacity did not recover: %d: %s", w.Code, w.Body.String())
	}
}
