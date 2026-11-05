package copilot

import (
	"context"
	"fmt"
	"os/exec"
	"sync"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/harness-gateway/gateway/internal/buildinfo"
)

// Each operation owns its client/process. Authentication stays in the CLI.
func (h *Harness) connect(ctx context.Context) (*sdk.Client, *rpc.ServerRPC, func(), error) {
	path := h.Path
	if path == "" {
		path = "copilot"
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("find copilot: %w", err)
	}
	client := sdk.NewClient(&sdk.ClientOptions{
		Connection:       sdk.StdioConnection{Path: resolved, Args: []string{"--no-auto-update"}},
		WorkingDirectory: h.Directory, UseLoggedInUser: sdk.Bool(true), LogLevel: "error",
		ClientInfo: &sdk.ClientInfo{ApplicationName: "Harness-Gateway", ApplicationVersion: buildinfo.Version},
	})
	// ForceStop mutates the SDK's connection fields. Serialize the snapshot and
	// stop only once if cancellation races an operation's return.
	var lifecycle sync.Mutex
	stopClient := sync.OnceFunc(func() { lifecycle.Lock(); defer lifecycle.Unlock(); client.ForceStop() })
	stopOnCancel := context.AfterFunc(ctx, stopClient)
	stop := func() { stopOnCancel(); stopClient() }
	if err := client.Start(ctx); err != nil {
		stop()
		return nil, nil, nil, fmt.Errorf("start copilot: %w", err)
	}
	lifecycle.Lock()
	connection := client.RPC
	lifecycle.Unlock()
	if err := ctx.Err(); err != nil {
		stop()
		return nil, nil, nil, err
	}
	if connection == nil {
		stop()
		return nil, nil, nil, fmt.Errorf("copilot connection closed during startup")
	}
	return client, connection, stop, nil
}

func (h *Harness) Models(ctx context.Context) ([]string, error) {
	client, _, stop, err := h.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer stop()
	models, err := client.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("copilot model discovery: %w", err)
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if model.Policy != nil && model.Policy.State == "disabled" {
			continue
		}
		if model.ID != "" {
			ids = append(ids, model.ID)
		}
	}
	return ids, nil
}
