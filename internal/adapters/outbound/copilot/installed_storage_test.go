package copilot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/harness-gateway/harness-gateway/internal/testutil"
)

// Verify the real runtime honors the storage controls used by Complete.
// Fixture tests separately exercise Complete's cleanup on every exit path.
func TestInstalledCLIIsolatedSessionStorage(t *testing.T) {
	path := testutil.InstalledCLI(t, "copilot")
	for _, mode := range []string{"success", "cancel", "crash"} {
		t.Run(mode, func(t *testing.T) {
			profile := t.TempDir()
			t.Setenv("COPILOT_HOME", profile)
			t.Setenv("COPILOT_OFFLINE", "true")
			t.Setenv("COPILOT_MODEL", "gateway-local-test")
			for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "COPILOT_GITHUB_TOKEN", "COPILOT_PROVIDER_API_KEY", "COPILOT_PROVIDER_API_KEY_COMMAND", "COPILOT_PROVIDER_BEARER_TOKEN", "COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_TYPE", "COPILOT_PROVIDER_WIRE_API", "COPILOT_PROVIDER_TRANSPORT"} {
				t.Setenv(key, "")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
					t.Errorf("unexpected mock provider request: %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected endpoint", http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: {\"id\":\"local\",\"object\":\"chat.completion.chunk\",\"model\":\"gateway-local-test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"local response\"},\"finish_reason\":null}]}\n\n")
				if mode == "success" {
					_, _ = fmt.Fprint(w, "data: {\"id\":\"local\",\"object\":\"chat.completion.chunk\",\"model\":\"gateway-local-test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					return
				}
				_ = http.NewResponseController(w).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			// Required by offline CLI startup; the SDK session also explicitly
			// selects this local provider rather than account-backed inference.
			t.Setenv("COPILOT_PROVIDER_BASE_URL", server.URL+"/v1")
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			h := Harness{Path: path, Directory: t.TempDir()}
			client, _, stop, err := h.connect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := stop(); err != nil {
					t.Error(err)
				}
			}()
			state := t.TempDir()
			session, err := client.CreateSession(ctx, &sdk.SessionConfig{
				Model: "gateway-local-test", ConfigDirectory: state,
				EnableSessionStore: sdk.Bool(false), Streaming: sdk.Bool(true),
				AvailableTools: []string{}, SkipCustomInstructions: sdk.Bool(true),
				EnableConfigDiscovery: sdk.Bool(false), EnableSkills: sdk.Bool(false), EnableFileHooks: sdk.Bool(false),
				InfiniteSessions: &sdk.InfiniteSessionConfig{Enabled: sdk.Bool(false)},
				Provider:         &sdk.ProviderConfig{Type: "openai", BaseURL: server.URL + "/v1"},
			})
			if err != nil {
				t.Fatal(err)
			}
			ready := make(chan struct{}, 1)
			unsubscribe := session.On(func(event sdk.SessionEvent) {
				if data, ok := event.Data.(*sdk.AssistantMessageDeltaData); ok && data.DeltaContent != "" {
					select {
					case ready <- struct{}{}:
					default:
					}
				}
			})
			defer unsubscribe()
			completed := make(chan error, 1)
			go func() {
				_, err := session.SendAndWait(ctx, sdk.MessageOptions{Prompt: "Reply with local response"})
				completed <- err
			}()
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatal("runtime did not stream from the local provider")
			}
			if _, err := os.Stat(filepath.Join(state, "session-state", session.SessionID)); err != nil {
				t.Fatalf("runtime did not use the isolated state directory: %v", err)
			}
			if mode == "cancel" {
				cancel()
			} else if mode == "crash" {
				if err := stop(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-completed:
				if mode == "success" && err != nil || mode != "success" && err == nil {
					t.Fatalf("unexpected %s result: %v", mode, err)
				}
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("request cancellation was lost: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("completion did not terminate")
			}
			if err := stop(); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"session-state", "session-store.db", "session-store.db-wal", "session-store.db-shm"} {
				if _, err := os.Stat(filepath.Join(profile, name)); !os.IsNotExist(err) {
					t.Errorf("session data escaped into the CLI profile (%s): %v", name, err)
				}
			}
			// Windows also verifies the stopped runtime has released file handles.
			if err := os.RemoveAll(state); err != nil {
				t.Fatalf("isolated runtime state could not be removed: %v", err)
			}
		})
	}
}
