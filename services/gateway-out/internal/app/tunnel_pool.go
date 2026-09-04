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
	tunnelPoolSize            = 2
	tunnelPoolInterval        = 250 * time.Millisecond
	tunnelRediscoveryInterval = 5 * time.Second
)

type managedTunnel interface {
	IsReady() bool
	ServerInstanceID() ([protocolv1.InstanceIDBytes]byte, bool)
	RequestReconnect()
	Run(context.Context) error
	Shutdown(context.Context) error
}

// tunnelPool keeps two sessions on distinct gateway-in processes. Initial
// readiness is fail-closed until both paths are observed. Afterwards one path
// remains available while the other is periodically redistributed so a
// maxSurge gateway-in Pod can receive a tunnel before an old Pod is stopped.
type tunnelPool struct {
	clients      []managedTunnel
	initialReady atomic.Bool
	nextClient   int
}

func newTunnelPool(clients []managedTunnel) (*tunnelPool, error) {
	if len(clients) != tunnelPoolSize {
		return nil, errors.New("gateway-out: tunnel pool must contain two clients")
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
			rediscovery := time.NewTicker(tunnelRediscoveryInterval)
			defer rediscovery.Stop()
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case err := <-results:
					return err
				case <-ticker.C:
					pool.reconcile()
				case <-rediscovery.C:
					pool.rediscover()
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
	if ready == tunnelPoolSize && len(identities) == tunnelPoolSize {
		pool.initialReady.Store(true)
	}
}

func (pool *tunnelPool) rediscover() {
	if pool == nil || !pool.initialReady.Load() {
		return
	}
	pool.clients[pool.nextClient%len(pool.clients)].RequestReconnect()
	pool.nextClient++
}
