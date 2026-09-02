package app

import (
	"context"
	"testing"

	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
)

func TestTunnelPoolRequiresDistinctInitialPathsAndRestoresDuplicates(t *testing.T) {
	t.Parallel()

	firstID := [protocolv1.InstanceIDBytes]byte{1}
	secondID := [protocolv1.InstanceIDBytes]byte{2}
	thirdID := [protocolv1.InstanceIDBytes]byte{3}
	first := &fakeManagedTunnel{ready: true, identity: firstID}
	second := &fakeManagedTunnel{ready: true, identity: secondID}
	discovery := &fakeManagedTunnel{ready: true, identity: secondID}
	pool, err := newTunnelPool([]managedTunnel{first, second, discovery})
	if err != nil {
		t.Fatalf("newTunnelPool() error = %v", err)
	}

	pool.reconcile()
	if !pool.IsReady() {
		t.Fatal("IsReady() = false with two distinct initial gateway-in paths")
	}
	if discovery.reconnects != 1 {
		t.Fatalf("discovery reconnects = %d, want 1", discovery.reconnects)
	}

	discovery.identity = thirdID
	pool.reconcile()
	if discovery.reconnects != 1 {
		t.Fatalf("discovery reconnects = %d after finding surge path, want 1", discovery.reconnects)
	}

	second.ready = false
	if !pool.IsReady() {
		t.Fatal("IsReady() = false with one surviving path after initial coverage")
	}
	discovery.ready = false
	first.ready = false
	if pool.IsReady() {
		t.Fatal("IsReady() = true without a surviving path")
	}
}

func TestNewTunnelPoolRejectsUnsafeCardinality(t *testing.T) {
	t.Parallel()

	if _, err := newTunnelPool(nil); err == nil {
		t.Fatal("newTunnelPool(nil) error = nil")
	}
	if _, err := newTunnelPool([]managedTunnel{nil, nil, nil}); err == nil {
		t.Fatal("newTunnelPool(nil clients) error = nil")
	}
	if _, err := newTunnelPool([]managedTunnel{
		&fakeManagedTunnel{},
		&fakeManagedTunnel{},
	}); err == nil {
		t.Fatal("newTunnelPool(two clients) error = nil")
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
