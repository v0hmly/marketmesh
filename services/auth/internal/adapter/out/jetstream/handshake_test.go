package jetstream

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/publishregistration"
)

func TestConnectionOptionsDelegateDNSAndSuppressRawErrors(t *testing.T) {
	p, _ := New(testConfig(t))
	defer func() { _ = p.Close() }()
	options := nats.GetDefaultOptions()
	dialer := &handshakeDialer{}
	for _, apply := range p.connectionOptions(dialer) {
		if err := apply(&options); err != nil {
			t.Fatal(err)
		}
	}
	if !options.SkipHostLookup || options.CustomDialer != dialer || options.AllowReconnect || options.ReconnectBufSize != -1 {
		t.Fatal("unsafe connection options")
	}
	if options.AsyncErrorCB == nil {
		t.Fatal("raw default error handler remains installed")
	}
	// The explicit no-op callback receives errors without formatting or logging them.
	options.AsyncErrorCB(nil, nil, errors.New("private subject and server response"))
}
func TestDNSDialUsesContextAndCloseCancelsIt(t *testing.T) {
	c := testConfig(t)
	c.URL = "tls://never-resolve.invalid:4222"
	c.ConnectTimeout = 5 * time.Second
	c.PublishTimeout = 5 * time.Second
	p, _ := New(c)
	entered := make(chan struct{})
	p.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "never-resolve.invalid:4222" {
			t.Error("NATS bypassed DNS delegation", address)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("dial has no deadline")
		}
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		done <- p.Publish(context.Background(), publishregistration.Record{ID: [16]byte{1}, Payload: []byte{1}})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("custom DNS dialer not invoked")
	}
	closeDone := make(chan struct{})
	go func() { _ = p.Close(); close(closeDone) }()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close waited for stalled DNS")
	}
	if err := <-done; err == nil {
		t.Fatal("cancelled DNS publish succeeded")
	}
}
func TestRealBlackholeHandshakeCancellationAndClose(t *testing.T) {
	for _, phase := range []string{"INFO", "TLS"} {
		t.Run(phase, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			accepted := make(chan struct{})
			serverDone := make(chan struct{})
			shutdown := make(chan struct{})
			var once sync.Once
			defer func() { once.Do(func() { close(shutdown) }); _ = listener.Close(); <-serverDone }()
			go func() {
				defer close(serverDone)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				if phase == "TLS" {
					_, _ = io.WriteString(conn, "INFO {\"server_id\":\"blackhole\",\"version\":\"2.14.6\",\"proto\":1,\"tls_required\":true,\"max_payload\":16384}\r\n")
					_ = conn.SetReadDeadline(time.Now().Add(time.Second))
					var hello [1]byte
					if _, err := io.ReadFull(conn, hello[:]); err != nil || hello[0] != 0x16 {
						t.Error("TLS ClientHello not reached", err)
					}
					_ = conn.SetReadDeadline(time.Time{})
				}
				close(accepted)
				<-shutdown
			}()
			cfg := testConfig(t)
			cfg.URL = "tls://" + listener.Addr().String()
			cfg.ConnectTimeout = 5 * time.Second
			cfg.PublishTimeout = 5 * time.Second
			publisher, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = publisher.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- publisher.Publish(ctx, publishregistration.Record{ID: [16]byte{1}, Payload: []byte{1}})
			}()
			select {
			case <-accepted:
			case <-time.After(time.Second):
				t.Fatal("TCP handshake not reached")
			}
			cancel()
			closeDone := make(chan struct{})
			go func() { _ = publisher.Close(); close(closeDone) }()
			select {
			case <-closeDone:
			case <-time.After(time.Second):
				t.Fatal("Close stuck in blackhole handshake")
			}
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation cause lost", err)
				}
			case <-time.After(time.Second):
				t.Fatal("publish stuck in blackhole handshake")
			}
		})
	}
}

type deadlineRecorder struct {
	net.Conn
	deadline, read, write time.Time
}

func (c *deadlineRecorder) SetDeadline(d time.Time) error      { c.deadline = d; return nil }
func (c *deadlineRecorder) SetReadDeadline(d time.Time) error  { c.read = d; return nil }
func (c *deadlineRecorder) SetWriteDeadline(d time.Time) error { c.write = d; return nil }
func TestHandshakeDeadlineCannotResetAndSuccessfulReleaseSurvivesCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	raw, peer := net.Pipe()
	defer peer.Close()
	defer raw.Close()
	recorder := &deadlineRecorder{Conn: raw}
	dialer := &handshakeDialer{ctx: ctx, dial: func(context.Context, string, string) (net.Conn, error) { return recorder, nil }}
	conn, err := dialer.Dial("tcp", "ignored")
	if err != nil {
		t.Fatal(err)
	}
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !recorder.deadline.Equal(deadline) || !recorder.read.Equal(deadline) || !recorder.write.Equal(deadline) {
		t.Fatal("library extended handshake budget")
	}
	if err := dialer.release(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if !recorder.deadline.IsZero() {
		t.Fatal("handshake deadline was not cleared")
	}
	read := make(chan error, 1)
	go func() { _, err := peer.Write([]byte{1}); read <- err }()
	buffer := make([]byte, 1)
	if _, err := raw.Read(buffer); err != nil {
		t.Fatal("successful connection closed by connect context", err)
	}
	if err := <-read; err != nil {
		t.Fatal(err)
	}
}
