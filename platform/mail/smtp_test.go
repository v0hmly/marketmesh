package mail

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"strings"
	"testing"
	"time"
)

func smtpMessage() Message {
	return Message{From: "MarketMesh <noreply@example.test>", Subject: "Подтвердите почту", HTML: []byte("<p>Код: 123456</p>"), Text: []byte("Код: 123456\nВторая строка")}
}

func TestMessageMIMEAndInjection(t *testing.T) {
	_, _, raw, err := encodeMessage("buyer@example.test", "0123456789abcdef", smtpMessage())
	if err != nil {
		t.Fatal(err)
	}
	message, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := (&mime.WordDecoder{}).DecodeHeader(message.Header.Get("Subject"))
	if err != nil || subject != smtpMessage().Subject {
		t.Fatal("subject did not round trip")
	}
	_, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(message.Body, params["boundary"])
	for _, want := range [][]byte{smtpMessage().Text, smtpMessage().HTML} {
		part, err := reader.NextRawPart()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(quotedprintable.NewReader(part))
		if err != nil || strings.ReplaceAll(string(got), "\r\n", "\n") != string(want) {
			t.Fatal("body did not round trip")
		}
	}
	if _, err := reader.NextPart(); !errors.Is(err, io.EOF) {
		t.Fatal("unexpected MIME part")
	}
	for _, field := range []string{"from", "to", "subject", "id"} {
		t.Run(field, func(t *testing.T) {
			m, recipient, id := smtpMessage(), "buyer@example.test", "0123456789abcdef"
			payload := "safe\r\nBcc: leak@example.test"
			switch field {
			case "from":
				m.From = payload
			case "to":
				recipient = payload
			case "subject":
				m.Subject = payload
			case "id":
				id = payload
			}
			if _, _, _, err := encodeMessage(recipient, id, m); err == nil || strings.Contains(err.Error(), "leak") {
				t.Fatal("unsafe message accepted or leaked")
			}
		})
	}
}

func TestSMTPDeliveryAndSanitizedFailure(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "rejected"}[reject], func(t *testing.T) {
			address, done := serveSMTP(t, func(conn net.Conn) error {
				r := bufio.NewReader(conn)
				_, _ = io.WriteString(conn, "220 test\r\n")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return err
					}
					switch {
					case strings.HasPrefix(line, "EHLO "):
						_, _ = io.WriteString(conn, "250-test\r\n250 8BITMIME\r\n")
					case strings.HasPrefix(line, "MAIL FROM:"):
						_, _ = io.WriteString(conn, "250 ok\r\n")
					case strings.HasPrefix(line, "RCPT TO:"):
						if reject {
							_, _ = io.WriteString(conn, "550 private-recipient-and-secret\r\n")
							return nil
						}
						_, _ = io.WriteString(conn, "250 ok\r\n")
					case line == "DATA\r\n":
						_, _ = io.WriteString(conn, "354 data\r\n")
						for {
							line, err = r.ReadString('\n')
							if err != nil {
								return err
							}
							if line == ".\r\n" {
								break
							}
						}
						_, _ = io.WriteString(conn, "250 accepted\r\n")
						// Deliberately drop the connection instead of answering QUIT.
						return nil
					default:
						return errors.New("unexpected command")
					}
				}
			})
			sender, err := NewSMTP(SMTPConfig{Address: address, Environment: "test", PlaintextReason: "isolated test relay"})
			if err != nil {
				t.Fatal(err)
			}
			err = sender.Send(t.Context(), "buyer@example.test", "0123456789abcdef", smtpMessage())
			if reject {
				var delivery *DeliveryError
				if !errors.As(err, &delivery) || delivery.Code != 550 || delivery.Retryable || strings.Contains(err.Error(), "private") {
					t.Fatal("unsafe failure classification")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSMTPRequiresTLSAndHonorsCancellation(t *testing.T) {
	address, done := serveSMTP(t, func(conn net.Conn) error {
		_, _ = io.WriteString(conn, "220 test\r\n")
		_, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			return err
		}
		_, err = io.WriteString(conn, "250 no TLS\r\n")
		return err
	})
	sender, err := NewSMTP(SMTPConfig{Address: address})
	if err != nil {
		t.Fatal(err)
	}
	err = sender.Send(t.Context(), "buyer@example.test", "0123456789abcdef", smtpMessage())
	var delivery *DeliveryError
	if !errors.As(err, &delivery) || delivery.Stage != "tls_required" {
		t.Fatal("plaintext downgrade was accepted")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	address, done = serveSMTP(t, func(conn net.Conn) error { _, _ = io.Copy(io.Discard, conn); return nil })
	sender, _ = NewSMTP(SMTPConfig{Address: address})
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = sender.Send(ctx, "buyer@example.test", "0123456789abcdef", smtpMessage())
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatal("SMTP did not honor parent deadline")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSMTPConfiguration(t *testing.T) {
	for _, config := range []SMTPConfig{
		{}, {Address: "localhost:0"}, {Address: "localhost:1025", Timeout: time.Minute},
		{Address: "localhost:1025", Environment: "production", PlaintextReason: "no"},
		{Address: "localhost:1025", Environment: "test", PlaintextReason: "test", Username: "u", Password: "p"},
		{Address: "localhost:1025", Username: "u"},
	} {
		if _, err := NewSMTP(config); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}

// deadlinePendingContext models a socket reaching its deadline before the
// context cancellation timer has run; Err deliberately remains nil.
type deadlinePendingContext struct{ context.Context }

func (deadlinePendingContext) Deadline() (time.Time, bool) {
	return time.Now().Add(-time.Second), true
}

func TestSMTPDeadlineBeforeContextTimer(t *testing.T) {
	ctx := deadlinePendingContext{context.Background()}
	err := deliveryError(ctx, "greeting", &net.DNSError{IsTimeout: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("elapsed socket deadline lost its cancellation classification")
	}
}

func TestSMTPGreetingDisconnectIsRetryable(t *testing.T) {
	address, done := serveSMTP(t, func(conn net.Conn) error {
		_, _ = io.WriteString(conn, "220 test\r\n")
		_, err := bufio.NewReader(conn).ReadString('\n')
		return err
	})
	sender, err := NewSMTP(SMTPConfig{Address: address})
	if err != nil {
		t.Fatal(err)
	}
	err = sender.Send(t.Context(), "buyer@example.test", "0123456789abcdef", smtpMessage())
	var failure *DeliveryError
	if !errors.As(err, &failure) || !failure.Retryable || failure.Stage != "hello" {
		t.Fatal("temporary EHLO failure was classified as permanent")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func serveSMTP(t *testing.T, serve func(net.Conn) error) (string, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		done <- serve(conn)
	}()
	return listener.Addr().String(), done
}
