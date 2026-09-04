package podchaos

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/frontdoor"
)

const (
	gatewayInHTTPNodePort = 30080
	frontDoorMaxBodyBytes = int64(64 * 1024)
	frontDoorShutdown     = 10 * time.Second
)

// LocalFrontDoor owns the loopback-only MM-30 entry point used by one MM-32
// runner. Close is bounded and idempotent.
type LocalFrontDoor struct {
	endpoint  string
	cancel    context.CancelFunc
	server    *http.Server
	done      chan error
	closeOnce sync.Once
	closeErr  error
}

// StartLocalFrontDoor derives only the fixed MM-29 NodePort around the actual
// MM-28 control-plane addresses. It does not discover or guess addresses.
func StartLocalFrontDoor(
	ctx context.Context,
	inventory TopologyInventory,
) (*LocalFrontDoor, error) {
	if !hasDeadline(ctx) {
		return nil, fmt.Errorf("%w: front door context is invalid", ErrInvalidConfiguration)
	}
	if err := validateTopologyInventory(inventory, inventory.Instance); err != nil {
		return nil, err
	}
	dcATarget, dcBTarget, err := frontDoorTargets(inventory)
	if err != nil {
		return nil, err
	}
	instance, err := frontdoor.New(frontdoor.Config{
		DCATarget: dcATarget, DCBTarget: dcBTarget,
		HealthCheckInterval: time.Second,
		HealthCheckTimeout:  250 * time.Millisecond,
		FailbackWarmup:      30 * time.Second,
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("podchaos: starting local front door listener")
	}
	runCtx, cancel := context.WithCancel(ctx)
	server := &http.Server{
		Handler:           http.MaxBytesHandler(instance.Handler(), frontDoorMaxBodyBytes),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 * 1024,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	result := &LocalFrontDoor{
		endpoint: "http://" + listener.Addr().String(),
		cancel:   cancel,
		server:   server,
		done:     make(chan error, 2),
	}
	go func() {
		err := instance.Run(runCtx)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			err = nil
		}
		result.done <- err
	}()
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		result.done <- err
	}()
	go func() {
		<-runCtx.Done()
		_ = result.Close()
	}()
	return result, nil
}

// Endpoint returns the literal loopback HTTP URL accepted by MM-31.
func (frontDoor *LocalFrontDoor) Endpoint() string {
	if frontDoor == nil {
		return ""
	}
	return frontDoor.endpoint
}

// Close stops health checks and HTTP serving, then joins both goroutines.
func (frontDoor *LocalFrontDoor) Close() error {
	if frontDoor == nil {
		return nil
	}
	frontDoor.closeOnce.Do(func() {
		frontDoor.cancel()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), frontDoorShutdown)
		shutdownErr := frontDoor.server.Shutdown(shutdownCtx)
		cancel()
		firstErr := <-frontDoor.done
		secondErr := <-frontDoor.done
		frontDoor.closeErr = errors.Join(shutdownErr, firstErr, secondErr)
	})
	return frontDoor.closeErr
}

func frontDoorTargets(inventory TopologyInventory) (string, string, error) {
	addresses := map[DC]netip.Addr{}
	for _, cluster := range inventory.Clusters {
		if cluster.Zone != ZoneDMZ {
			continue
		}
		address, err := netip.ParseAddr(cluster.ControlPlaneAddress)
		if err != nil || !address.Is4() {
			return "", "", fmt.Errorf(
				"%w: front door topology address is invalid",
				ErrInvalidConfiguration,
			)
		}
		addresses[DC(cluster.DC)] = address
	}
	if len(addresses) != 2 || !addresses[DCA].IsValid() || !addresses[DCB].IsValid() {
		return "", "", fmt.Errorf(
			"%w: front door topology is incomplete",
			ErrInvalidConfiguration,
		)
	}
	target := func(address netip.Addr) string {
		return "http://" + net.JoinHostPort(address.String(), strconv.Itoa(gatewayInHTTPNodePort))
	}
	return target(addresses[DCA]), target(addresses[DCB]), nil
}
