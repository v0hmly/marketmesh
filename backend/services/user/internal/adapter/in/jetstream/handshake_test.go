package jetstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"github.com/v0hmly/marketmesh/services/user/internal/application/consumeregistration"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/registrationevent"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

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

func testConfig(t *testing.T) Config {
	return Config{URL: "tls://localhost:4222", TLSConfig: &tls.Config{ServerName: "localhost", MinVersion: tls.VersionTLS13, RootCAs: x509.NewCertPool(), Certificates: []tls.Certificate{{Certificate: [][]byte{{1}}, PrivateKey: struct{}{}}}}}
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
			cfg.OperationTimeout = 5 * time.Second
			publisher, err := New(cfg, applyFunc(func(context.Context, registrationevent.Event) (consumeregistration.Outcome, error) {
				return consumeregistration.Applied, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = publisher.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- publisher.Run(ctx)
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
