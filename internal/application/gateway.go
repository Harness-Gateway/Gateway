// Package application routes completions without depending on HTTP or a CLI SDK.
package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/harness-gateway/gateway/internal/domain"
)

var ErrUnknownModel = errors.New("unknown model")
var ErrBusy = errors.New("gateway is at its concurrency limit")

type Model struct {
	ID       string
	Provider string
}

type Backend struct {
	Model   Model
	Harness domain.Harness
}

type Gateway struct {
	backends map[string]Backend
	slots    chan struct{}
}

func New(backends []Backend, concurrency int) *Gateway {
	if concurrency < 1 {
		panic("concurrency must be positive")
	}
	g := &Gateway{backends: make(map[string]Backend), slots: make(chan struct{}, concurrency)}
	for _, backend := range backends {
		if backend.Model.ID == "" || backend.Harness == nil {
			panic("invalid backend")
		}
		if _, exists := g.backends[backend.Model.ID]; exists {
			panic("duplicate model")
		}
		g.backends[backend.Model.ID] = backend
	}
	return g
}

func (g *Gateway) Models(ctx context.Context) ([]Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		return nil, ErrBusy
	}
	models := make([]Model, 0, len(g.backends))
	for _, backend := range g.backends {
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

// Complete routes an admitted request to its backend. accepted runs after routing
// and capacity admission, allowing a transport to establish its response before
// backend startup without admitting rejected work.
func (g *Gateway) Complete(ctx context.Context, model string, request domain.Request, emit func(string) error, accepted func() error) (domain.Result, error) {
	alias, native, explicit := strings.Cut(model, "/")
	backend, ok := g.backends[alias]
	if explicit && native == "" {
		ok = false
	}
	if !ok {
		return domain.Result{}, ErrUnknownModel
	}
	if err := ctx.Err(); err != nil {
		return domain.Result{}, err
	}
	if explicit {
		request.Model = native
	}
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		return domain.Result{}, ErrBusy
	}
	if accepted != nil {
		if err := accepted(); err != nil {
			return domain.Result{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return domain.Result{}, err
	}
	return backend.Harness.Complete(ctx, request, emit)
}
