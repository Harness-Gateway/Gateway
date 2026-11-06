package copilot

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/harness-gateway/harness-gateway/internal/adapters/inbound/openai"
	"github.com/harness-gateway/harness-gateway/internal/application"
	"github.com/harness-gateway/harness-gateway/internal/testutil"
)

func childPID(t *testing.T, marker string) int {
	t.Helper()
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func TestOperationStopsDescendants(t *testing.T) {
	for _, mode := range []string{"normal", "hold", "crash", "startup-crash", "catalog-error"} {
		t.Run(mode, func(t *testing.T) {
			h, _, _ := testHarness(t, "descendant-"+mode)
			marker := filepath.Join(t.TempDir(), "child-pid")
			t.Setenv("HARNESS_GATEWAY_COPILOT_CHILD", marker)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var err error
			if mode == "catalog-error" {
				_, err = h.Models(ctx)
			} else {
				_, err = h.Complete(ctx, testRequest(), func(string) error {
					if mode == "hold" {
						cancel()
					}
					return nil
				})
			}
			pid := childPID(t, marker)
			t.Cleanup(func() {
				if running, _ := testutil.ProcessRunning(pid); running {
					process, _ := os.FindProcess(pid)
					if process != nil {
						_ = process.Kill()
					}
				}
			})
			if mode == "normal" && err != nil {
				t.Fatal(err)
			}
			if mode != "normal" && err == nil {
				t.Fatal("fixture failure was lost")
			}
			if mode == "hold" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				running, err := testutil.ProcessRunning(pid)
				if err != nil {
					t.Fatal(err)
				}
				if !running {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("descendant PID %d survived %s cleanup", pid, mode)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestFrameGuardRejectsBeforeSDKAllocation(t *testing.T) {
	for _, mode := range []string{"oversized-frame", "oversized-header"} {
		t.Run(mode, func(t *testing.T) {
			h, _, _ := testHarness(t, mode)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			_, err := h.Complete(ctx, testRequest(), func(string) error { t.Fatal("malformed frame emitted text"); return nil })
			runtime.ReadMemStats(&after)
			if err == nil || !strings.Contains(err.Error(), "copilot protocol") || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("bad frame was not rejected promptly: %v", err)
			}
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8*1024*1024 {
				t.Fatalf("header caused oversized allocation: %d bytes", allocated)
			}
		})
	}
}

func TestFrameGuardHTTPFailureAndRecovery(t *testing.T) {
	h, _, _ := testHarness(t, "oversized-frame")
	server := openai.New(application.New([]application.Backend{{Model: application.Model{ID: "copilot"}, Harness: h}}, 1), 5*time.Second, nil)
	for _, mode := range []string{"oversized-frame", "normal"} {
		t.Setenv("HARNESS_GATEWAY_COPILOT_TEST", mode)
		request := httptest.NewRequest("POST", "http://127.0.0.1/v1/chat/completions", strings.NewReader(`{"model":"copilot","messages":[{"role":"user","content":"hello"}]}`))
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if mode == "oversized-frame" {
			if response.Code != 502 || !strings.Contains(response.Body.String(), "harness_error") {
				t.Fatalf("bad frame did not fail safely: %d %s", response.Code, response.Body.String())
			}
		} else if response.Code != 200 || !strings.Contains(response.Body.String(), "hello world") {
			t.Fatalf("capacity did not recover: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestPartialFrameCancellation(t *testing.T) {
	h, _, pidFile := testHarness(t, "partial-frame")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err := h.Complete(ctx, testRequest(), func(string) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("partial frame did not honor the request deadline: %v", err)
	}
	if _, err := os.Stat(pidFile); err != nil {
		t.Fatalf("partial-frame fixture did not start: %v", err)
	}
}

func TestEscapedResponseThroughSDKAndFrameGuard(t *testing.T) {
	h, _, _ := testHarness(t, "escaped")
	// This checks wire-size compatibility, not parsing speed under -race.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	var response strings.Builder
	_, err := h.Complete(ctx, testRequest(), func(text string) error { response.WriteString(text); return nil })
	if err != nil || response.String() != strings.Repeat("\x00", 3*1024*1024) {
		t.Fatalf("escaped response was lost or truncated: bytes=%d err=%v", response.Len(), err)
	}
}

func TestBridgeCloseWhileWaitingForHandshake(t *testing.T) {
	h, _, _ := testHarness(t, "normal")
	bridge, err := startBridge(t.Context(), h.Path, h.Directory)
	if err != nil {
		t.Fatal(err)
	}
	address := bridge.listener.Addr().String()
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Close(); err != nil {
		t.Fatalf("repeated close failed: %v", err)
	}
	conn, err := net.DialTimeout("tcp4", address, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatal("closed bridge still accepts connections")
	}
}

func TestPrivateBridgeRejectsUnauthenticatedClientAndRecovers(t *testing.T) {
	h, _, _ := testHarness(t, "normal")
	bridge, err := startBridge(t.Context(), h.Path, h.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	conn, err := net.DialTimeout("tcp4", bridge.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	bad := `{"id":1,"method":"connect","params":{"token":"wrong"}}`
	if _, err := fmt.Fprintf(conn, "Content-Length: %d\r\n\r\n%s", len(bad), bad); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(conn).ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("unauthorized client was not disconnected: %v", err)
	}
	client := sdk.NewClient(&sdk.ClientOptions{Connection: sdk.URIConnection{URL: bridge.listener.Addr().String(), ConnectionToken: bridge.token}})
	defer client.ForceStop()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatalf("legitimate SDK could not connect after rejected client: %v", err)
	}
	if _, err := client.ListModels(ctx); err != nil {
		t.Fatal(err)
	}
}
