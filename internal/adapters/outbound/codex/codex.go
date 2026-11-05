// Package codex implements the completion port using Codex's app-server protocol.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/harness-gateway/gateway/internal/adapters/outbound/prompt"
	"github.com/harness-gateway/gateway/internal/domain"
)

type Harness struct {
	Path      string
	Model     string
	Directory string
}

type message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type connection struct {
	ctx       context.Context
	input     *json.Encoder
	output    <-chan message
	readError <-chan error
	nextID    int
}

// Complete owns one ephemeral app-server process, so unrelated HTTP requests
// cannot share conversation state. Authentication and model defaults remain the CLI's.
func (h *Harness) Complete(ctx context.Context, request domain.Request, emit func(string) error) (result domain.Result, err error) {
	c, stop, err := h.connect(ctx)
	if err != nil {
		return result, err
	}
	defer stop()
	instructions, input := prompt.Build(request.Messages)
	params := map[string]any{"ephemeral": true, "approvalPolicy": "never", "sandbox": "read-only"}
	model := h.Model
	if request.Model != "" {
		model = request.Model
	}
	if model != "" {
		params["model"] = model
	}
	if h.Directory != "" {
		params["cwd"] = h.Directory
	}
	if instructions != "" {
		params["developerInstructions"] = instructions
	}
	data, err := c.call("thread/start", params, nil)
	if err != nil {
		return result, err
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(data, &started); err != nil || started.Thread.ID == "" {
		return result, errors.New("codex returned no thread id")
	}
	done := false
	var turnErr error
	deltaItems := map[string]bool{}
	handle := func(event message) error {
		switch event.Method {
		case "item/agentMessage/delta":
			var params struct {
				Delta  string `json:"delta"`
				ItemID string `json:"itemId"`
			}
			if err := json.Unmarshal(event.Params, &params); err != nil {
				return err
			}
			if params.Delta != "" {
				deltaItems[params.ItemID] = true
			}
			return emit(params.Delta)
		case "item/completed":
			var params struct {
				Item struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Text string `json:"text"`
				} `json:"item"`
			}
			if err := json.Unmarshal(event.Params, &params); err != nil {
				return err
			}
			if params.Item.Type == "agentMessage" && !deltaItems[params.Item.ID] && params.Item.Text != "" {
				return emit(params.Item.Text)
			}
		case "thread/tokenUsage/updated":
			var params struct {
				TokenUsage struct {
					Total *struct {
						Input  int `json:"inputTokens"`
						Output int `json:"outputTokens"`
						Total  int `json:"totalTokens"`
					} `json:"total"`
				} `json:"tokenUsage"`
			}
			if err := json.Unmarshal(event.Params, &params); err != nil {
				return err
			}
			u := params.TokenUsage.Total
			if u != nil {
				result.Usage = &domain.Usage{InputTokens: u.Input, OutputTokens: u.Output, TotalTokens: u.Total}
			}
		case "turn/completed":
			var params struct {
				Turn struct {
					Status string `json:"status"`
					Error  *struct {
						Message string `json:"message"`
					} `json:"error"`
				} `json:"turn"`
			}
			if err := json.Unmarshal(event.Params, &params); err != nil {
				return err
			}
			done = true
			if params.Turn.Status != "completed" {
				turnErr = fmt.Errorf("codex turn %s", params.Turn.Status)
				if params.Turn.Error != nil {
					turnErr = fmt.Errorf("%w: %s", turnErr, params.Turn.Error.Message)
				}
			}
		}
		return nil
	}
	turn := map[string]any{"threadId": started.Thread.ID, "input": []any{map[string]string{"type": "text", "text": input}}}
	if request.ReasoningEffort != "" {
		turn["effort"] = request.ReasoningEffort
	}
	if _, err := c.call("turn/start", turn, handle); err != nil {
		return result, err
	}
	for !done {
		event, err := c.receive()
		if err != nil {
			return result, err
		}
		if err := c.handle(event, handle); err != nil {
			return result, err
		}
	}
	return result, turnErr
}

func (c *connection) receive() (message, error) {
	if err := c.ctx.Err(); err != nil {
		return message{}, err
	}
	select {
	case <-c.ctx.Done():
		return message{}, c.ctx.Err()
	case event, ok := <-c.output:
		if !ok {
			if err := c.ctx.Err(); err != nil {
				return message{}, err
			}
			select {
			case err := <-c.readError:
				return message{}, err
			default:
				return message{}, io.ErrUnexpectedEOF
			}
		}
		return event, nil
	}
}

func (c *connection) call(method string, params any, handler func(message) error) (json.RawMessage, error) {
	c.nextID++
	id := fmt.Sprint(c.nextID)
	if err := c.input.Encode(map[string]any{"id": c.nextID, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		event, err := c.receive()
		if err != nil {
			return nil, err
		}
		if event.Method == "" && string(event.ID) == id {
			if event.Error != nil {
				return nil, fmt.Errorf("codex %s: %s", method, event.Error.Message)
			}
			return event.Result, nil
		}
		if err := c.handle(event, handler); err != nil {
			return nil, err
		}
	}
}

func (c *connection) handle(event message, handler func(message) error) error {
	if event.Method != "" && len(event.ID) != 0 {
		// There is no interactive approval channel in this MVP. Decline supported
		// approval requests and explicitly reject other server-initiated methods.
		response := map[string]any{"id": event.ID}
		switch event.Method {
		case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
			response["result"] = map[string]string{"decision": "decline"}
		default:
			response["error"] = map[string]any{"code": -32601, "message": "interactive requests are not supported by Harness-Gateway"}
		}
		return c.input.Encode(response)
	}
	if handler != nil {
		return handler(event)
	}
	return nil
}
