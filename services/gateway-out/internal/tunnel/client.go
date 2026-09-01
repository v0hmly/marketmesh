package tunnel

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	"github.com/v0hmly/marketmesh/platform/logger"
	platformruntime "github.com/v0hmly/marketmesh/platform/runtime"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

var (
	errConnect   = errors.New("gateway-out tunnel connection failed")
	errHandshake = errors.New("gateway-out tunnel handshake failed")
)

// Client поддерживает один последовательный lifecycle исходящего reverse tunnel.
type Client struct {
	settings settings
	registry *Registry
	observer observer

	used             atomic.Bool
	isReady          atomic.Bool
	serverMu         sync.RWMutex
	serverInstanceID [protocolv1.InstanceIDBytes]byte
	reconnect        chan struct{}

	stopCtx      context.Context
	stop         context.CancelFunc
	shutdownOnce sync.Once
	shutdownMu   sync.Mutex
	shutdownCtx  context.Context
	done         chan struct{}
}

// NewClient создаёт client без сетевых операций и listener.
func NewClient(config Config, registry *Registry) (*Client, error) {
	if registry == nil {
		return nil, errors.New("gateway-out tunnel registry must not be nil")
	}

	settings, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	for _, class := range registry.advertisedClasses() {
		if _, found := settings.classLimits[class]; !found {
			return nil, fmt.Errorf("gateway-out tunnel class %s is not configured", class)
		}
	}

	observer, err := newObserver(settings.telemetry)
	if err != nil {
		return nil, err
	}
	stopCtx, stop := context.WithCancel(context.Background())

	return &Client{
		settings:  settings,
		registry:  registry,
		observer:  observer,
		stopCtx:   stopCtx,
		stop:      stop,
		done:      make(chan struct{}),
		reconnect: make(chan struct{}, 1),
	}, nil
}

// Component адаптирует client к общему lifecycle MarketMesh.
func (client *Client) Component(name string) platformruntime.Component {
	return platformruntime.Component{
		Name:     name,
		Run:      client.Run,
		Shutdown: client.Shutdown,
	}
}

// IsReady reports whether a negotiated tunnel session currently accepts
// statically configured routes. It becomes false before reconnect and drain.
func (client *Client) IsReady() bool {
	if client == nil {
		return false
	}

	return client.isReady.Load()
}

// ServerInstanceID returns the opaque gateway-in process identifier for the
// current negotiated session. The identifier grants no authority.
func (client *Client) ServerInstanceID() ([protocolv1.InstanceIDBytes]byte, bool) {
	if client == nil || !client.isReady.Load() {
		return [protocolv1.InstanceIDBytes]byte{}, false
	}
	client.serverMu.RLock()
	defer client.serverMu.RUnlock()
	if !client.isReady.Load() {
		return [protocolv1.InstanceIDBytes]byte{}, false
	}

	return client.serverInstanceID, true
}

// RequestReconnect asks the running client to drain its current session and
// reconnect. Signals are coalesced and never create a parallel session.
func (client *Client) RequestReconnect() {
	if client == nil {
		return
	}
	select {
	case client.reconnect <- struct{}{}:
	default:
	}
}

// Run последовательно подключается и никогда не запускает параллельные reconnect sessions.
func (client *Client) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("gateway-out tunnel run context must not be nil")
	}
	if !client.used.CompareAndSwap(false, true) {
		return ErrClientUsed
	}
	client.isReady.Store(false)
	defer client.markNotReady()
	defer close(client.done)

	failures := 0
	for {
		if failures > 0 {
			delay := client.backoff(failures)
			client.settings.logger.WarnContext(
				ctx,
				"повторное подключение reverse tunnel отложено",
				logger.Int("attempt", failures+1),
				logger.Duration("backoff", delay),
			)
			if stopped := client.waitBackoff(ctx, delay); stopped {
				return nil
			}
		}

		attemptCtx, cancelAttempt := context.WithCancel(context.WithoutCancel(ctx))
		stopRoot := context.AfterFunc(ctx, cancelAttempt)
		stopShutdown := context.AfterFunc(client.stopCtx, cancelAttempt)
		session, connection, err := client.openSession(attemptCtx)
		stopRoot()
		stopShutdown()
		if err != nil {
			cancelAttempt()
			if connection != nil {
				_ = connection.Close()
			}
			if ctx.Err() != nil || client.stopCtx.Err() != nil {
				return nil
			}

			failures++
			client.observer.reconnect(ctx, "failed")
			if failures >= client.settings.reconnect.MaxAttempts {
				return ErrReconnectExhausted
			}
			continue
		}

		client.observer.connection(ctx, 1)
		client.observer.reconnect(ctx, "ready")
		client.markReady(session.serverInstanceID)
		client.settings.logger.InfoContext(ctx, "reverse tunnel готов")
		started := client.settings.now()

		sessionResult := make(chan error, 1)
		go func() {
			sessionResult <- session.run()
		}()

		select {
		case <-ctx.Done():
			client.markNotReady()
			drainCtx, cancelDrain := context.WithTimeout(
				context.WithoutCancel(ctx),
				client.settings.drainTimeout,
			)
			client.stopSession(drainCtx, session, connection)
			cancelDrain()
			<-sessionResult
			return nil
		case <-client.stopCtx.Done():
			client.markNotReady()
			drainCtx, cancelDrain := client.drainContext()
			client.stopSession(drainCtx, session, connection)
			cancelDrain()
			<-sessionResult
			return nil
		case <-client.reconnect:
			client.markNotReady()
			drainCtx, cancelDrain := context.WithTimeout(
				context.WithoutCancel(ctx),
				client.settings.drainTimeout,
			)
			client.stopSession(drainCtx, session, connection)
			cancelDrain()
			<-sessionResult
			cancelAttempt()
			client.settings.logger.InfoContext(ctx, "reverse tunnel перераспределён")
		case <-sessionResult:
			client.markNotReady()
			client.observer.connection(ctx, -1)
			cancelAttempt()
			_ = connection.Close()
			client.settings.logger.WarnContext(ctx, "reverse tunnel разорван")
		}

		if client.settings.now().Sub(started) >= client.settings.reconnect.StableResetAfter {
			failures = 0
		}
		failures++
		if failures >= client.settings.reconnect.MaxAttempts {
			return ErrReconnectExhausted
		}
	}
}

func (client *Client) markReady(instanceID [protocolv1.InstanceIDBytes]byte) {
	client.serverMu.Lock()
	client.serverInstanceID = instanceID
	client.serverMu.Unlock()
	client.isReady.Store(true)
}

func (client *Client) markNotReady() {
	client.isReady.Store(false)
	client.serverMu.Lock()
	client.serverInstanceID = [protocolv1.InstanceIDBytes]byte{}
	client.serverMu.Unlock()
}

// Shutdown инициирует Drain и ждёт завершения Run в пределах ctx.
func (client *Client) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("gateway-out tunnel shutdown context must not be nil")
	}
	client.shutdownOnce.Do(func() {
		client.shutdownMu.Lock()
		client.shutdownCtx = ctx
		client.shutdownMu.Unlock()
		client.stop()
	})

	select {
	case <-client.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (client *Client) openSession(ctx context.Context) (*session, *grpcgo.ClientConn, error) {
	options := []grpcgo.DialOption{
		grpcgo.WithTransportCredentials(credentials.NewTLS(client.settings.tlsConfig)),
		grpcgo.WithDisableRetry(),
		grpcgo.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                client.settings.keepaliveTime,
			Timeout:             client.settings.keepaliveTimeout,
			PermitWithoutStream: false,
		}),
		grpcgo.WithStatsHandler(client.settings.telemetry.GRPCClientStatsHandler()),
		grpcgo.WithDefaultCallOptions(
			grpcgo.ForceCodec(strictCodec{}),
			grpcgo.MaxCallRecvMsgSize(protocolv1.MaxEncodedFrameBytes),
			grpcgo.MaxCallSendMsgSize(protocolv1.MaxEncodedFrameBytes),
		),
	}
	if client.settings.dialer != nil {
		options = append(options, grpcgo.WithContextDialer(client.settings.dialer))
	}

	connection, err := grpcgo.NewClient(client.settings.target, options...)
	if err != nil {
		return nil, nil, errConnect
	}
	connectCtx, cancelConnect := context.WithTimeout(ctx, client.settings.connectTimeout)
	defer cancelConnect()
	if err := waitReady(connectCtx, connection); err != nil {
		return nil, connection, errConnect
	}

	session, err := newSession(ctx, client.settings, client.registry, client.observer, connection)
	if err != nil {
		return nil, connection, errHandshake
	}

	return session, connection, nil
}

func waitReady(ctx context.Context, connection *grpcgo.ClientConn) error {
	connection.Connect()
	for {
		state := connection.GetState()
		if state == connectivity.Ready {
			return nil
		}
		if !connection.WaitForStateChange(ctx, state) {
			return ctx.Err()
		}
	}
}

func (client *Client) stopSession(
	ctx context.Context,
	session *session,
	connection *grpcgo.ClientConn,
) {
	_ = session.drain(ctx, drainLocalShutdown)
	session.cancel()
	client.observer.connection(context.WithoutCancel(ctx), -1)
	_ = connection.Close()
}

func (client *Client) waitBackoff(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return false
	case <-ctx.Done():
		return true
	case <-client.stopCtx.Done():
		return true
	}
}

func (client *Client) backoff(failures int) time.Duration {
	delay := client.settings.reconnect.InitialBackoff
	for range max(failures-1, 0) {
		next := time.Duration(float64(delay) * client.settings.reconnect.Multiplier)
		if next < delay || next > client.settings.reconnect.MaxBackoff {
			delay = client.settings.reconnect.MaxBackoff
			break
		}
		delay = next
	}

	jitterBound := time.Duration(float64(delay) * client.settings.reconnect.JitterRatio)
	if jitterBound <= 0 {
		return delay
	}
	random := client.settings.jitter(2 * jitterBound)
	if random < 0 || random > 2*jitterBound {
		random = jitterBound
	}

	return min(delay-jitterBound+random, client.settings.reconnect.MaxBackoff)
}

func (client *Client) drainContext() (context.Context, context.CancelFunc) {
	client.shutdownMu.Lock()
	shutdownCtx := client.shutdownCtx
	client.shutdownMu.Unlock()
	if shutdownCtx == nil {
		shutdownCtx = context.Background()
	}

	return context.WithTimeout(shutdownCtx, client.settings.drainTimeout)
}
