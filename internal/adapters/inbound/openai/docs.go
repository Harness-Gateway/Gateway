package openai

import (
	"bytes"
	_ "embed"
	"net/http"
	"time"

	"github.com/harness-gateway/harness-gateway/api"
)

//go:embed docs/index.html
var docsHTML []byte

//go:embed docs/init.js
var docsInit []byte

//go:embed docs/assets/scalar-1.70.0.js
var scalarJS []byte

//go:embed docs/assets/LICENSE.scalar
var scalarLicense []byte

func registerDocs(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if err := setWriteDeadline(w); err != nil {
			return
		}
		http.Redirect(w, r, "/docs", http.StatusTemporaryRedirect)
	})
	for path, asset := range map[string]struct {
		body        []byte
		contentType string
		immutable   bool
	}{
		"/docs":                         {docsHTML, "text/html; charset=utf-8", false},
		"/docs/":                        {docsHTML, "text/html; charset=utf-8", false},
		"/docs/init.js":                 {docsInit, "text/javascript; charset=utf-8", false},
		"/docs/assets/scalar-1.70.0.js": {scalarJS, "text/javascript; charset=utf-8", true},
		"/docs/assets/LICENSE.scalar":   {scalarLicense, "text/plain; charset=utf-8", false},
		"/openapi.json":                 {api.OpenAPI, "application/json", false},
	} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			// /docs/ is an exact alias, not a directory listing or catch-all.
			if r.URL.Path != path {
				writeError(w, http.StatusNotFound, "Unknown endpoint", "invalid_request_error", "not_found", nil)
				return
			}
			if err := setWriteDeadline(w); err != nil {
				return
			}
			w.Header().Set("Content-Type", asset.contentType)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "no-cache")
			if asset.immutable {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			if asset.contentType == "text/html; charset=utf-8" {
				w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; worker-src 'self' blob:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
			}
			http.ServeContent(w, r, path, time.Time{}, bytes.NewReader(asset.body))
		})
	}
}
