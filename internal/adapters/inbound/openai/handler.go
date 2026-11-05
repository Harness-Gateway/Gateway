// Package openai adapts the supported OpenAI HTTP contract to the application.
package openai

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/harness-gateway/gateway/internal/application"
	"github.com/harness-gateway/gateway/internal/domain"
)

const maxRequestBytes = 1024 * 1024
const maxResponseBytes = 16 * 1024 * 1024
const responseWriteTimeout = 10 * time.Second

// ErrShuttingDown distinguishes server termination from an HTTP disconnect.
var ErrShuttingDown = errors.New("gateway is shutting down")

type Handler struct {
	gateway *application.Gateway
	timeout time.Duration
	logger  *slog.Logger
}

func New(gateway *application.Gateway, timeout time.Duration, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{gateway: gateway, timeout: timeout, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/models", h.models)
	mux.HandleFunc("POST /v1/chat/completions", h.complete)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "Unknown endpoint", "invalid_request_error", "not_found", nil)
	})
	return mux
}

func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), min(h.timeout, 20*time.Second))
	defer cancel()
	models, err := h.gateway.Models(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		h.logger.Error("model discovery failed", "error", err)
		status, code, message := http.StatusBadGateway, "model_discovery_failed", "Local model discovery failed; check the gateway log"
		switch {
		case errors.Is(err, application.ErrBusy):
			status, code, message = http.StatusTooManyRequests, "gateway_busy", "Gateway is busy; try again after the active request finishes"
		case errors.Is(err, context.DeadlineExceeded):
			status = http.StatusGatewayTimeout
		}
		writeError(w, status, message, "server_error", code, nil)
		return
	}
	response := ModelList{Object: "list", Data: []Model{}}
	for _, model := range models {
		response.Data = append(response.Data, Model{Id: model.ID, Object: "model", OwnedBy: model.Provider})
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) complete(w http.ResponseWriter, r *http.Request) {
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil || mediaType != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json", "invalid_request_error", "invalid_content_type", nil)
			return
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	var request ChatRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			writeError(w, http.StatusRequestEntityTooLarge, "Request exceeds 1 MiB", "invalid_request_error", "request_too_large", nil)
		} else {
			writeError(w, http.StatusBadRequest, "Invalid or unsupported request: "+err.Error(), "invalid_request_error", "invalid_request", nil)
		}
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "Expected a single JSON object", "invalid_request_error", "invalid_request", nil)
		return
	}
	input, field, err := validate(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "unsupported_parameter", &field)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	id := newID()
	w.Header().Set("X-Request-ID", id)
	created := time.Now().Unix()
	stream := request.Stream != nil && *request.Stream
	startedAt := time.Now()
	h.logger.Info("completion started", "request_id", id, "model", request.Model, "stream", stream)
	defer func() { h.logger.Info("completion finished", "request_id", id, "duration", time.Since(startedAt)) }()
	started := false
	wroteText := false
	size := 0
	var text strings.Builder
	chunk := func(delta Delta, finish *string, usage *Usage, choices bool) error {
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			started = true
		}
		response := Chunk{Id: id, Object: "chat.completion.chunk", Created: created, Model: request.Model, Choices: []ChunkChoice{}, Usage: usage}
		if choices {
			response.Choices = append(response.Choices, ChunkChoice{Index: 0, Delta: delta, FinishReason: finish})
		}
		return writeSSE(w, response)
	}
	emit := func(delta string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if delta == "" {
			return nil
		}
		size += len(delta)
		if size > maxResponseBytes {
			return errors.New("harness response exceeds 16 MiB")
		}
		if stream {
			wroteText = true
			return chunk(Delta{Content: &delta}, nil, nil, true)
		}
		wroteText = true
		_, err := text.WriteString(delta)
		return err
	}
	accepted := func() error {
		if stream {
			// Establish the SSE response before CLI startup or first-token latency.
			// Clients can then close the actual HTTP call while the CLI is silent.
			return chunk(Delta{Role: ptr("assistant"), Content: ptr("")}, nil, nil, true)
		}
		return nil
	}
	result, err := h.gateway.Complete(ctx, request.Model, input, emit, accepted)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && !wroteText {
		err = errors.New("harness completed without assistant text")
	}
	if err != nil {
		shuttingDown := errors.Is(context.Cause(ctx), ErrShuttingDown)
		if errors.Is(err, context.Canceled) && !shuttingDown {
			return
		}
		status, code, message := http.StatusBadGateway, "harness_error", "Local harness failed; check the gateway log and CLI login"
		switch {
		case shuttingDown:
			status, code, message = http.StatusServiceUnavailable, "gateway_shutting_down", "Gateway is shutting down; request was interrupted"
		case errors.Is(err, application.ErrUnknownModel):
			status, code, message = http.StatusNotFound, "model_not_found", "Unknown model; use GET /v1/models"
		case errors.Is(err, application.ErrBusy):
			status, code, message = http.StatusTooManyRequests, "gateway_busy", "Gateway is busy; try again after the active request finishes"
		case errors.Is(err, context.DeadlineExceeded):
			status, code, message = http.StatusGatewayTimeout, "harness_timeout", "Local harness exceeded the request timeout"
		default:
			h.logger.Error("completion failed", "request_id", id, "model", request.Model, "error", err)
		}
		if started {
			// Never send a successful finish chunk or [DONE] after an error.
			_ = writeSSE(w, ErrorResponse{Error: APIError{Message: message, Type: "server_error", Code: &code}})
		} else {
			writeError(w, status, message, "server_error", code, nil)
		}
		return
	}
	finish := result.FinishReason
	if finish == "" {
		finish = "stop"
	}
	var usage *Usage
	if result.Usage != nil {
		usage = &Usage{PromptTokens: result.Usage.InputTokens, CompletionTokens: result.Usage.OutputTokens, TotalTokens: result.Usage.TotalTokens}
	}
	if stream {
		if err := chunk(Delta{}, &finish, nil, true); err != nil {
			return
		}
		if request.StreamOptions != nil && request.StreamOptions.IncludeUsage != nil && *request.StreamOptions.IncludeUsage {
			if err := chunk(Delta{}, nil, usage, false); err != nil {
				return
			}
		}
		_ = writeSSE(w, "[DONE]")
		return
	}
	content := text.String()
	writeJSON(w, http.StatusOK, Completion{Id: id, Object: "chat.completion", Created: created, Model: request.Model, Usage: usage,
		Choices: []CompletionChoice{{Index: 0, Message: ResponseMessage{Role: "assistant", Content: &content}, FinishReason: finish}}})
}

func validate(request ChatRequest) (domain.Request, string, error) {
	var result domain.Request
	if strings.TrimSpace(request.Model) == "" {
		return result, "model", errors.New("model is required")
	}
	if len(request.Messages) == 0 {
		return result, "messages", errors.New("messages must not be empty")
	}
	if request.N != nil && *request.N != 1 {
		return result, "n", errors.New("only n=1 is supported")
	}
	if request.StreamOptions != nil && (request.Stream == nil || !*request.Stream) {
		return result, "stream_options", errors.New("stream_options requires stream=true")
	}
	if request.StreamOptions != nil && request.StreamOptions.IncludeObfuscation != nil && *request.StreamOptions.IncludeObfuscation {
		return result, "stream_options.include_obfuscation", errors.New("stream obfuscation is unsupported; omit include_obfuscation or set it to false")
	}
	if request.ReasoningEffort != nil {
		switch *request.ReasoningEffort {
		case "none", "minimal", "low", "medium", "high", "xhigh", "max":
			result.ReasoningEffort = *request.ReasoningEffort
		default:
			return result, "reasoning_effort", errors.New("unsupported reasoning_effort")
		}
	}
	sawConversation := false
	for i, message := range request.Messages {
		field := fmt.Sprintf("messages[%d]", i)
		switch message.Role {
		case "system", "developer":
			if sawConversation {
				return result, field + ".role", errors.New("system/developer instructions must precede conversation messages")
			}
		case "user", "assistant":
			sawConversation = true
		default:
			return result, field + ".role", errors.New("only system, developer, user and assistant roles are supported")
		}
		text, err := textContent(message.Content)
		if err != nil {
			return result, field + ".content", err
		}
		result.Messages = append(result.Messages, domain.Message{Role: message.Role, Text: text})
	}
	if request.Messages[len(request.Messages)-1].Role != "user" {
		return result, "messages", errors.New("the last message must have role=user")
	}
	return result, "", nil
}

func textContent(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", errors.New("message content is required")
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", err
		}
		return text, nil
	}
	var parts []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parts); err != nil || len(parts) == 0 {
		return "", errors.New("content must be a string or a nonempty array of text parts")
	}
	var text strings.Builder
	for _, part := range parts {
		if part.Type != "text" || part.Text == nil {
			return "", errors.New("only text content parts are supported")
		}
		text.WriteString(*part.Text)
	}
	return text.String(), nil
}

func ptr[T any](value T) *T { return &value }

func newID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return "chatcmpl-" + hex.EncodeToString(data[:])
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	if err := setWriteDeadline(w); err != nil {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message, kind, code string, param *string) {
	writeJSON(w, status, ErrorResponse{Error: APIError{Message: message, Type: kind, Code: &code, Param: param}})
}

func writeSSE(w http.ResponseWriter, value any) error {
	if err := setWriteDeadline(w); err != nil {
		return err
	}
	var data []byte
	if text, ok := value.(string); ok && text == "[DONE]" {
		data = []byte("[DONE]")
	} else {
		var err error
		data, err = json.Marshal(value)
		if err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	return http.NewResponseController(w).Flush()
}

func setWriteDeadline(w http.ResponseWriter) error {
	err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(responseWriteTimeout))
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}
