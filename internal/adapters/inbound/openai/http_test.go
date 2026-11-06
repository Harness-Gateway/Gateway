package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/domain"
)

func TestAPIMethodErrors(t *testing.T) {
	h := handler(harnessFunc(func(context.Context, domain.Request, func(string) error) (domain.Result, error) {
		t.Fatal("unsupported method started inference")
		return domain.Result{}, nil
	}), time.Second)
	for _, test := range []struct{ method, path, allow string }{
		{http.MethodGet, "/v1/chat/completions", "POST"},
		{http.MethodHead, "/v1/chat/completions", "POST"},
		{http.MethodOptions, "/v1/chat/completions", "POST"},
		{http.MethodPut, "/v1/chat/completions", "POST"},
		{http.MethodPost, "/v1/models", "GET, HEAD"},
		{http.MethodDelete, "/health", "GET, HEAD"},
		{http.MethodOptions, "/health", "GET, HEAD"},
		{http.MethodPost, "/unknown", ""},
		{http.MethodGet, "/v1/chat/completions/unknown", ""},
		{http.MethodPost, "/v1/models/unknown", ""},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(test.method, test.path, nil))
			status, code := http.StatusMethodNotAllowed, "method_not_allowed"
			if test.allow == "" {
				status, code = http.StatusNotFound, "not_found"
			}
			if w.Code != status || w.Header().Get("Allow") != test.allow || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
			}
			var response ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Error.Code == nil || *response.Error.Code != code || response.Error.Type != "invalid_request_error" {
				t.Fatalf("missing OpenAI-style %s error: %v %s", code, err, w.Body.String())
			}
		})
	}
}

func TestRequestBodyLimitIncludesTrailingData(t *testing.T) {
	const valid = `{"model":"codex","messages":[{"role":"user","content":"hello"}]}`
	boundary := valid + strings.Repeat(" ", maxRequestBytes-len(valid))
	for _, framing := range []string{"content-length", "streamed"} {
		for _, test := range []struct {
			name, body string
			status     int
		}{
			{"exact-limit", boundary, http.StatusOK},
			{"trailing-whitespace-over-limit", boundary + " ", http.StatusRequestEntityTooLarge},
			{"json-over-limit", `{"model":"codex","messages":[{"role":"user","content":"` + strings.Repeat("x", maxRequestBytes) + `"}]}`, http.StatusRequestEntityTooLarge},
			{"second-object", valid + " {}", http.StatusBadRequest},
			{"malformed-trailer", valid + " !", http.StatusBadRequest},
		} {
			t.Run(framing+"/"+test.name, func(t *testing.T) {
				calls := 0
				h := handler(harnessFunc(func(_ context.Context, _ domain.Request, emit func(string) error) (domain.Result, error) {
					calls++
					return domain.Result{}, emit("ok")
				}), time.Second)
				r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(test.body))
				if framing == "streamed" {
					r.ContentLength = -1
					r.TransferEncoding = []string{"chunked"}
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != test.status {
					t.Fatalf("status=%d, want %d: %s", w.Code, test.status, w.Body.String())
				}
				if test.status == http.StatusOK {
					if calls != 1 {
						t.Fatal("request exactly at the limit did not reach the harness once")
					}
					return
				}
				if calls != 0 {
					t.Fatal("rejected request started inference")
				}
				var response ErrorResponse
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Error.Code == nil {
					t.Fatalf("missing OpenAI error envelope: %v %s", err, w.Body.String())
				}
				if test.status == http.StatusRequestEntityTooLarge && *response.Error.Code != "request_too_large" {
					t.Fatalf("wrong size-limit error: %s", w.Body.String())
				}
			})
		}
	}
}
