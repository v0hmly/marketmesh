package app

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
)

func TestTunnelPoolRequiresDistinctInitialPathsAndRestoresDuplicates(t *testing.T) {
	t.Parallel()

	firstID := [protocolv1.InstanceIDBytes]byte{1}
	secondID := [protocolv1.InstanceIDBytes]byte{2}
	first := &fakeManagedTunnel{ready: true, identity: firstID}
	second := &fakeManagedTunnel{ready: true, identity: firstID}
	pool, err := newTunnelPool([]managedTunnel{first, second}, true)
	if err != nil {
		t.Fatalf("newTunnelPool() error = %v", err)
	}

	pool.reconcile()
	if pool.IsReady() {
		t.Fatal("IsReady() = true for duplicate initial gateway-in path")
	}
	if second.reconnects != 1 {
		t.Fatalf("duplicate reconnects = %d, want 1", second.reconnects)
	}

	second.identity = secondID
	pool.reconcile()
	if !pool.IsReady() {
		t.Fatal("IsReady() = false after two distinct gateway-in paths")
	}
	pool.rediscover()
	if first.reconnects != 1 {
		t.Fatalf("rediscovery reconnects = %d, want 1", first.reconnects)
	}

	second.ready = false
	if !pool.IsReady() {
		t.Fatal("IsReady() = false with one surviving path after initial coverage")
	}
	first.ready = false
	if pool.IsReady() {
		t.Fatal("IsReady() = true without a surviving path")
	}
}

func TestNewTunnelPoolRejectsUnsafeCardinality(t *testing.T) {
	t.Parallel()

	if _, err := newTunnelPool(nil, true); err == nil {
		t.Fatal("newTunnelPool(nil, true) error = nil")
	}
	if _, err := newTunnelPool([]managedTunnel{nil, nil}, true); err == nil {
		t.Fatal("newTunnelPool(nil clients) error = nil")
	}
	if _, err := newTunnelPool([]managedTunnel{
		&fakeManagedTunnel{},
		&fakeManagedTunnel{},
		&fakeManagedTunnel{},
	}, true); err == nil {
		t.Fatal("newTunnelPool(three clients) error = nil")
	}
}

func TestTunnelPoolDoesNotRediscoverBeforeInitialCoverage(t *testing.T) {
	t.Parallel()

	first := &fakeManagedTunnel{}
	second := &fakeManagedTunnel{}
	pool, err := newTunnelPool([]managedTunnel{first, second}, true)
	if err != nil {
		t.Fatalf("newTunnelPool() error = %v", err)
	}

	pool.rediscover()
	if first.reconnects != 0 || second.reconnects != 0 {
		t.Fatal("rediscovery was requested before initial coverage")
	}
}

type fakeManagedTunnel struct {
	ready      bool
	identity   [protocolv1.InstanceIDBytes]byte
	reconnects int
}

func (client *fakeManagedTunnel) IsReady() bool {
	return client.ready
}

func (client *fakeManagedTunnel) ServerInstanceID() ([protocolv1.InstanceIDBytes]byte, bool) {
	return client.identity, client.ready
}

func (client *fakeManagedTunnel) RequestReconnect() {
	client.reconnects++
}

func (client *fakeManagedTunnel) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (client *fakeManagedTunnel) Shutdown(context.Context) error {
	return nil
}

func TestFixedTunnelPoolKeepsDistinctCoverageAndDuplicateRecovery(t *testing.T) {
	first := &fakeManagedTunnel{ready: true, identity: [protocolv1.InstanceIDBytes]byte{1}}
	second := &fakeManagedTunnel{ready: true, identity: first.identity}
	pool, err := newTunnelPool([]managedTunnel{first, second}, false)
	if err != nil {
		t.Fatal(err)
	}
	pool.reconcile()
	if pool.IsReady() || second.reconnects != 1 {
		t.Fatal("fixed pool accepted duplicate initial paths")
	}
	second.identity = [protocolv1.InstanceIDBytes]byte{2}
	pool.reconcile()
	if !pool.IsReady() {
		t.Fatal("fixed pool rejected distinct initial paths")
	}
	pool.rediscover()
	pool.rediscover()
	if first.reconnects != 0 || second.reconnects != 1 {
		t.Fatal("disabled rediscovery forced a reconnect")
	}
	second.identity = first.identity
	pool.reconcile()
	if second.reconnects != 2 {
		t.Fatal("duplicate recovery was disabled after initial coverage")
	}
	second.ready = false
	if !pool.IsReady() {
		t.Fatal("surviving path lost readiness")
	}
	first.ready = false
	if pool.IsReady() {
		t.Fatal("unavailable pool remained ready")
	}
}

func TestTunnelPoolPeriodicRediscoverySwitch(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		name := "fixed"
		if enabled {
			name = "periodic"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				first := &fakeManagedTunnel{ready: true, identity: [protocolv1.InstanceIDBytes]byte{1}}
				second := &fakeManagedTunnel{ready: true, identity: [protocolv1.InstanceIDBytes]byte{2}}
				pool, err := newTunnelPool([]managedTunnel{first, second}, enabled)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- pool.Component().Run(ctx) }()
				synctest.Wait()
				if !pool.IsReady() {
					t.Fatal("initial reconcile did not run")
				}
				time.Sleep(2*tunnelRediscoveryInterval + time.Millisecond)
				synctest.Wait()
				want := 0
				if enabled {
					want = 1
				}
				if first.reconnects != want || second.reconnects != want {
					t.Fatalf("reconnects=(%d,%d), want (%d,%d)", first.reconnects, second.reconnects, want, want)
				}
				cancel()
				synctest.Wait()
				if err := <-done; err != context.Canceled {
					t.Fatal(err)
				}
			})
		})
	}
}
