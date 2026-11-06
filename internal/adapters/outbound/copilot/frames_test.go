package copilot

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestGuardedFrames(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		want        string
	}{
		{"valid", "Content-Length: 2\r\n\r\n{}", "Content-Length: 2\r\n\r\n{}"},
		{"multiple", "Content-Length: 2\r\n\r\n{}Content-Length: 2\r\n\r\n[]", "Content-Length: 2\r\n\r\n{}Content-Length: 2\r\n\r\n[]"},
		{"header normalization", "content-length: 2\nContent-Type: application/json\n\n{}", "Content-Length: 2\r\n\r\n{}"},
		{"too large", fmt.Sprintf("Content-Length: %d\r\n\r\n", maxProtocolFrameBytes+1), ""},
		{"overflow", "Content-Length: 9999999999999999999999\r\n\r\n", ""},
		{"signed", "Content-Length: +2\r\n\r\n{}", ""},
		{"zero", "Content-Length: 0\r\n\r\n", ""},
		{"negative", "Content-Length: -2\r\n\r\n{}", ""},
		{"duplicate", "Content-Length: 2\r\nContent-Length: 3\r\n\r\n{}", ""},
		{"missing", "Content-Type: application/json\r\n\r\n{}", ""},
		{"malformed", "not a header\r\n\r\n{}", ""},
		{"large line", strings.Repeat("x", maxProtocolHeaderBytes+1), ""},
		{"large aggregate headers", strings.Repeat("X-Extra: value\r\n", 1024) + "Content-Length: 2\r\n\r\n{}", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			err := copyFrames(&output, bufio.NewReaderSize(strings.NewReader(tc.frame), maxProtocolHeaderBytes))
			if output.String() != tc.want {
				t.Fatalf("unexpected forwarded bytes: %d", output.Len())
			}
			if tc.want == "" && (err == nil || errors.Is(err, io.EOF)) {
				t.Fatalf("invalid header accepted: %v", err)
			}
			if tc.want != "" && !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
		})
	}
}

func TestFrameBoundaryAndTruncatedBody(t *testing.T) {
	for _, length := range []int{maxProtocolFrameBytes, maxProtocolFrameBytes + 1} {
		reader := bufio.NewReader(strings.NewReader(fmt.Sprintf("Content-Length: %d\r\n\r\n", length)))
		actual, err := frameLength(reader, maxProtocolFrameBytes)
		if length == maxProtocolFrameBytes && (actual != length || err != nil) {
			t.Fatalf("boundary rejected: %d %v", actual, err)
		}
		if length > maxProtocolFrameBytes && err == nil {
			t.Fatal("oversized frame accepted")
		}
	}
	var output bytes.Buffer
	err := copyFrames(&output, bufio.NewReader(strings.NewReader("Content-Length: 5\r\n\r\n{}")))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncation not detected: %v", err)
	}
}

func TestBridgeAuthentication(t *testing.T) {
	for _, body := range []string{
		`{"id":7,"jsonrpc":"2.0","method":"connect","params":{"token":"private-token","extra":"keep"}}`,
		`{"method":"connect","params":{"token":"wrong-token"}}`,
		`{"method":"ping","params":{"token":"private-token"}}`,
		`{"method":"connect","params":{}}`, `invalid-json`,
	} {
		frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
		result, err := authenticate(bufio.NewReader(strings.NewReader(frame)), "private-token")
		if strings.Contains(body, `"extra":"keep"`) {
			if err != nil || bytes.Contains(result, []byte("private-token")) {
				t.Fatalf("valid SDK handshake rejected: %v", err)
			}
			var forwarded struct {
				ID              int
				JSONRPC, Method string
				Params          map[string]string
			}
			if err := json.Unmarshal(result, &forwarded); err != nil || forwarded.ID != 7 || forwarded.JSONRPC != "2.0" || forwarded.Method != "connect" || forwarded.Params["extra"] != "keep" {
				t.Fatalf("bridge changed SDK handshake fields: %s", result)
			}
			if _, exists := forwarded.Params["token"]; exists {
				t.Fatal("bridge token forwarded to CLI")
			}
		} else if err == nil {
			t.Fatalf("unauthorized handshake accepted: %s", body)
		}
	}
}
