package app

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

const (
	tunnelPoolSize     = 3
	tunnelReadyPaths   = 2
	tunnelPoolInterval = 250 * time.Millisecond
)

type managedTunnel interface {
	IsReady() bool
	ServerInstanceID() ([protocolv1.InstanceIDBytes]byte, bool)
	RequestReconnect()
	Run(context.Context) error
	Shutdown(context.Context) error
}

// tunnelPool keeps two sessions on distinct gateway-in processes and one
// bounded discovery session. The discovery session reconnects while it is a
// duplicate, allowing a maxSurge gateway-in Pod to receive a tunnel before an
// old Pod is stopped. Readiness remains fail-closed until two paths are seen.
type tunnelPool struct {
	clients      []managedTunnel
	initialReady atomic.Bool
}

func newTunnelPool(clients []managedTunnel) (*tunnelPool, error) {
	if len(clients) != tunnelPoolSize {
		return nil, errors.New("gateway-out: tunnel pool must contain three clients")
	}
	for _, client := range clients {
		if client == nil {
			return nil, errors.New("gateway-out: tunnel pool client must not be nil")
		}
	}

	return &tunnelPool{clients: append([]managedTunnel(nil), clients...)}, nil
}

func (pool *tunnelPool) Component() serviceruntime.Component {
	return serviceruntime.Component{
		Name: "reverse-tunnels",
		Run: func(ctx context.Context) error {
			results := make(chan error, len(pool.clients))
			for _, client := range pool.clients {
				go func() { results <- client.Run(ctx) }()
			}
			pool.reconcile()
			ticker := time.NewTicker(tunnelPoolInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case err := <-results:
					return err
				case <-ticker.C:
					pool.reconcile()
				}
			}
		},
		Shutdown: func(ctx context.Context) error {
			results := make(chan error, len(pool.clients))
			for _, client := range pool.clients {
				go func() { results <- client.Shutdown(ctx) }()
			}
			var resultErr error
			for range pool.clients {
				select {
				case err := <-results:
					resultErr = errors.Join(resultErr, err)
				case <-ctx.Done():
					return errors.Join(resultErr, ctx.Err())
				}
			}

			return resultErr
		},
	}
}

func (pool *tunnelPool) IsReady() bool {
	if pool == nil || !pool.initialReady.Load() {
		return false
	}
	for _, client := range pool.clients {
		if client.IsReady() {
			return true
		}
	}

	return false
}

func (pool *tunnelPool) reconcile() {
	identities := make(map[[protocolv1.InstanceIDBytes]byte]struct{}, len(pool.clients))
	ready := 0
	for _, client := range pool.clients {
		identity, found := client.ServerInstanceID()
		if !client.IsReady() || !found {
			continue
		}
		ready++
		if _, duplicated := identities[identity]; duplicated {
			client.RequestReconnect()
			continue
		}
		identities[identity] = struct{}{}
	}
	if ready >= tunnelReadyPaths && len(identities) >= tunnelReadyPaths {
		pool.initialReady.Store(true)
	}
}
