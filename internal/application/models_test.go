package application

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/domain"
)

type modelSource func(context.Context) ([]string, error)

func (s modelSource) Models(ctx context.Context) ([]string, error) { return s(ctx) }
func (s modelSource) Complete(context.Context, domain.Request, func(string) error) (domain.Result, error) {
	return domain.Result{}, nil
}

func modelGateway(source modelSource, concurrency int) *Gateway {
	return New([]Backend{{Model: Model{ID: "test", Provider: "test"}, Harness: source}}, concurrency)
}

type modelsResult struct {
	models []Model
	err    error
}

func asyncModels(g *Gateway, ctx context.Context) <-chan modelsResult {
	done := make(chan modelsResult, 1)
	go func() {
		models, err := g.Models(ctx)
		done <- modelsResult{models, err}
	}()
	return done
}

func TestModelCacheExpiryAndCopies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		g := modelGateway(func(context.Context) ([]string, error) {
			calls.Add(1)
			return []string{"native", "native", ""}, nil
		}, 1)
		want := []Model{{ID: "test", Provider: "test"}, {ID: "test/native", Provider: "test"}}
		for range 3 {
			models, err := g.Models(t.Context())
			if err != nil || !reflect.DeepEqual(models, want) {
				t.Fatalf("bad cached catalog: %v %v", models, err)
			}
			models[0].ID = "caller mutation"
		}
		if calls.Load() != 1 {
			t.Fatal("cache hits restarted discovery")
		}
		time.Sleep(modelCacheTTL - time.Nanosecond)
		if _, err := g.Models(t.Context()); err != nil || calls.Load() != 1 {
			t.Fatal("cache expired prematurely")
		}
		time.Sleep(time.Nanosecond)
		if _, err := g.Models(t.Context()); err != nil || calls.Load() != 2 {
			t.Fatal("expired catalog was not refreshed")
		}
		other := modelGateway(func(context.Context) ([]string, error) { return []string{"other"}, nil }, 1)
		models, err := other.Models(t.Context())
		if err != nil || models[1].ID != "test/other" {
			t.Fatal("independent gateways shared their catalogs")
		}
	})
}

func TestModelCacheTTLStartsAfterDiscovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		g := modelGateway(func(context.Context) ([]string, error) {
			calls.Add(1)
			time.Sleep(3 * time.Second)
			return []string{"native"}, nil
		}, 1)
		if _, err := g.Models(t.Context()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(modelCacheTTL - time.Second)
		if _, err := g.Models(t.Context()); err != nil || calls.Load() != 1 {
			t.Fatal("refresh time was deducted from cache lifetime")
		}
	})
}

func TestModelCacheCoalescesConcurrentMisses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		release := make(chan struct{})
		g := modelGateway(func(context.Context) ([]string, error) {
			calls.Add(1)
			<-release
			return []string{"native"}, nil
		}, 1)
		var waiting []<-chan modelsResult
		for range 64 {
			waiting = append(waiting, asyncModels(g, t.Context()))
		}
		synctest.Wait()
		if calls.Load() != 1 || len(g.slots) != 1 {
			t.Fatalf("concurrent discovery bypassed the process limit: calls=%d slots=%d", calls.Load(), len(g.slots))
		}
		if _, err := g.Complete(t.Context(), "test", domain.Request{}, nil, nil); !errors.Is(err, ErrBusy) {
			t.Fatalf("refresh did not reserve capacity: %v", err)
		}
		close(release)
		for _, result := range waiting {
			r := <-result
			if r.err != nil || len(r.models) != 2 || r.models[1].ID != "test/native" {
				t.Fatalf("coalesced caller failed: %+v", r)
			}
			r.models[1].ID = "caller mutation"
		}
		if len(g.slots) != 0 {
			t.Fatal("discovery capacity leaked")
		}
	})
}

func TestModelRefreshCancellationIsPerCaller(t *testing.T) {
	for _, cancelLeader := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			var calls atomic.Int32
			release := make(chan struct{})
			g := modelGateway(func(ctx context.Context) ([]string, error) {
				calls.Add(1)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
					return []string{"native"}, nil
				}
			}, 1)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			firstCtx, secondCtx := t.Context(), ctx
			if cancelLeader {
				firstCtx, secondCtx = ctx, t.Context()
			}
			first := asyncModels(g, firstCtx)
			synctest.Wait()
			second := asyncModels(g, secondCtx)
			synctest.Wait()
			cancel()
			synctest.Wait()
			cancelled, survivor := second, first
			if cancelLeader {
				cancelled, survivor = first, second
			}
			if r := <-cancelled; !errors.Is(r.err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", r.err)
			}
			select {
			case r := <-survivor:
				t.Fatalf("one caller cancelled another: %+v", r)
			default:
			}
			close(release)
			if r := <-survivor; r.err != nil || calls.Load() != 1 {
				t.Fatalf("surviving caller restarted or failed discovery: %+v calls=%d", r, calls.Load())
			}
		})
	}
}

func TestAbandonedRefreshStopsBeforeReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		cleanup := make(chan struct{})
		cause := make(chan error, 1)
		g := modelGateway(func(ctx context.Context) ([]string, error) {
			if calls.Add(1) == 1 {
				<-ctx.Done()
				cause <- context.Cause(ctx)
				<-cleanup
				return []string{"stale"}, nil // A late success must not populate the cache.
			}
			return []string{"fresh"}, nil
		}, 1)
		ctx, cancel := context.WithCancelCause(t.Context())
		first := asyncModels(g, ctx)
		synctest.Wait()
		shutdown := errors.New("shutdown")
		cancel(shutdown)
		synctest.Wait()
		if err := <-cause; !errors.Is(err, shutdown) {
			t.Fatalf("shared refresh lost cancellation cause: %v", err)
		}
		select {
		case <-first:
			t.Fatal("last caller returned before CLI cleanup")
		default:
		}
		next := asyncModels(g, t.Context())
		synctest.Wait()
		if calls.Load() != 1 || len(g.slots) != 1 {
			t.Fatal("replacement overlapped a retiring refresh")
		}
		close(cleanup)
		if r := <-first; !errors.Is(r.err, context.Canceled) {
			t.Fatalf("caller cancellation lost: %+v", r)
		}
		if r := <-next; r.err != nil || r.models[1].ID != "test/fresh" || calls.Load() != 2 {
			t.Fatalf("replacement inherited cancelled or stale work: %+v", r)
		}
	})
}

func TestModelRefreshDoesNotCacheErrorsOrServeExpiredResults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		failure := errors.New("catalog failure")
		g := modelGateway(func(context.Context) ([]string, error) {
			n := calls.Add(1)
			if n == 2 || n == 3 {
				return nil, failure
			}
			return []string{"native"}, nil
		}, 1)
		if _, err := g.Models(t.Context()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(modelCacheTTL)
		for range 2 {
			if models, err := g.Models(t.Context()); !errors.Is(err, failure) || len(models) != 0 {
				t.Fatalf("failed refresh returned stale data or hid the error: %v %v", models, err)
			}
		}
		for range 2 {
			if models, err := g.Models(t.Context()); err != nil || len(models) != 2 || calls.Load() != 4 {
				t.Fatalf("failure was cached or recovery not cached: %v %v calls=%d", models, err, calls.Load())
			}
		}
	})
}

func TestModelRefreshHasIndependentDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := modelGateway(func(ctx context.Context) ([]string, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, 1)
		start := time.Now()
		if _, err := g.Models(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unbounded or incorrect shared deadline: %v", err)
		}
		if time.Since(start) != ModelDiscoveryTimeout || len(g.slots) != 0 {
			t.Fatal("refresh exceeded its deadline or retained capacity")
		}
	})
}

func TestModelWaiterDeadlineDoesNotStopOtherCallers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		g := modelGateway(func(ctx context.Context) ([]string, error) {
			select {
			case <-release:
				return []string{"native"}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}, 1)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		first := asyncModels(g, ctx)
		synctest.Wait()
		second := asyncModels(g, t.Context())
		synctest.Wait()
		time.Sleep(6 * time.Second)
		if r := <-first; !errors.Is(r.err, context.DeadlineExceeded) {
			t.Fatalf("individual deadline lost: %+v", r)
		}
		close(release)
		if r := <-second; r.err != nil {
			t.Fatalf("individual deadline stopped another caller: %+v", r)
		}
	})
}

func TestCancelledLateSuccessIsNotCached(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		var calls atomic.Int32
		g := modelGateway(func(context.Context) ([]string, error) {
			calls.Add(1)
			cancel()
			return []string{"late"}, nil
		}, 1)
		if _, err := g.Models(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("late success lost cancellation: %v", err)
		}
		if _, err := g.Models(t.Context()); err != nil || calls.Load() != 2 {
			t.Fatalf("cancelled caller populated cache: %v calls=%d", err, calls.Load())
		}
	})
}

func TestModelCacheHitsDoNotNeedProcessCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := modelGateway(func(context.Context) ([]string, error) { return []string{"native"}, nil }, 1)
		g.slots <- struct{}{} // An active completion owns all CLI capacity.
		if _, err := g.Models(t.Context()); !errors.Is(err, ErrBusy) {
			t.Fatalf("cold refresh bypassed the process limit: %v", err)
		}
		<-g.slots
		if _, err := g.Models(t.Context()); err != nil {
			t.Fatal(err)
		}
		g.slots <- struct{}{}
		defer func() { <-g.slots }()
		if _, err := g.Models(t.Context()); err != nil {
			t.Fatalf("warm cache needed CLI capacity: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := g.Models(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("warm cache ignored cancellation: %v", err)
		}
	})
}

func TestModelRefreshPanicReleasesWaitersAndCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		release := make(chan struct{})
		g := modelGateway(func(context.Context) ([]string, error) {
			if calls.Add(1) == 1 {
				<-release
				panic("fixture catalog panic")
			}
			return []string{"recovered"}, nil
		}, 1)
		first := asyncModels(g, t.Context())
		second := asyncModels(g, t.Context())
		synctest.Wait()
		close(release)
		for _, result := range []<-chan modelsResult{first, second} {
			if r := <-result; r.err == nil || !strings.Contains(r.err.Error(), "fixture catalog panic") || len(r.models) != 0 {
				t.Fatalf("panic did not fail all shared callers: %+v", r)
			}
		}
		if len(g.slots) != 0 {
			t.Fatal("panicking discovery retained capacity")
		}
		if models, err := g.Models(t.Context()); err != nil || models[1].ID != "test/recovered" || calls.Load() != 2 {
			t.Fatalf("panic was cached or prevented recovery: %v %v", models, err)
		}
	})
}

func TestModelRefreshGoexitReleasesCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		g := modelGateway(func(context.Context) ([]string, error) {
			if calls.Add(1) == 1 {
				runtime.Goexit()
			}
			return []string{"recovered"}, nil
		}, 1)
		if _, err := g.Models(t.Context()); err == nil || !strings.Contains(err.Error(), "without a result") {
			t.Fatalf("abnormal worker exit did not release its caller: %v", err)
		}
		if _, err := g.Models(t.Context()); err != nil || len(g.slots) != 0 || calls.Load() != 2 {
			t.Fatalf("abnormal worker exit prevented recovery: %v", err)
		}
	})
}

func TestAllModelWaitersCancelAndRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		g := modelGateway(func(ctx context.Context) ([]string, error) {
			if calls.Add(1) == 1 {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return []string{"recovered"}, nil
		}, 1)
		ctx, cancel := context.WithCancel(t.Context())
		var waiting []<-chan modelsResult
		for range 64 {
			waiting = append(waiting, asyncModels(g, ctx))
		}
		synctest.Wait()
		cancel()
		for _, result := range waiting {
			if r := <-result; !errors.Is(r.err, context.Canceled) {
				t.Fatalf("shared cancellation was lost: %+v", r)
			}
		}
		if len(g.slots) != 0 || calls.Load() != 1 {
			t.Fatal("abandoned burst retained capacity or started duplicate work")
		}
		if models, err := g.Models(t.Context()); err != nil || models[1].ID != "test/recovered" || calls.Load() != 2 {
			t.Fatalf("abandoned burst prevented recovery: %v %v", models, err)
		}
	})
}

func TestPartialModelCatalogIsNotCached(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		var fail atomic.Bool
		fail.Store(true)
		failure := errors.New("provider failed")
		g := New([]Backend{
			{Model: Model{ID: "a"}, Harness: modelSource(func(context.Context) ([]string, error) { return []string{"one"}, nil })},
			{Model: Model{ID: "b"}, Harness: modelSource(func(context.Context) ([]string, error) {
				calls.Add(1)
				if fail.Load() {
					return nil, failure
				}
				return []string{"two"}, nil
			})},
		}, 1)
		if models, err := g.Models(t.Context()); !errors.Is(err, failure) || len(models) != 0 {
			t.Fatalf("partial discovery was returned: %v %v", models, err)
		}
		fail.Store(false)
		for range 2 {
			if models, err := g.Models(t.Context()); err != nil || len(models) != 4 || calls.Load() != 2 {
				t.Fatalf("partial catalog was cached or recovery not cached: %v %v", models, err)
			}
		}
	})
}
