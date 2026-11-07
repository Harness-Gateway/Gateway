package openai

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/application"
	"github.com/harness-gateway/harness-gateway/internal/domain"
)

func assertDiscoveryShutdown(t *testing.T, status int, body []byte) {
	t.Helper()
	var response ErrorResponse
	if status != http.StatusServiceUnavailable || json.Unmarshal(body, &response) != nil ||
		response.Error.Code == nil || *response.Error.Code != "gateway_shutting_down" || response.Error.Type != "server_error" {
		t.Fatalf("shutdown did not produce an explicit API error: %d %s", status, body)
	}
}

func TestConcurrentModelDiscoveryShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(ErrShuttingDown)
		var calls atomic.Int32
		stopped := make(chan struct{})
		backend := catalogBackend{list: func(ctx context.Context) ([]string, error) {
			if calls.Add(1) == 1 {
				defer close(stopped)
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		h := handler(backend, time.Minute)
		responses := make(chan *httptest.ResponseRecorder, 32)
		for range cap(responses) {
			go func() {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, localRequest(http.MethodGet, "/v1/models", nil).WithContext(ctx))
				responses <- w
			}()
		}
		// All handlers and the shared backend are blocked inside this bubble.
		// Cancel only after the callers have joined the same refresh.
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatalf("requests did not share discovery: calls=%d", calls.Load())
		}
		cancel(ErrShuttingDown)
		for range cap(responses) {
			w := <-responses
			assertDiscoveryShutdown(t, w.Code, w.Body.Bytes())
		}
		select {
		case <-stopped:
		default:
			t.Fatal("shutdown returned before discovery cleanup")
		}
		if calls.Load() != 1 {
			t.Fatal("shutdown restarted discovery")
		}
	})
}

func TestConcurrentModelDiscoveryShutdownOverTCP(t *testing.T) {
	const clients = 24
	root, cancel := context.WithCancelCause(t.Context())
	started, stopped, arrived := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls, arrivals atomic.Int32
	backend := catalogBackend{
		harnessFunc: func(_ context.Context, _ domain.Request, emit func(string) error) (domain.Result, error) {
			return domain.Result{}, emit("recovered")
		},
		list: func(ctx context.Context) ([]string, error) {
			if calls.Add(1) == 1 {
				close(started)
				defer close(stopped)
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	gateway := application.New([]application.Backend{{Model: application.Model{ID: "codex"}, Harness: backend}}, 1)
	h := New(gateway, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if arrivals.Add(1) == clients {
			close(arrived)
		}
		h.ServeHTTP(w, r)
	}))
	server.Config.BaseContext = func(net.Listener) context.Context { return root }
	server.Start()
	client := server.Client()
	client.Timeout = 10 * time.Second
	t.Cleanup(func() {
		cancel(ErrShuttingDown)
		server.CloseClientConnections()
		server.Close()
		client.CloseIdleConnections()
	})
	type result struct {
		method string
		status int
		body   []byte
		err    error
	}
	responses := make(chan result, clients)
	for i := range clients {
		method := http.MethodGet
		if i%2 != 0 {
			method = http.MethodHead
		}
		go func() {
			r := result{method: method}
			request, err := http.NewRequestWithContext(t.Context(), method, server.URL+"/v1/models", nil)
			if err == nil {
				var response *http.Response
				response, err = client.Do(request)
				if err == nil {
					r.status = response.StatusCode
					r.body, err = io.ReadAll(response.Body)
					closeErr := response.Body.Close()
					if err == nil {
						err = closeErr
					}
				}
			}
			r.err = err
			responses <- r
		}()
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for _, signal := range []<-chan struct{}{arrived, started} {
		select {
		case <-signal:
		case <-deadline.C:
			t.Fatalf("HTTP discovery did not start: arrivals=%d backend_calls=%d", arrivals.Load(), calls.Load())
		}
	}
	cancel(ErrShuttingDown)
	shutdown, stopShutdown := context.WithTimeout(t.Context(), 5*time.Second)
	defer stopShutdown()
	if err := server.Config.Shutdown(shutdown); err != nil {
		t.Fatal(err)
	}
	for range clients {
		r := <-responses
		if r.err != nil {
			t.Fatalf("shutdown dropped a %s connection instead of returning an API response: %v", r.method, r.err)
		}
		if r.method == http.MethodGet {
			assertDiscoveryShutdown(t, r.status, r.body)
		} else if r.status != http.StatusServiceUnavailable || len(r.body) != 0 {
			t.Fatalf("HEAD shutdown response: %d %s", r.status, r.body)
		}
	}
	select {
	case <-stopped:
	default:
		t.Fatal("shared discovery survived HTTP server shutdown")
	}
	if calls.Load() != 1 {
		t.Fatalf("HTTP shutdown started duplicate discovery: %d", calls.Load())
	}
	var output strings.Builder
	if _, err := gateway.Complete(t.Context(), "codex", domain.Request{}, func(text string) error {
		_, err := output.WriteString(text)
		return err
	}, nil); err != nil || output.String() != "recovered" {
		t.Fatalf("shutdown retained backend capacity: output=%q error=%v", output.String(), err)
	}
}

func TestModelCacheExpiryThroughHTTPHandler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		h := handler(catalogBackend{list: func(context.Context) ([]string, error) {
			if calls.Add(1) == 1 {
				return []string{"old"}, nil
			}
			return []string{"fresh"}, nil
		}}, time.Minute)
		get := func(method, id string, wantCalls int32) {
			t.Helper()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, localRequest(method, "/v1/models", nil))
			var response ModelList
			if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &response) != nil ||
				len(response.Data) != 2 || response.Data[1].Id != "codex/"+id || calls.Load() != wantCalls {
				t.Fatalf("HTTP catalog: status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
			}
		}
		get(http.MethodGet, "old", 1)
		time.Sleep(29 * time.Second)
		get(http.MethodHead, "old", 1)
		time.Sleep(time.Second)
		get(http.MethodGet, "fresh", 2)
		get(http.MethodHead, "fresh", 2)
	})
}
