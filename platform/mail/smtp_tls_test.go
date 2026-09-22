package mail

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSMTPRequiresVerifiedTLSBeforeMessage(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	for _, trusted := range []bool{true, false} {
		t.Run(map[bool]string{true: "trusted", false: "untrusted"}[trusted], func(t *testing.T) {
			received := false
			address, done := serveSMTP(t, func(conn net.Conn) error {
				_, _ = io.WriteString(conn, "220 test\r\n")
				reader := bufio.NewReader(conn)
				if line, _ := reader.ReadString('\n'); !strings.HasPrefix(line, "EHLO ") {
					return errors.New("expected greeting")
				}
				_, _ = io.WriteString(conn, "250-test\r\n250 STARTTLS\r\n")
				if line, _ := reader.ReadString('\n'); line != "STARTTLS\r\n" {
					return errors.New("message before TLS")
				}
				_, _ = io.WriteString(conn, "220 ready\r\n")
				secure := tls.Server(conn, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
				if err := secure.Handshake(); err != nil {
					if trusted {
						return errors.New("trusted handshake failed")
					}
					return nil
				}
				if !trusted {
					return errors.New("untrusted certificate accepted")
				}
				reader = bufio.NewReader(secure)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return err
					}
					switch {
					case strings.HasPrefix(line, "EHLO "):
						_, _ = io.WriteString(secure, "250 test\r\n")
					case strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
						_, _ = io.WriteString(secure, "250 ok\r\n")
					case line == "DATA\r\n":
						_, _ = io.WriteString(secure, "354 send\r\n")
						for {
							line, err := reader.ReadString('\n')
							if err != nil {
								return err
							}
							if line == ".\r\n" {
								break
							}
						}
						received = true
						_, _ = io.WriteString(secure, "250 accepted\r\n")
					case line == "QUIT\r\n":
						_, _ = io.WriteString(secure, "221 bye\r\n")
						return nil
					default:
						return errors.New("unexpected command")
					}
				}
			})
			config := SMTPConfig{Address: address}
			if trusted {
				config.RootCAs = roots
			}
			sender, err := NewSMTP(config)
			if err != nil {
				t.Fatal(err)
			}
			sendErr := sender.Send(t.Context(), "buyer@example.test", "0123456789abcdef", smtpMessage())
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if trusted && (sendErr != nil || !received) {
				t.Fatal("verified delivery failed")
			}
			if !trusted && (sendErr == nil || received) {
				t.Fatal("unverified delivery accepted")
			}
		})
	}
}
