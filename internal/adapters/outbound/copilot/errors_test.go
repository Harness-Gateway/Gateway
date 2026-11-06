package copilot

import (
	"context"
	"errors"
	"testing"
	"time"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/harness-gateway/harness-gateway/internal/domain"
)

func TestStructuredCompletionErrors(t *testing.T) {
	for _, mode := range []string{"invalid-params", "provider-invalid-request", "session-error", "create-error"} {
		t.Run(mode, func(t *testing.T) {
			h, _, _ := testHarness(t, mode)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_, err := h.Complete(ctx, domain.Request{Messages: []domain.Message{{Role: "user", Text: "hello"}}}, func(string) error { return nil })
			var inputError *domain.RequestError
			want := mode == "invalid-params" || mode == "provider-invalid-request"
			if err == nil || errors.As(err, &inputError) != want {
				t.Fatalf("classification lost: %v", err)
			}
			if mode == "invalid-params" {
				var rpcError *sdk.RPCError
				if !errors.As(err, &rpcError) || rpcError.Code != -32602 {
					t.Fatalf("cause lost: %v", err)
				}
			}
		})
	}
}
