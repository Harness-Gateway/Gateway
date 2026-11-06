package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// This sampler exists only in the test executable. No debug HTTP endpoint or
// metrics flag is compiled into the distributed gateway.
type resourceSnapshot struct {
	Request     string `json:"request"`
	HeapAlloc   uint64 `json:"heap_alloc_bytes"`
	HeapObjects uint64 `json:"heap_objects"`
	Goroutines  int    `json:"goroutines"`
	GCs         uint32 `json:"gc_cycles"`
}

func observeResources(ctx context.Context, path string) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var previous string
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			request, err := os.ReadFile(path + ".request")
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if len(request) == 0 || string(request) == previous {
				continue
			}
			// The Java driver requests samples only after all request-owned CLIs
			// have exited. GC makes retained heap comparable between samples.
			runtime.GC()
			var stats runtime.MemStats
			runtime.ReadMemStats(&stats)
			data, err := json.Marshal(resourceSnapshot{Request: string(request), HeapAlloc: stats.HeapAlloc,
				HeapObjects: stats.HeapObjects, Goroutines: runtime.NumGoroutine(), GCs: stats.NumGC})
			if err != nil {
				return err
			}
			if err := os.WriteFile(path+".new", data, 0600); err != nil {
				return err
			}
			if err := os.Rename(path+".new", path); err != nil {
				return err
			}
			previous = string(request)
		}
	}
}

func TestResourceSamplerProducesFreshSamplesAndStops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.json")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- observeResources(ctx, path) }()
	for _, request := range []string{"first", "second"} {
		if err := os.WriteFile(path+".request", []byte(request), 0600); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			data, err := os.ReadFile(path)
			var snapshot resourceSnapshot
			if err == nil && json.Unmarshal(data, &snapshot) == nil && snapshot.Request == request {
				if snapshot.HeapAlloc == 0 || snapshot.HeapObjects == 0 || snapshot.Goroutines == 0 || snapshot.GCs == 0 {
					t.Fatalf("empty resource snapshot: %+v", snapshot)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("sampler did not publish request %s: %v", request, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("resource sampler did not stop")
	}
}
