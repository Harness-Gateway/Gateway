package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestInvalidReleaseIdentityFailsBeforeInstalling(t *testing.T) {
	for _, version := range []string{"", "latest", "main", "v1.2.3/../../main", "v01.2.3", "v1.2.3 --help", "v1.2.3-rc.1"} {
		if _, err := verify(t.Context(), version, strings.Repeat("a", 40), ""); err == nil {
			t.Fatalf("accepted invalid version %q", version)
		}
	}
	if _, err := verify(t.Context(), "v1.2.3", "short", ""); err == nil {
		t.Fatal("accepted incomplete tag revision")
	}
}

func TestPublicModuleMustMatchTagAndChecksum(t *testing.T) {
	valid := moduleInfo{Path: modulePath, Version: "v1.2.3", Dir: "/module", Sum: "h1:checksum"}
	valid.Origin.Hash, valid.Origin.Ref = strings.Repeat("a", 40), "refs/tags/v1.2.3"
	if err := validateModule(valid, valid.Version, valid.Origin.Hash); err != nil {
		t.Fatal(err)
	}
	for _, corrupt := range []func(*moduleInfo){
		func(m *moduleInfo) { m.Path = "example.com/fork" },
		func(m *moduleInfo) { m.Version = "v1.2.4" },
		func(m *moduleInfo) { m.Origin.Hash = strings.Repeat("b", 40) },
		func(m *moduleInfo) { m.Origin.Ref = "refs/heads/main" },
		func(m *moduleInfo) { m.Sum = "" },
		func(m *moduleInfo) { m.Dir = "" },
	} {
		broken := valid
		corrupt(&broken)
		if err := validateModule(broken, valid.Version, valid.Origin.Hash); err == nil {
			t.Fatalf("accepted corrupt module %+v", broken)
		}
	}
}

func TestPublicInstallCannotInheritProxyOrPrivateSettings(t *testing.T) {
	work := t.TempDir()
	env := publicEnvironment([]string{"PATH=/bin", "GOPROXY=file:///fake", "GOSUMDB=off", "GOPRIVATE=*", "GONOSUMDB=*", "GONOPROXY=*", "GOENV=/custom", "GOFLAGS=-ldflags=bad", "HOME=/real-home", "GOBIN=/real-bin", "GOMODCACHE=/real-cache"}, work)
	values := make(map[string]string)
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if _, exists := values[key]; exists {
			t.Fatalf("duplicate environment setting %s", key)
		}
		values[key] = value
	}
	for key, expected := range map[string]string{"PATH": "/bin", "GOPROXY": "https://proxy.golang.org", "GOSUMDB": "sum.golang.org", "GOPRIVATE": "", "GONOSUMDB": "", "GONOPROXY": "", "GOENV": "off", "GOFLAGS": "", "GOBIN": "", "HOME": filepath.Join(work, "home"), "GOMODCACHE": filepath.Join(work, "module-cache")} {
		if values[key] != expected {
			t.Fatalf("%s = %q, expected %q", key, values[key], expected)
		}
	}
}

func TestStreamSmokeRejectsTruncationErrorsAndWrongModels(t *testing.T) {
	text := `data: {"model":"codex/native-one","choices":[{"delta":{"content":"hello world"}}]}` + "\n\n"
	finish := `data: {"model":"codex/native-one","choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n"
	if err := checkStream([]byte(text+finish+"data: [DONE]\n\n"), "codex/native-one"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{text, text + "data: [DONE]\n\n", text + "data: {\"error\":{}}\n\ndata: [DONE]\n\n", text + finish + "data: [DONE]\n\n" + text, text + finish + text, "data: [DONE]\n\n"} {
		if err := checkStream([]byte(body), "codex/native-one"); err == nil {
			t.Fatalf("accepted broken stream %q", body)
		}
	}
	if err := checkStream([]byte(text+finish+"data: [DONE]\n\n"), "copilot/native-one"); err == nil {
		t.Fatal("accepted another model's stream")
	}
}

func TestSmokeRejectsWrongPublishedAPIVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/":
			w.Header().Set("Location", "/docs")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case "/docs":
			_, _ = w.Write([]byte(`<script src="/docs/assets/scalar-1.0.0.js"></script>`))
		case "/openapi.json":
			_, _ = w.Write([]byte(`{"info":{"version":"0.0.0"}}`))
		default:
			_, _ = w.Write([]byte("javascript"))
		}
	}))
	defer server.Close()
	if _, err := smokeHTTP(context.Background(), server.URL, "v1.2.3"); err == nil || !strings.Contains(err.Error(), "OpenAPI release version") {
		t.Fatalf("wrong version was not rejected: %v", err)
	}
}
