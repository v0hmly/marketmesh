package testkit

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const defaultBufconnSize = 1024 * 1024

// BufconnConfig задаёт явные server/client options in-memory gRPC harness.
// При отсутствии DialOptions используется plaintext transport, доступный только
// через приватный bufconn listener текущего экземпляра.
type BufconnConfig struct {
	BufferSize    int
	ServerOptions []grpcgo.ServerOption
	DialOptions   []grpcgo.DialOption
}

// Bufconn владеет in-memory listener, gRPC server и единственным client
// connection. Close и автоматический Cleanup освобождают их ровно один раз.
type Bufconn struct {
	listener   *bufconn.Listener
	server     *grpcgo.Server
	connection *grpcgo.ClientConn
	runDone    chan error

	closeOnce sync.Once
	closeErr  error
}

// NewBufconn создаёт и запускает полностью изолированный gRPC harness.
// register вызывается до запуска server и должен зарегистрировать test services.
func NewBufconn(
	t testing.TB,
	config BufconnConfig,
	register func(grpcgo.ServiceRegistrar),
) *Bufconn {
	t.Helper()

	if register == nil {
		t.Fatalf("testkit: bufconn register function must not be nil")
	}
	bufferSize := config.BufferSize
	if bufferSize == 0 {
		bufferSize = defaultBufconnSize
	}
	if bufferSize < 0 {
		t.Fatalf("testkit: bufconn buffer size must be positive")
	}

	listener := bufconn.Listen(bufferSize)
	server := grpcgo.NewServer(slices.Clone(config.ServerOptions)...)
	register(server)

	dialOptions := slices.Clone(config.DialOptions)
	if len(dialOptions) == 0 {
		dialOptions = append(dialOptions, grpcgo.WithTransportCredentials(insecure.NewCredentials()))
	}
	dialOptions = append(dialOptions, grpcgo.WithContextDialer(
		func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		},
	))
	connection, err := grpcgo.NewClient("passthrough:///bufconn", dialOptions...)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("testkit: create bufconn client: %v", err)
	}
	runDone := make(chan error, 1)
	go func() {
		runDone <- server.Serve(listener)
	}()

	harness := &Bufconn{
		listener:   listener,
		server:     server,
		connection: connection,
		runDone:    runDone,
	}
	t.Cleanup(func() {
		if err := harness.Close(); err != nil {
			t.Errorf("testkit: close bufconn harness: %v", err)
		}
	})

	return harness
}

// Connection возвращает разделяемое client connection harness.
func (harness *Bufconn) Connection() *grpcgo.ClientConn {
	return harness.connection
}

// Close немедленно и конкурентно-безопасно останавливает все ресурсы harness.
func (harness *Bufconn) Close() error {
	harness.closeOnce.Do(func() {
		connectionErr := harness.connection.Close()
		harness.server.Stop()
		listenerErr := harness.listener.Close()

		var serveErr error
		timer := time.NewTimer(cleanupTimeout)
		defer timer.Stop()
		select {
		case serveErr = <-harness.runDone:
			if errors.Is(serveErr, grpcgo.ErrServerStopped) {
				serveErr = nil
			}
		case <-timer.C:
			serveErr = errors.New("bufconn server did not stop before timeout")
		}

		harness.closeErr = errors.Join(
			wrapCloseError("client connection", connectionErr),
			wrapCloseError("listener", listenerErr),
			wrapCloseError("server", serveErr),
		)
	})

	return harness.closeErr
}

func wrapCloseError(resource string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("close %s: %w", resource, err)
}
