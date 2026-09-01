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
	first := &fakeManagedTunnel{ready: true, identity: firstID}
	second := &fakeManagedTunnel{ready: true, identity: firstID}
	pool, err := newTunnelPool([]managedTunnel{first, second})
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

	if _, err := newTunnelPool(nil); err == nil {
		t.Fatal("newTunnelPool(nil) error = nil")
	}
	if _, err := newTunnelPool([]managedTunnel{nil, nil}); err == nil {
		t.Fatal("newTunnelPool(nil clients) error = nil")
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
