package openai

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/application"
	"github.com/harness-gateway/harness-gateway/internal/domain"
)

func TestResponseTextLimitAndRecovery(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, excess := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/excess=%t", stream, excess), func(t *testing.T) {
				// Non-ASCII text verifies a byte limit, rather than a rune count.
				delta := strings.Repeat("é", 128*1024)
				backend := harnessFunc(func(_ context.Context, request domain.Request, emit func(string) error) (domain.Result, error) {
					if request.Messages[0].Text == "recover" {
						return domain.Result{}, emit("recovered")
					}
					for size := 0; size < maxResponseBytes; size += len(delta) {
						if err := emit(delta); err != nil {
							return domain.Result{}, err
						}
					}
					if excess {
						return domain.Result{}, emit("z")
					}
					return domain.Result{}, nil
				})
				h := handler(backend, 30*time.Second)
				w := httptest.NewRecorder()
				body := `{"model":"codex","messages":[{"role":"user","content":"limit"}],"stream":` + fmtBool(stream) + `}`
				h.ServeHTTP(w, localRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
				wantStatus := http.StatusOK
				if excess && !stream {
					wantStatus = http.StatusBadGateway
				}
				if w.Code != wantStatus {
					t.Fatalf("status=%d, expected %d", w.Code, wantStatus)
				}
				if stream {
					textBytes, failures, finishes, dones := 0, 0, 0, 0
					reader := bufio.NewScanner(w.Body)
					reader.Buffer(make([]byte, 4096), 1024*1024)
					for reader.Scan() {
						line := reader.Text()
						if line == "" {
							continue
						}
						data := strings.TrimPrefix(line, "data: ")
						if data == "[DONE]" {
							dones++
							continue
						}
						var frame struct {
							Chunk
							Error *APIError `json:"error"`
						}
						if err := json.Unmarshal([]byte(data), &frame); err != nil {
							t.Fatal(err)
						}
						if frame.Error != nil {
							failures++
							if frame.Error.Code == nil || *frame.Error.Code != "harness_error" {
								t.Fatal("response limit did not surface as a harness error")
							}
						}
						for _, choice := range frame.Choices {
							if choice.Delta.Content != nil {
								if strings.Contains(*choice.Delta.Content, "z") {
									t.Fatal("text exceeding the limit was emitted")
								}
								textBytes += len(*choice.Delta.Content)
							}
							if choice.FinishReason != nil {
								finishes++
							}
						}
					}
					if err := reader.Err(); err != nil {
						t.Fatal(err)
					}
					if textBytes != maxResponseBytes || (excess && (failures != 1 || finishes != 0 || dones != 0)) || (!excess && (failures != 0 || finishes != 1 || dones != 1)) {
						t.Fatalf("bad boundary response: bytes=%d errors=%d finishes=%d DONE=%d", textBytes, failures, finishes, dones)
					}
				} else if excess {
					var failure ErrorResponse
					if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || failure.Error.Code == nil || *failure.Error.Code != "harness_error" {
						t.Fatal("buffered limit failure did not return an error envelope")
					}
				} else {
					var response Completion
					if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					if len(response.Choices) != 1 || response.Choices[0].Message.Content == nil || len(*response.Choices[0].Message.Content) != maxResponseBytes {
						t.Fatal("exactly 16 MiB of assistant text was not accepted")
					}
				}
				assertRecovery(t, h)
			})
		}
	}
}

func assertRecovery(t *testing.T, h http.Handler) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, localRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"codex","messages":[{"role":"user","content":"recover"}]}`)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"content":"recovered"`) {
		t.Fatalf("gateway did not recover: %d", w.Code)
	}
}

type writeDeadlineRecorder struct {
	http.ResponseWriter
	deadline *time.Time
}

func (w writeDeadlineRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w writeDeadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	if err := http.NewResponseController(w.ResponseWriter).SetWriteDeadline(deadline); err != nil {
		return err
	}
	*w.deadline = deadline
	return nil
}

func TestSlowReadersCannotRetainResponseHandlers(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmtBool(stream), func(t *testing.T) {
			finished := make(chan struct{})
			var lastWriteDeadline, finishedAt time.Time
			backendStopped := make(chan struct{})
			delta := strings.Repeat("x", 256*1024)
			backend := harnessFunc(func(ctx context.Context, request domain.Request, emit func(string) error) (domain.Result, error) {
				if request.Messages[0].Text == "recover" {
					return domain.Result{}, emit("recovered")
				}
				defer close(backendStopped)
				for size := 0; size < maxResponseBytes; size += len(delta) {
					if err := emit(delta); err != nil {
						return domain.Result{}, err
					}
				}
				return domain.Result{}, nil
			})
			gateway := application.New([]application.Backend{{Model: application.Model{ID: "codex"}, Harness: backend}}, 1)
			h := New(gateway, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				h.ServeHTTP(writeDeadlineRecorder{w, &lastWriteDeadline}, r)
				if r.Header.Get("X-Test-Slow") == "1" {
					finishedAt = time.Now()
					close(finished)
				}
			}))
			defer server.Close()
			connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			if err := connection.(*net.TCPConn).SetReadBuffer(1024); err != nil {
				t.Fatal(err)
			}
			body := `{"model":"codex","messages":[{"role":"user","content":"limit"}],"stream":` + fmtBool(stream) + `}`
			if _, err := fmt.Fprintf(connection, "POST /v1/chat/completions HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nX-Test-Slow: 1\r\nContent-Length: %d\r\n\r\n%s", len(body), body); err != nil {
				t.Fatal(err)
			}
			if err := connection.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(connection)
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					t.Fatal(err)
				}
				if line == "\r\n" {
					break
				}
			}
			// Keep the socket open without consuming its body. Only the server's
			// write deadline may release the handler; client cancellation cannot.
			select {
			case <-finished:
			case <-time.After(13 * time.Second):
				t.Fatal("slow reader retained the response handler beyond its 10s write budget")
			}
			// Buffered JSON encoding consumes part of the server's write budget
			// before headers reach the client. Observe the actual deadline rather
			// than starting a second clock after the client reads those headers.
			if lastWriteDeadline.IsZero() || finishedAt.Before(lastWriteDeadline.Add(-100*time.Millisecond)) {
				t.Fatal("handler finished before exercising its recorded write deadline")
			}
			if finishedAt.After(lastWriteDeadline.Add(3 * time.Second)) {
				t.Fatal("handler exceeded its recorded write deadline and cleanup allowance")
			}
			select {
			case <-backendStopped:
			default:
				t.Fatal("slow reader left the backend active")
			}
			assertRecovery(t, h)
		})
	}
}
