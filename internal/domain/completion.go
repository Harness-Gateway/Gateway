// Package domain contains the provider-independent completion port and values.
package domain

import "context"

type Message struct {
	Role string
	Text string
}

type Request struct {
	// Model is a native CLI model ID. Empty keeps the adapter's configured default.
	Model           string
	Messages        []Message
	ReasoningEffort string
}

type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

type Result struct {
	Usage        *Usage
	FinishReason string
}

// Complete calls emit synchronously and in order. Returning an error from emit
// must stop the operation. Implementations must release their request resources
// and stop inference when ctx is cancelled.
type Harness interface {
	Complete(ctx context.Context, request Request, emit func(string) error) (Result, error)
}

// ModelCatalog is an optional discovery port for harnesses with a documented
// native model-list interface. Catalog entries are not an entitlement guarantee.
type ModelCatalog interface {
	Models(context.Context) ([]string, error)
}
