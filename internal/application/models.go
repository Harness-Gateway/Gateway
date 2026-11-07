package application

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/domain"
)

const modelCacheTTL = 30 * time.Second

// ModelDiscoveryTimeout bounds a shared refresh independently of its callers.
const ModelDiscoveryTimeout = 20 * time.Second

type modelCache struct {
	mu      sync.Mutex
	models  []Model
	expires time.Time
	refresh *modelRefresh
}

type modelWaiter struct{ ctx context.Context }

type modelRefresh struct {
	ctx     context.Context
	cancel  context.CancelCauseFunc
	done    chan struct{}
	waiters map[*modelWaiter]struct{}
	models  []Model
	err     error
}

// Models caches only complete successful catalogs. Concurrent misses share one
// refresh and one CLI capacity slot. Each caller owns its cancellation; the last
// caller to leave cancels the refresh and waits for backend cleanup.
func (g *Gateway) Models(ctx context.Context) ([]Model, error) {
	for {
		g.catalog.mu.Lock()
		if err := ctx.Err(); err != nil {
			g.catalog.mu.Unlock()
			return nil, err
		}
		if time.Now().Before(g.catalog.expires) {
			models := slices.Clone(g.catalog.models)
			g.catalog.mu.Unlock()
			return models, nil
		}
		refresh := g.catalog.refresh
		if refresh != nil && len(refresh.waiters) == 0 {
			// The previous refresh is stopping. Never join cancelled work or
			// start a replacement before its CLI resources have been released.
			g.catalog.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-refresh.done:
				continue
			}
		}
		if refresh == nil {
			select {
			case g.slots <- struct{}{}:
			default:
				g.catalog.mu.Unlock()
				return nil, ErrBusy
			}
			shared, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
			refresh = &modelRefresh{ctx: shared, cancel: cancel, done: make(chan struct{}), waiters: make(map[*modelWaiter]struct{})}
			g.catalog.refresh = refresh
			go g.refreshModels(refresh)
		}
		waiter := &modelWaiter{ctx: ctx}
		refresh.waiters[waiter] = struct{}{}
		g.catalog.mu.Unlock()
		return g.awaitModels(refresh, waiter)
	}
}

func (g *Gateway) awaitModels(refresh *modelRefresh, waiter *modelWaiter) ([]Model, error) {
	defer func() {
		g.catalog.mu.Lock()
		delete(refresh.waiters, waiter)
		last := g.catalog.refresh == refresh && len(refresh.waiters) == 0
		if last {
			refresh.cancel(context.Cause(waiter.ctx))
		}
		g.catalog.mu.Unlock()
		if last {
			<-refresh.done
		}
	}()
	select {
	case <-waiter.ctx.Done():
		return nil, waiter.ctx.Err()
	case <-refresh.done:
		if err := waiter.ctx.Err(); err != nil {
			return nil, err
		}
		return slices.Clone(refresh.models), refresh.err
	}
}

func (g *Gateway) refreshModels(refresh *modelRefresh) {
	ctx, cancel := context.WithTimeout(refresh.ctx, ModelDiscoveryTimeout)
	defer cancel()
	defer refresh.cancel(nil)
	var models []Model
	err := errors.New("model discovery exited without a result")
	// This worker is outside net/http's recovery boundary. Always release
	// callers and capacity, including when a backend panics or calls Goexit.
	defer func() {
		if failure := recover(); failure != nil {
			err = fmt.Errorf("model discovery panicked: %v\n%s", failure, debug.Stack())
		}
		if contextErr := ctx.Err(); contextErr != nil {
			err = contextErr
		}
		g.catalog.mu.Lock()
		defer g.catalog.mu.Unlock()
		if err == nil {
			refresh.models = models
			for waiter := range refresh.waiters {
				if waiter.ctx.Err() == nil {
					g.catalog.models = models
					g.catalog.expires = time.Now().Add(modelCacheTTL)
					break
				}
			}
		}
		refresh.err = err
		<-g.slots
		g.catalog.refresh = nil
		close(refresh.done)
	}()
	models, err = g.discoverModels(ctx)
}

func (g *Gateway) discoverModels(ctx context.Context) ([]Model, error) {
	models := make([]Model, 0, len(g.backends))
	for _, backend := range g.backends {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		models = append(models, backend.Model)
		if catalog, ok := backend.Harness.(domain.ModelCatalog); ok {
			native, err := catalog.Models(ctx)
			if err != nil {
				return nil, fmt.Errorf("list %s models: %w", backend.Model.ID, err)
			}
			seen := map[string]bool{}
			for _, id := range native {
				if id == "" || seen[id] {
					continue
				}
				seen[id] = true
				models = append(models, Model{ID: backend.Model.ID + "/" + id, Provider: backend.Model.Provider})
			}
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
