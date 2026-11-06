// Package copilot implements the completion port with GitHub's official Go SDK.
package copilot

import (
	"context"
	"errors"
	"fmt"
	"time"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/harness-gateway/harness-gateway/internal/adapters/outbound/prompt"
	"github.com/harness-gateway/harness-gateway/internal/domain"
)

type Harness struct {
	Path      string
	Model     string
	Directory string
}

func (h *Harness) Complete(ctx context.Context, request domain.Request, emit func(string) error) (result domain.Result, err error) {
	client, connection, stop, err := h.connect(ctx)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, stop()) }()
	instructions, input := prompt.Build(request.Messages)
	model := h.Model
	if request.Model != "" {
		model = request.Model
	}
	config := &sdk.SessionConfig{
		Model: model, WorkingDirectory: h.Directory,
		Streaming: sdk.Bool(true), AvailableTools: []string{},
		ReasoningEffort:        request.ReasoningEffort,
		SkipCustomInstructions: sdk.Bool(true),
		EnableSkills:           sdk.Bool(false), EnableFileHooks: sdk.Bool(false),
		EnableConfigDiscovery: sdk.Bool(false),
		InfiniteSessions:      &sdk.InfiniteSessionConfig{Enabled: sdk.Bool(false)},
		OnPermissionRequest: func(sdk.PermissionRequest, sdk.PermissionInvocation) (rpc.PermissionDecision, error) {
			return &rpc.PermissionDecisionReject{}, nil
		},
	}
	if instructions != "" {
		config.SystemMessage = &sdk.SystemMessageConfig{Mode: "append", Content: instructions}
	}
	session, err := client.CreateSession(ctx, config)
	if err != nil {
		return result, classifyRequestError(fmt.Errorf("create copilot session: %w", err))
	}
	defer func() {
		if ctx.Err() != nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// DeleteSession removes the SDK's registry entry without retiring its
		// event consumer. Disconnect first so ForceStop cannot lose that owner.
		// Disconnect uses a background RPC context internally; closing our owned
		// transport bounds it if a legacy/misbehaving CLI never answers.
		stopOnDeadline := context.AfterFunc(cleanup, func() { _ = stop() })
		detachErr := session.Disconnect()
		if !stopOnDeadline() {
			// Join a deadline callback already running before inspecting SDK state.
			_ = stop()
		}
		var rpcError *sdk.RPCError
		unsupported := errors.As(detachErr, &rpcError) && rpcError.Code == -32601
		if ctx.Err() != nil {
			if err == nil {
				err = ctx.Err()
			}
			return
		}
		if cleanup.Err() != nil {
			if err == nil {
				err = fmt.Errorf("detach copilot session: %w", cleanup.Err())
			}
			return
		}
		if detachErr != nil && !unsupported {
			if err == nil {
				err = fmt.Errorf("detach copilot session: %w", detachErr)
			}
			return
		}
		// Older CLIs may reject detach; the SDK still retires local resources
		// on that error. Preserve compatibility and delete their on-disk session.
		if cleanupErr := client.DeleteSession(cleanup, session.SessionID); err == nil && cleanupErr != nil {
			// Cancellation can stop the SDK after the check above but while
			// session deletion is in flight. Preserve the request's cause so
			// an intentional disconnect is not reported as an inference failure.
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = fmt.Errorf("delete copilot session: %w", cleanupErr)
			}
		}
	}()
	events := make(chan sdk.SessionEvent, 64)
	eventCtx, stopEvents := context.WithCancel(ctx)
	defer stopEvents()
	unsubscribe := session.On(func(event sdk.SessionEvent) {
		select {
		case events <- event:
		case <-eventCtx.Done():
		}
	})
	defer unsubscribe()
	if _, err := session.Send(ctx, sdk.MessageOptions{Prompt: input}); err != nil {
		return result, classifyRequestError(fmt.Errorf("send copilot request: %w", err))
	}
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	defer stopMonitor()
	connectionErrors := monitorConnection(monitorCtx, connection)
	deltaMessages := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case err := <-connectionErrors:
			return result, fmt.Errorf("copilot connection lost: %w", err)
		case event := <-events:
			if event.AgentID != nil && *event.AgentID != "" {
				continue
			}
			switch data := event.Data.(type) {
			case *sdk.AssistantMessageDeltaData:
				if data.DeltaContent != "" {
					deltaMessages[data.MessageID] = true
				}
				if err := emit(data.DeltaContent); err != nil {
					return result, err
				}
			case *sdk.AssistantMessageData:
				if !deltaMessages[data.MessageID] && data.Content != "" {
					deltaMessages[data.MessageID] = true
					if err := emit(data.Content); err != nil {
						return result, err
					}
				}
			case *sdk.AssistantUsageData:
				if data.InputTokens != nil && data.OutputTokens != nil {
					if result.Usage == nil {
						result.Usage = &domain.Usage{}
					}
					result.Usage.InputTokens += int(*data.InputTokens)
					result.Usage.OutputTokens += int(*data.OutputTokens)
					result.Usage.TotalTokens = result.Usage.InputTokens + result.Usage.OutputTokens
				}
				if data.FinishReason != nil && (*data.FinishReason == "length" || *data.FinishReason == "content_filter") {
					result.FinishReason = *data.FinishReason
				}
			case *sdk.SessionErrorData:
				err := fmt.Errorf("copilot %s: %s", data.ErrorType, data.Message)
				if data.ProviderErrorType != nil && *data.ProviderErrorType == "invalid_request_error" && data.StatusCode != nil && *data.StatusCode == 400 {
					return result, &domain.RequestError{Cause: err}
				}
				return result, err
			case *sdk.SessionIdleData:
				return result, nil
			}
		}
	}
}

func classifyRequestError(err error) error {
	var rpcError *sdk.RPCError
	if errors.As(err, &rpcError) && rpcError.Code == -32602 {
		return &domain.RequestError{Cause: err}
	}
	return err
}

// The SDK exposes no public connection-closed channel. A pending ping fails on
// transport loss, including for legacy CLIs that only implement session.send.
// Keep it separate from event consumption so backpressure cannot block the
// protocol reader. Use the request deadline, not a shorter liveness timeout.
func monitorConnection(ctx context.Context, connection *rpc.ServerRPC) <-chan error {
	errors := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := connection.Ping(ctx, nil); err != nil {
					if ctx.Err() == nil {
						errors <- err
					}
					return
				}
			}
		}
	}()
	return errors
}
