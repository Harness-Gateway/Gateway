package openai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/harness-gateway/harness-gateway/api"
	"github.com/harness-gateway/harness-gateway/internal/application"
	"github.com/harness-gateway/harness-gateway/internal/domain"
)

func TestDocumentationRoutes(t *testing.T) {
	h := handler(harnessFunc(func(context.Context, domain.Request, func(string) error) (domain.Result, error) {
		t.Fatal("documentation started a harness")
		return domain.Result{}, nil
	}), time.Second)
	for _, test := range []struct{ path, contentType, contains string }{
		{"/docs", "text/html", "/docs/assets/scalar-1.70.0.js"},
		{"/docs/", "text/html", "/docs/init.js"},
		{"/docs/init.js", "text/javascript", "url: window.location.origin"},
		{"/docs/assets/scalar-1.70.0.js", "text/javascript", "Scalar"},
		{"/docs/assets/LICENSE.scalar", "text/plain", "MIT License"},
		{"/openapi.json", "application/json", "createChatCompletion"},
	} {
		t.Run(test.path, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest(method, test.path, nil))
				if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), test.contentType) {
					t.Fatalf("%s: status=%d headers=%v", method, w.Code, w.Header())
				}
				if method == http.MethodGet && !strings.Contains(w.Body.String(), test.contains) {
					t.Fatalf("%s body missing %q", method, test.contains)
				}
				if method == http.MethodHead && w.Body.Len() != 0 {
					t.Fatal("HEAD returned a body")
				}
				if test.contentType == "text/html" && !strings.Contains(w.Header().Get("Content-Security-Policy"), "connect-src 'self'") {
					t.Fatal("documentation allows off-origin API traffic")
				}
			}
		})
	}
	for _, path := range []string{"/docs/unknown", "/docs/assets/unknown.js", "/docs/assets/../README.md"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		// ServeMux canonicalizes paths with .. before dispatching them.
		if w.Code != http.StatusNotFound && w.Code != http.StatusMovedPermanently {
			t.Fatalf("unexpected docs route %q returned %d", path, w.Code)
		}
	}
}

func TestPinnedScalarBundle(t *testing.T) {
	const want = "ab654f973a5e5aa5ba0cb0f53635db8c9b2a0f3b454a613238c4fa223b2046e2"
	if got := fmt.Sprintf("%x", sha256.Sum256(scalarJS)); got != want {
		t.Fatalf("Scalar bundle changed: got %s, want %s", got, want)
	}
}

func TestDocumentationContract(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(api.OpenAPI, &document); err != nil {
		t.Fatal(err)
	}
	paths := document["paths"].(map[string]any)
	for path, method := range map[string]string{"/health": "get", "/v1/models": "get", "/v1/chat/completions": "post"} {
		operation, ok := paths[path].(map[string]any)[method].(map[string]any)
		if !ok || operation["operationId"] == "" {
			t.Fatalf("missing operation %s %s", method, path)
		}
	}
	if len(paths) != 3 || document["servers"].([]any)[0].(map[string]any)["url"] != "/" {
		t.Fatal("specification advertises unsupported endpoints or a fixed host/port")
	}
	var checkRefs func(any)
	checkRefs = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if ref, ok := value["$ref"].(string); ok {
				if !strings.HasPrefix(ref, "#/") {
					t.Fatalf("nonlocal specification reference %q", ref)
				}
				var target any = document
				for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
					object, ok := target.(map[string]any)
					if !ok || object[part] == nil {
						t.Fatalf("unresolved specification reference %q", ref)
					}
					target = object[part]
				}
			}
			for _, child := range value {
				checkRefs(child)
			}
		case []any:
			for _, child := range value {
				checkRefs(child)
			}
		}
	}
	checkRefs(document)
}

func TestDocumentedCompletionExamples(t *testing.T) {
	backend := harnessFunc(func(ctx context.Context, request domain.Request, emit func(string) error) (domain.Result, error) {
		return domain.Result{}, emit("Hello!")
	})
	h := New(application.New([]application.Backend{
		{Model: application.Model{ID: "codex", Provider: "codex"}, Harness: backend},
		{Model: application.Model{ID: "copilot", Provider: "copilot"}, Harness: backend},
	}, 1), time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var document struct {
		Paths map[string]struct {
			Post struct {
				RequestBody struct {
					Content map[string]struct {
						Examples map[string]struct{ Value json.RawMessage }
					}
				}
			}
		}
	}
	if err := json.Unmarshal(api.OpenAPI, &document); err != nil {
		t.Fatal(err)
	}
	examples := document.Paths["/v1/chat/completions"].Post.RequestBody.Content["application/json"].Examples
	if len(examples) != 4 {
		t.Fatalf("expected buffered and streaming examples for both providers, got %d", len(examples))
	}
	for name, example := range examples {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(example.Value))
			r.Header.Set("Content-Type", "application/json")
			h.ServeHTTP(w, r)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Hello!") {
				t.Fatalf("documented example failed: %d %s", w.Code, w.Body.String())
			}
			if strings.HasSuffix(name, "Stream") && !strings.HasSuffix(w.Body.String(), "data: [DONE]\n\n") {
				t.Fatal("streaming example did not finish")
			}
		})
	}
}
