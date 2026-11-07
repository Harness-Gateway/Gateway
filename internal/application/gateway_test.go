package application

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/domain"
)

type blockingHarness struct{ started chan struct{} }

func (h blockingHarness) Complete(ctx context.Context, _ domain.Request, _ func(string) error) (domain.Result, error) {
	close(h.started)
	<-ctx.Done()
	return domain.Result{}, ctx.Err()
}

type catalogHarness struct {
	ids      []string
	err      error
	selected string
	calls    int
}

func (h *catalogHarness) Models(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return h.ids, h.err
}

func (h *catalogHarness) Complete(_ context.Context, request domain.Request, emit func(string) error) (domain.Result, error) {
	h.calls++
	h.selected = request.Model
	return domain.Result{}, emit("ok")
}

func TestAdmissionCallbackFailureAndCancellationReleaseCapacity(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelRequest), func(t *testing.T) {
			h := &catalogHarness{}
			g := New([]Backend{{Model: Model{ID: "test"}, Harness: h}}, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			expected := errors.New("client response failed")
			accepted := func() error {
				if len(g.slots) != 1 {
					t.Fatal("callback ran before capacity admission")
				}
				if cancelRequest {
					cancel()
					return nil
				}
				return expected
			}
			if cancelRequest {
				expected = context.Canceled
			}
			_, err := g.Complete(ctx, "test", domain.Request{}, nil, accepted)
			if !errors.Is(err, expected) || h.calls != 0 || len(g.slots) != 0 {
				t.Fatalf("admission failure leaked work: calls=%d slots=%d error=%v", h.calls, len(g.slots), err)
			}
			if _, err := g.Complete(context.Background(), "test", domain.Request{}, func(string) error { return nil }, nil); err != nil {
				t.Fatalf("capacity was not reusable: %v", err)
			}
		})
	}
}

func TestNativeModelsAndRouting(t *testing.T) {
	codex := &catalogHarness{ids: []string{"shared", "native", "native", ""}}
	copilot := &catalogHarness{ids: []string{"shared"}}
	g := New([]Backend{
		{Model: Model{ID: "codex", Provider: "codex"}, Harness: codex},
		{Model: Model{ID: "copilot", Provider: "copilot"}, Harness: copilot},
	}, 1)
	models, err := g.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Model{{ID: "codex", Provider: "codex"}, {ID: "codex/native", Provider: "codex"}, {ID: "codex/shared", Provider: "codex"}, {ID: "copilot", Provider: "copilot"}, {ID: "copilot/shared", Provider: "copilot"}}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("got %+v, want %+v", models, want)
	}
	for _, model := range []string{"codex/native", "copilot/shared", "codex"} {
		if _, err := g.Complete(context.Background(), model, domain.Request{}, func(string) error { return nil }, nil); err != nil {
			t.Fatal(err)
		}
		if model == "codex/native" && codex.selected != "native" {
			t.Fatal("native model was not forwarded")
		}
		if model == "copilot/shared" && copilot.selected != "shared" {
			t.Fatal("wrong provider routing")
		}
		if model == "codex" && codex.selected != "" {
			t.Fatal("alias lost its CLI default")
		}
	}
	for _, model := range []string{"missing/native", "codex/"} {
		if _, err := g.Complete(context.Background(), model, domain.Request{}, nil, nil); !errors.Is(err, ErrUnknownModel) {
			t.Fatal(err)
		}
	}
}

func TestCatalogFailuresAreExplicit(t *testing.T) {
	expected := errors.New("catalog unavailable")
	g := New([]Backend{{Model: Model{ID: "codex"}, Harness: &catalogHarness{err: expected}}}, 1)
	if _, err := g.Models(context.Background()); !errors.Is(err, expected) {
		t.Fatalf("catalog failure was hidden: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Models(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestConcurrencyAndCancellation(t *testing.T) {
	h := blockingHarness{make(chan struct{})}
	g := New([]Backend{{Model: Model{ID: "test"}, Harness: h}}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := g.Complete(ctx, "test", domain.Request{}, nil, nil); done <- err }()
	<-h.started
	if _, err := g.Complete(ctx, "test", domain.Request{}, nil, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("expected busy: %v", err)
	}
	if _, err := g.Complete(ctx, "missing", domain.Request{}, nil, nil); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("expected unknown model: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach harness")
	}
	if len(g.slots) != 0 {
		t.Fatal("concurrency slot leaked")
	}
}

type activeOperationHarness struct {
	active  string
	started chan struct{}
}

func (h *activeOperationHarness) Models(ctx context.Context) ([]string, error) {
	if h.active == "models" {
		close(h.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return []string{"native"}, nil
}

func (h *activeOperationHarness) Complete(ctx context.Context, _ domain.Request, emit func(string) error) (domain.Result, error) {
	if h.active == "completion" {
		close(h.started)
		<-ctx.Done()
		return domain.Result{}, ctx.Err()
	}
	return domain.Result{}, emit("ok")
}

func TestModelDiscoverySharesConcurrencyLimit(t *testing.T) {
	for _, active := range []string{"models", "completion"} {
		t.Run(active, func(t *testing.T) {
			h := &activeOperationHarness{active: active, started: make(chan struct{})}
			g := New([]Backend{{Model: Model{ID: "test"}, Harness: h}}, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if active == "models" {
					_, err := g.Models(ctx)
					done <- err
				} else {
					_, err := g.Complete(ctx, "test", domain.Request{}, nil, nil)
					done <- err
				}
			}()
			<-h.started
			if active == "completion" {
				if _, err := g.Models(ctx); !errors.Is(err, ErrBusy) {
					t.Fatalf("catalog escaped the process limit: %v", err)
				}
			}
			if _, err := g.Complete(ctx, "test", domain.Request{}, nil, nil); !errors.Is(err, ErrBusy) {
				t.Fatalf("completion escaped the process limit: %v", err)
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled operation did not stop")
			}
			if len(g.slots) != 0 {
				t.Fatal("concurrency slot leaked")
			}
			if active == "models" {
				if _, err := g.Complete(context.Background(), "test", domain.Request{}, func(string) error { return nil }, nil); err != nil {
					t.Fatal(err)
				}
			} else if _, err := g.Models(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
