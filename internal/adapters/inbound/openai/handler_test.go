package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/harness-gateway/gateway/internal/application"
	"github.com/harness-gateway/gateway/internal/domain"
)

type harnessFunc func(context.Context, domain.Request, func(string) error) (domain.Result, error)

func (f harnessFunc) Complete(ctx context.Context, req domain.Request, emit func(string) error) (domain.Result, error) {
	return f(ctx, req, emit)
}

func handler(backend domain.Harness, timeout time.Duration) http.Handler {
	return New(application.New([]application.Backend{{Model: application.Model{ID: "codex", Provider: "codex"}, Harness: backend}}, 1), timeout, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestCompletionAndStream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmtBool(stream), func(t *testing.T) {
			backend := harnessFunc(func(ctx context.Context, req domain.Request, emit func(string) error) (domain.Result, error) {
				if len(req.Messages) != 4 || req.Messages[0].Text != "Be concise" || req.Messages[3].Text != "Hello again" {
					t.Fatalf("lost conversation: %+v", req)
				}
				for _, text := range []string{"Hi ", "世界\n"} {
					if err := emit(text); err != nil {
						return domain.Result{}, err
					}
				}
				return domain.Result{Usage: &domain.Usage{InputTokens: 10, OutputTokens: 3, TotalTokens: 13}}, nil
			})
			body := `{"model":"codex","messages":[{"role":"system","content":"Be concise"},{"role":"user","content":"Hello"},{"role":"assistant","content":"Hi"},{"role":"user","content":[{"type":"text","text":"Hello "},{"type":"text","text":"again"}]}],"stream":` + fmtBool(stream)
			if stream {
				body += `,"stream_options":{"include_usage":true,"include_obfuscation":false}`
			}
			body += `}`
			w := httptest.NewRecorder()
			handler(backend, time.Second).ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
			if w.Code != http.StatusOK {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			if !stream {
				var completion Completion
				if err := json.Unmarshal(w.Body.Bytes(), &completion); err != nil {
					t.Fatal(err)
				}
				if completion.Object != "chat.completion" || completion.Model != "codex" || *completion.Choices[0].Message.Content != "Hi 世界\n" || completion.Choices[0].FinishReason != "stop" || completion.Usage.TotalTokens != 13 {
					t.Fatalf("bad completion: %+v", completion)
				}
				return
			}
			if w.Header().Get("Content-Type") != "text/event-stream" {
				t.Fatal(w.Header())
			}
			frames := strings.Split(strings.TrimSpace(w.Body.String()), "\n\n")
			var id, content string
			var created int64
			for i, frame := range frames {
				data := strings.TrimPrefix(frame, "data: ")
				if i == len(frames)-1 {
					if data != "[DONE]" {
						t.Fatal("missing DONE")
					}
					continue
				}
				var chunk Chunk
				if err := json.Unmarshal([]byte(data), &chunk); err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					id, created = chunk.Id, chunk.Created
					if chunk.Choices[0].Delta.Role == nil || *chunk.Choices[0].Delta.Role != "assistant" {
						t.Fatal("missing role")
					}
				}
				if chunk.Id != id || chunk.Created != created || chunk.Object != "chat.completion.chunk" {
					t.Fatal("unstable stream metadata")
				}
				for _, choice := range chunk.Choices {
					if choice.Delta.Content != nil {
						content += *choice.Delta.Content
					}
				}
				if i == len(frames)-2 && (len(chunk.Choices) != 0 || chunk.Usage == nil || chunk.Usage.TotalTokens != 13) {
					t.Fatal("missing usage chunk")
				}
			}
			if content != "Hi 世界\n" {
				t.Fatalf("duplicated/lost text: %q", content)
			}
		})
	}
}

func fmtBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func TestRejectUnsupportedRequestsBeforeCallingHarness(t *testing.T) {
	backend := harnessFunc(func(context.Context, domain.Request, func(string) error) (domain.Result, error) {
		t.Fatal("invalid request reached harness")
		return domain.Result{}, nil
	})
	cases := []string{
		`{}`, `null`, `{"model":"codex","messages":[]}`,
		`{"model":"codex","messages":[{"role":"tool","content":"x"}]}`,
		`{"model":"codex","messages":[{"role":"user","content":null}]}`,
		`{"model":"codex","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`,
		`{"model":"codex","messages":[{"role":"user","content":"x","tool_calls":[]}]}`,
		`{"model":"codex","messages":[{"role":"user","content":"x"}],"temperature":0.8}`,
		`{"model":"codex","messages":[{"role":"user","content":"x"}],"tools":[]}`,
		`{"model":"codex","messages":[{"role":"user","content":"x"}],"n":2}`,
		`{"model":"codex","messages":[{"role":"user","content":"x"}],"reasoning_effort":"ultra"}`,
		`{"model":"codex","messages":[{"role":"user","content":"x"}],"stream_options":{"include_usage":true}}`,
		`{"model":"codex","messages":[{"role":"user","content":"x"}],"stream":true,"stream_options":{"include_obfuscation":true}}`,
		`{"model":"codex","messages":[{"role":"user","content":"x"}]} {}`,
		`{"model":"codex","messages":[{"role":"assistant","content":"x"}]}`,
		`{"model":"codex","messages":[{"role":"user","content":"x"},{"role":"system","content":"s"},{"role":"user","content":"y"}]}`,
	}
	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler(backend, time.Second).ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d, body=%s", w.Code, w.Body.String())
			}
			var response ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Error.Message == "" {
				t.Fatal("missing OpenAI error envelope")
			}
		})
	}
}

func TestFailuresAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		backend   harnessFunc
		body      string
		status    int
		errorCode string
	}{
		{"failure", func(context.Context, domain.Request, func(string) error) (domain.Result, error) {
			return domain.Result{}, errors.New("secret backend detail")
		}, `{"model":"codex","messages":[{"role":"user","content":"x"}]}`, 502, "harness_error"},
		{"empty", func(context.Context, domain.Request, func(string) error) (domain.Result, error) {
			return domain.Result{}, nil
		}, `{"model":"codex","messages":[{"role":"user","content":"x"}]}`, 502, "harness_error"},
		{"unknown", nil, `{"model":"missing","messages":[{"role":"user","content":"x"}]}`, 404, "model_not_found"},
		{"timeout", func(ctx context.Context, _ domain.Request, _ func(string) error) (domain.Result, error) {
			<-ctx.Done()
			return domain.Result{}, ctx.Err()
		}, `{"model":"codex","messages":[{"role":"user","content":"x"}]}`, 504, "harness_timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := tc.backend
			if backend == nil {
				backend = func(context.Context, domain.Request, func(string) error) (domain.Result, error) {
					t.Fatal("unknown model reached harness")
					return domain.Result{}, nil
				}
			}
			w := httptest.NewRecorder()
			handler(backend, 10*time.Millisecond).ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tc.body)))
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.errorCode) || strings.Contains(w.Body.String(), "secret") {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestStreamErrorDoesNotLookSuccessful(t *testing.T) {
	backend := harnessFunc(func(_ context.Context, _ domain.Request, emit func(string) error) (domain.Result, error) {
		if err := emit("partial"); err != nil {
			return domain.Result{}, err
		}
		return domain.Result{}, errors.New("failed after first chunk")
	})
	w := httptest.NewRecorder()
	handler(backend, time.Second).ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"codex","messages":[{"role":"user","content":"x"}],"stream":true}`)))
	if !strings.Contains(w.Body.String(), `"error":`) || strings.Contains(w.Body.String(), "[DONE]") || strings.Contains(w.Body.String(), `"finish_reason":"stop"`) {
		t.Fatal(w.Body.String())
	}
}

func TestStreamEstablishedBeforeBackendStarts(t *testing.T) {
	w := httptest.NewRecorder()
	backend := harnessFunc(func(ctx context.Context, _ domain.Request, _ func(string) error) (domain.Result, error) {
		if !w.Flushed || !strings.Contains(w.Body.String(), `"role":"assistant"`) {
			t.Fatal("CLI started before the client could access the SSE response")
		}
		if strings.Contains(w.Body.String(), `"finish_reason":"stop"`) {
			t.Fatal("silent request reported completion")
		}
		<-ctx.Done()
		return domain.Result{}, ctx.Err()
	})
	body := `{"model":"codex","messages":[{"role":"user","content":"silent"}],"stream":true}`
	handler(backend, 10*time.Millisecond).ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if !strings.Contains(w.Body.String(), "harness_timeout") || strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatal(w.Body.String())
	}
}

func TestShutdownProducesExplicitError(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmtBool(stream), func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			backend := harnessFunc(func(ctx context.Context, _ domain.Request, emit func(string) error) (domain.Result, error) {
				if err := emit("partial"); err != nil {
					return domain.Result{}, err
				}
				cancel(ErrShuttingDown)
				return domain.Result{}, ctx.Err()
			})
			w := httptest.NewRecorder()
			body := `{"model":"codex","messages":[{"role":"user","content":"x"}],"stream":` + fmtBool(stream) + `}`
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
			handler(backend, time.Second).ServeHTTP(w, r)
			want := http.StatusServiceUnavailable
			if stream {
				want = http.StatusOK
			}
			if w.Code != want || !strings.Contains(w.Body.String(), "gateway_shutting_down") || strings.Contains(w.Body.String(), "[DONE]") || strings.Contains(w.Body.String(), `"finish_reason":"stop"`) {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestStreamAdmissionErrorsRetainHTTPStatus(t *testing.T) {
	started := make(chan struct{})
	backend := harnessFunc(func(ctx context.Context, _ domain.Request, _ func(string) error) (domain.Result, error) {
		close(started)
		<-ctx.Done()
		return domain.Result{}, ctx.Err()
	})
	gateway := application.New([]application.Backend{{Model: application.Model{ID: "codex"}, Harness: backend}}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = gateway.Complete(ctx, "codex", domain.Request{}, nil, nil)
	}()
	<-started
	for _, tc := range []struct {
		model  string
		status int
	}{{"codex", 429}, {"missing", 404}} {
		w := httptest.NewRecorder()
		body := `{"model":"` + tc.model + `","messages":[{"role":"user","content":"x"}],"stream":true}`
		New(gateway, time.Second, nil).ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		if w.Code != tc.status || w.Header().Get("Content-Type") != "application/json" || strings.Contains(w.Body.String(), `"role":"assistant"`) {
			t.Fatalf("admission error started SSE: %d %s", w.Code, w.Body.String())
		}
	}
	cancel()
	<-done
}

func TestRequestSizeAndModels(t *testing.T) {
	backend := harnessFunc(func(context.Context, domain.Request, func(string) error) (domain.Result, error) {
		t.Fatal("unexpected completion")
		return domain.Result{}, nil
	})
	h := handler(backend, time.Second)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+strings.Repeat("x", maxRequestBytes)+`"}`)))
	if w.Code != 413 {
		t.Fatalf("status=%d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/models", nil))
	var response ModelList
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Object != "list" || len(response.Data) != 1 || response.Data[0].Id != "codex" {
		t.Fatal(w.Body.String())
	}
}

func TestHTTPDisconnectCancelsHarness(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	backend := harnessFunc(func(ctx context.Context, _ domain.Request, emit func(string) error) (domain.Result, error) {
		if err := emit("started"); err != nil {
			return domain.Result{}, err
		}
		close(started)
		<-ctx.Done()
		close(stopped)
		return domain.Result{}, ctx.Err()
	})
	server := httptest.NewServer(handler(backend, time.Second))
	defer server.Close()
	response, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"codex","messages":[{"role":"user","content":"x"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	<-started
	_ = response.Body.Close()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("disconnected HTTP request left harness running")
	}
}

type catalogBackend struct {
	harnessFunc
	list func(context.Context) ([]string, error)
}

func (h catalogBackend) Models(ctx context.Context) ([]string, error) { return h.list(ctx) }

func TestNativeModelDiscoveryHTTP(t *testing.T) {
	backend := catalogBackend{list: func(context.Context) ([]string, error) { return []string{"native-one", "native-two"}, nil }}
	w := httptest.NewRecorder()
	handler(backend, time.Second).ServeHTTP(w, httptest.NewRequest("GET", "/v1/models", nil))
	var response ModelList
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(response.Data) != 3 || response.Data[1].Id != "codex/native-one" || response.Data[2].OwnedBy != "codex" {
		t.Fatalf("bad catalog: %d %s", w.Code, w.Body.String())
	}
}

func TestModelDiscoveryHTTPFailures(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmtBool(timeout), func(t *testing.T) {
			backend := catalogBackend{list: func(ctx context.Context) ([]string, error) {
				if timeout {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return nil, errors.New("private CLI detail")
			}}
			w := httptest.NewRecorder()
			handler(backend, 10*time.Millisecond).ServeHTTP(w, httptest.NewRequest("GET", "/v1/models", nil))
			want := 502
			if timeout {
				want = 504
			}
			if w.Code != want || !strings.Contains(w.Body.String(), "model_discovery_failed") || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestBusyModelDiscoveryReturns429(t *testing.T) {
	backend := catalogBackend{list: func(context.Context) ([]string, error) { return nil, application.ErrBusy }}
	w := httptest.NewRecorder()
	handler(backend, time.Second).ServeHTTP(w, httptest.NewRequest("GET", "/v1/models", nil))
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "gateway_busy") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestReasoningEffortForwarded(t *testing.T) {
	for _, effort := range []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max"} {
		t.Run("effort="+effort, func(t *testing.T) {
			backend := harnessFunc(func(_ context.Context, request domain.Request, emit func(string) error) (domain.Result, error) {
				if request.ReasoningEffort != effort {
					t.Fatalf("effort changed: got %q, want %q", request.ReasoningEffort, effort)
				}
				return domain.Result{}, emit("ok")
			})
			body := `{"model":"codex","messages":[{"role":"user","content":"x"}]`
			if effort != "" {
				body += `,"reasoning_effort":"` + effort + `"`
			}
			body += `}`
			w := httptest.NewRecorder()
			handler(backend, time.Second).ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
			if w.Code != http.StatusOK {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}
