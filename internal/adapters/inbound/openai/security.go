package openai

import (
	"net"
	"net/http"
	"strconv"
	"strings"
)

// Loopback binding alone does not prevent browser-origin or DNS-rebinding
// requests. Keep command-line clients unauthenticated and Scalar same-origin.
func protectLocalRequests(next http.Handler) http.Handler {
	deny := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusForbidden, "Only local, same-origin requests are allowed", "invalid_request_error", "forbidden_origin", nil)
	})
	protection := http.NewCrossOriginProtection()
	protection.SetDenyHandler(deny)
	protected := protection.Handler(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			deny.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/v1/models" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			// Discovery can start CLI processes. Apply the standard library's
			// unsafe-method origin checks to these resource-consuming reads,
			// using a copy so routing and HEAD semantics remain unchanged.
			check := *r
			check.Method = http.MethodPost
			if err := protection.Check(&check); err != nil {
				deny.ServeHTTP(w, r)
				return
			}
		}
		protected.ServeHTTP(w, r)
	})
}

func loopbackHost(host string) bool {
	if name, port, err := net.SplitHostPort(host); err == nil {
		n, err := strconv.Atoi(port)
		if err != nil || n < 0 || n > 65535 {
			return false
		}
		host = name
	} else if strings.Contains(host, ":") {
		// IPv6 Host headers without a port still require square brackets.
		if !strings.HasPrefix(host, "[") || !strings.HasSuffix(host, "]") {
			return false
		}
		host = host[1 : len(host)-1]
	}
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}
