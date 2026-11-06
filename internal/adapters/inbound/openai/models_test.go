package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/application"
)

func TestModelDiscoveryErrorClassification(t *testing.T) {
	for _, test := range []struct {
		name        string
		cause       error
		failure     error
		lateSuccess bool
		timeout     bool
		status      int
		code, level string
		event       string
	}{
		{name: "shutdown", cause: ErrShuttingDown, status: 503, code: "gateway_shutting_down", level: "INFO", event: "model discovery interrupted"},
		{name: "shutdown-transport-error", cause: ErrShuttingDown, failure: errors.New("client stopped"), status: 503, code: "gateway_shutting_down", level: "INFO", event: "model discovery interrupted"},
		{name: "success-racing-shutdown", cause: ErrShuttingDown, lateSuccess: true, status: 503, code: "gateway_shutting_down", level: "INFO", event: "model discovery interrupted"},
		{name: "client-disconnect", cause: context.Canceled},
		{name: "client-disconnect-transport-error", cause: context.Canceled, failure: errors.New("client stopped")},
		{name: "busy", failure: application.ErrBusy, status: 429, code: "gateway_busy", level: "INFO", event: "model discovery rejected"},
		{name: "cli-failure", failure: errors.New("private CLI detail"), status: 502, code: "model_discovery_failed", level: "ERROR", event: "model discovery failed"},
		{name: "timeout", timeout: true, status: 504, code: "model_discovery_failed", level: "ERROR", event: "model discovery failed"},
		{name: "timeout-transport-error", timeout: true, failure: errors.New("connection closed"), status: 504, code: "model_discovery_failed", level: "ERROR", event: "model discovery failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(context.Canceled)
			backend := catalogBackend{list: func(ctx context.Context) ([]string, error) {
				if test.timeout {
					<-ctx.Done()
				}
				if test.cause != nil {
					cancel(test.cause)
				}
				if test.failure != nil {
					return nil, test.failure
				}
				if test.lateSuccess {
					return []string{"native"}, nil
				}
				return nil, ctx.Err()
			}}
			gateway := application.New([]application.Backend{{Model: application.Model{ID: "codex"}, Harness: backend}}, 1)
			var logs bytes.Buffer
			timeout := time.Second
			if test.timeout {
				timeout = 10 * time.Millisecond
			}
			h := New(gateway, timeout, slog.New(slog.NewJSONHandler(&logs, nil)))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil).WithContext(ctx))
			if test.status == 0 {
				if w.Body.Len() != 0 || logs.Len() != 0 {
					t.Fatalf("client cancellation wrote a response or logged a failure: body=%s logs=%s", w.Body.String(), logs.String())
				}
				return
			}
			var response ErrorResponse
			if w.Code != test.status || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Error.Code == nil || *response.Error.Code != test.code || response.Error.Type != "server_error" {
				t.Fatalf("expected HTTP %d %s, got %d %s", test.status, test.code, w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "private CLI detail") {
				t.Fatal("discovery exposed private CLI diagnostics to the client")
			}
			var record struct {
				Level string `json:"level"`
				Msg   string `json:"msg"`
				Code  string `json:"code"`
			}
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil || record.Level != test.level || record.Msg != test.event || record.Code != test.code {
				t.Fatalf("wrong discovery log classification: %v %s", err, logs.String())
			}
		})
	}
}
