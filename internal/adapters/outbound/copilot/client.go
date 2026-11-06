package copilot

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/harness-gateway/harness-gateway/internal/buildinfo"
)

// Each operation owns its client/process. Authentication stays in the CLI.
func (h *Harness) connect(ctx context.Context) (*sdk.Client, *rpc.ServerRPC, func() error, error) {
	path := h.Path
	if path == "" {
		path = "copilot"
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("find copilot: %w", err)
	}
	bridge, err := startBridge(ctx, resolved, h.Directory)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("start copilot: %w", err)
	}
	client := sdk.NewClient(&sdk.ClientOptions{
		Connection: sdk.URIConnection{URL: bridge.listener.Addr().String(), ConnectionToken: bridge.token},
		ClientInfo: &sdk.ClientInfo{ApplicationName: "Harness-Gateway", ApplicationVersion: buildinfo.Version},
	})
	// ForceStop mutates the SDK's connection fields. Serialize the snapshot and
	// stop only once if cancellation races an operation's return.
	var lifecycle sync.Mutex
	stopClient := sync.OnceValue(func() error {
		lifecycle.Lock()
		defer lifecycle.Unlock()
		// Close the bridge first to unblock SDK startup and in-flight writes.
		closeErr := bridge.Close()
		client.ForceStop()
		return errors.Join(bridge.Err(), closeErr)
	})
	stopOnCancel := context.AfterFunc(ctx, func() { _ = stopClient() })
	stop := func() error { stopOnCancel(); return stopClient() }
	if err := client.Start(ctx); err != nil {
		return nil, nil, nil, fmt.Errorf("start copilot: %w", errors.Join(err, stop()))
	}
	lifecycle.Lock()
	connection := client.RPC
	lifecycle.Unlock()
	if err := ctx.Err(); err != nil {
		_ = stop()
		return nil, nil, nil, err
	}
	if connection == nil {
		return nil, nil, nil, errors.Join(fmt.Errorf("copilot connection closed during startup"), stop())
	}
	return client, connection, stop, nil
}

func (h *Harness) Models(ctx context.Context) (ids []string, err error) {
	client, _, stop, err := h.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, stop()) }()
	models, err := client.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("copilot model discovery: %w", err)
	}
	ids = make([]string, 0, len(models))
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
