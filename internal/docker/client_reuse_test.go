package docker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/docker/go-sdk/client"
)

// stubClient satisfies client.SDKClient by embedding the interface: only the
// methods a test actually calls need real bodies, and these tests never call one.
type stubClient struct {
	client.SDKClient
}

// Every method used to build a fresh SDK client, and client.New is not a
// constructor so much as a round trip: it loads the docker context and
// health-checks the daemon before returning (go-sdk/client/client.go:70-90). A
// doctor run pays that once now instead of once per probe; the reuse is the whole
// point, and a Runner that quietly went back to per-call construction would be
// 4ms per API call slower without any test noticing. So this pins the count.
func TestClientIsBuiltOnceAndReused(t *testing.T) {
	var built atomic.Int64
	r := &Runner{newClient: func(context.Context) (client.SDKClient, error) {
		built.Add(1)
		return stubClient{}, nil
	}}
	for i := 0; i < 5; i++ {
		if _, err := r.client(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := built.Load(); got != 1 {
		t.Errorf("client built %d times across 5 calls, want 1", got)
	}
}

// Concurrent callers must not each build one: doctor fans its probes out, and the
// first version of that fan-out would have constructed a client per goroutine
// (each doing its own daemon health check) instead of one.
func TestClientIsBuiltOnceUnderConcurrency(t *testing.T) {
	var built atomic.Int64
	r := &Runner{newClient: func(context.Context) (client.SDKClient, error) {
		built.Add(1)
		return stubClient{}, nil
	}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.client(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := built.Load(); got != 1 {
		t.Errorf("client built %d times by 8 concurrent callers, want 1", got)
	}
}

// A failed construction must NOT be remembered. A Runner outlives a single call,
// and caching "no daemon" would make a daemon that starts a second later
// permanently unreachable for the rest of the process — a long foreground `serve`
// would hold that error for hours.
func TestClientFailureIsNotCached(t *testing.T) {
	var attempts atomic.Int64
	r := &Runner{newClient: func(context.Context) (client.SDKClient, error) {
		attempts.Add(1)
		return nil, errors.New("no daemon yet")
	}}
	for i := 0; i < 3; i++ {
		if _, err := r.client(context.Background()); err == nil {
			t.Fatal("expected the error to surface")
		}
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("construction attempted %d times, want a retry each call", got)
	}
	// And once the daemon answers, the very next call succeeds through the same Runner.
	r.newClient = func(context.Context) (client.SDKClient, error) { return stubClient{}, nil }
	if _, err := r.client(context.Background()); err != nil {
		t.Fatalf("a Runner that saw failures must still connect: %v", err)
	}
}
