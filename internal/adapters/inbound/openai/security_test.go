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

func TestLocalRequestProtection(t *testing.T) {
	for _, tc := range []struct {
		name, host, origin, site string
		want                     int
	}{
		{"curl", "127.0.0.1:8080", "", "", 200},
		{"localhost", "LOCALHOST:8080", "", "", 200},
		{"IPv6", "[::1]:8080", "", "", 200},
		{"IPv6 without port", "[::1]", "", "", 200},
		{"Scalar", "127.0.0.1:8080", "http://127.0.0.1:8080", "same-origin", 200},
		{"legacy same origin", "localhost:8080", "http://localhost:8080", "", 200},
		{"cross site", "127.0.0.1:8080", "http://attacker.example", "cross-site", 403},
		{"legacy cross origin", "127.0.0.1:8080", "http://attacker.example", "", 403},
		{"different local port", "127.0.0.1:8080", "http://127.0.0.1:9090", "", 403},
		{"same site different origin", "localhost:8080", "http://localhost:9090", "same-site", 403},
		{"opaque origin", "localhost:8080", "null", "", 403},
		{"rebound host", "attacker.example:8080", "http://attacker.example:8080", "same-origin", 403},
		{"empty host", "", "", "", 403},
		{"public IP", "192.0.2.1:8080", "", "", 403},
		{"userinfo", "user@localhost:8080", "", "", 403},
		{"malformed port", "localhost:wrong", "", "", 403},
		{"invalid port", "localhost:65536", "", "", 403},
		{"empty port", "localhost:", "", "", 403},
		{"unbracketed IPv6", "::1", "", "", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			backend := harnessFunc(func(ctx context.Context, req domain.Request, emit func(string) error) (domain.Result, error) {
				calls++
				return domain.Result{}, emit("hello")
			})
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"codex","messages":[{"role":"user","content":"hello"}]}`))
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.site != "" {
				r.Header.Set("Sec-Fetch-Site", tc.site)
			}
			w := httptest.NewRecorder()
			handler(backend, time.Second).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d: %s", w.Code, w.Body.String())
			}
			if (calls == 1) != (tc.want == 200) {
				t.Fatalf("unexpected backend calls: %d", calls)
			}
			if tc.want == 403 {
				var response ErrorResponse
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Error.Code == nil || *response.Error.Code != "forbidden_origin" {
					t.Fatalf("missing API error: %s", w.Body.String())
				}
			}
		})
	}
}

func TestHostProtectionAlsoAppliesToSafeMethods(t *testing.T) {
	h := protectLocalRequests(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("foreign Host reached endpoint")
	}))
	for _, path := range []string{"/", "/health", "/docs", "/v1/models"} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
			r := httptest.NewRequest(method, "http://untrusted.example"+path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatalf("%s %s: %d", method, path, w.Code)
			}
		}
	}
}
