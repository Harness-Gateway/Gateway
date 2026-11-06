package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/application"
	"github.com/harness-gateway/harness-gateway/internal/domain"
)

func TestCompletionContextOverridesTransportError(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, returnedError := range []error{nil, errors.New("private broken pipe"), &domain.RequestError{Cause: errors.New("private invalid model")}} {
			t.Run(fmt.Sprintf("stream=%t/error=%v", stream, returnedError), func(t *testing.T) {
				backend := harnessFunc(func(ctx context.Context, req domain.Request, emit func(string) error) (domain.Result, error) {
					<-ctx.Done()
					return domain.Result{}, returnedError
				})
				w := httptest.NewRecorder()
				body := fmt.Sprintf(`{"model":"codex","messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream)
				handler(backend, 5*time.Millisecond).ServeHTTP(w, localRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
				want := 504
				if stream {
					want = 200
				}
				if w.Code != want || !strings.Contains(w.Body.String(), "harness_timeout") || strings.Contains(w.Body.String(), "[DONE]") || strings.Contains(w.Body.String(), "private") {
					t.Fatalf("status=%d: %s", w.Code, w.Body.String())
				}
			})
		}
	}
}

func TestDisconnectSuppressesTransportError(t *testing.T) {
	for _, stream := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		backend := harnessFunc(func(context.Context, domain.Request, func(string) error) (domain.Result, error) {
			cancel()
			return domain.Result{}, errors.New("private broken pipe after disconnect")
		})
		var logs bytes.Buffer
		gateway := application.New([]application.Backend{{Model: application.Model{ID: "codex"}, Harness: backend}}, 1)
		h := New(gateway, time.Second, slog.New(slog.NewTextHandler(&logs, nil)))
		w := httptest.NewRecorder()
		body := fmt.Sprintf(`{"model":"codex","messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream)
		r := localRequest("POST", "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
		h.ServeHTTP(w, r)
		cancel()
		if strings.Contains(w.Body.String(), `"error"`) || strings.Contains(w.Body.String(), "[DONE]") || strings.Contains(logs.String(), "level=ERROR") {
			t.Fatalf("disconnect was reported as a failure: %s\n%s", w.Body.String(), logs.String())
		}
	}
}

func TestStructuredRequestErrorIsSanitized(t *testing.T) {
	for _, stream := range []bool{false, true} {
		backend := harnessFunc(func(context.Context, domain.Request, func(string) error) (domain.Result, error) {
			return domain.Result{}, fmt.Errorf("wrapped: %w", &domain.RequestError{Cause: errors.New("private CLI detail")})
		})
		w := httptest.NewRecorder()
		body := fmt.Sprintf(`{"model":"codex","messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream)
		handler(backend, time.Second).ServeHTTP(w, localRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		want := 400
		if stream {
			want = 200
		}
		if w.Code != want || !strings.Contains(w.Body.String(), `"type":"invalid_request_error"`) || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "[DONE]") {
			t.Fatalf("status=%d: %s", w.Code, w.Body.String())
		}
	}
}
