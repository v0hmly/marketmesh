package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxMessageBytes = 1024 * 1024

// SMTPConfig selects one trusted relay. Plaintext is permitted only for an
// explicitly documented development/test relay, without authentication.
type SMTPConfig struct {
	Address, Username, Password  string
	Environment, PlaintextReason string
	RootCAs                      *x509.CertPool
	Timeout                      time.Duration
}

// SMTP sends one multipart message per connection. It never logs message data.
// A stable Message-ID helps operators identify retries; SMTP is at-least-once.
type SMTP struct {
	config SMTPConfig
	host   string
}

// DeliveryError carries only a bounded stage and SMTP reply code, never the
// relay's reply text, an address, credentials, or message content.
type DeliveryError struct {
	Stage     string
	Code      int
	Retryable bool
}

func (e *DeliveryError) Error() string {
	return fmt.Sprintf("mail: SMTP %s failed (code %d)", e.Stage, e.Code)
}

func NewSMTP(config SMTPConfig) (*SMTP, error) {
	host, port, err := net.SplitHostPort(config.Address)
	n, portErr := strconv.Atoi(port)
	if err != nil || host == "" || strings.ContainsAny(host, "\r\n\x00") || portErr != nil || n < 1 || n > 65535 {
		return nil, errors.New("mail: invalid SMTP address")
	}
	if config.Timeout == 0 {
		config.Timeout = 10 * time.Second
	}
	if config.Timeout < time.Second || config.Timeout > 30*time.Second {
		return nil, errors.New("mail: SMTP timeout must be between 1s and 30s")
	}
	if (config.Username == "") != (config.Password == "") {
		return nil, errors.New("mail: incomplete SMTP authentication")
	}
	if config.PlaintextReason != "" {
		if (config.Environment != "development" && config.Environment != "test") || strings.TrimSpace(config.PlaintextReason) == "" || config.Username != "" || config.RootCAs != nil {
			return nil, errors.New("mail: plaintext SMTP requires an isolated development/test relay without credentials")
		}
	}
	if config.RootCAs != nil {
		config.RootCAs = config.RootCAs.Clone()
	}
	return &SMTP{config: config, host: host}, nil
}

// Send returns success only after the relay acknowledges DATA. Failure after
// sending DATA may have an unknown outcome: a retry can deliver a duplicate.
// messageID is an opaque queue ID (ASCII letters/digits/hyphens), not a secret.
func (s *SMTP) Send(ctx context.Context, recipient, messageID string, message Message) error {
	if s == nil || ctx == nil {
		return errors.New("mail: sender and context are required")
	}
	from, to, wire, err := encodeMessage(recipient, messageID, message)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", s.config.Address)
	if err != nil {
		return deliveryError(ctx, "connect", err)
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return deliveryError(ctx, "deadline", err)
	}
	client, err := smtp.NewClient(conn, s.host)
	if err != nil {
		return deliveryError(ctx, "greeting", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Hello("localhost"); err != nil {
		return deliveryError(ctx, "hello", err)
	}
	if s.config.PlaintextReason == "" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return &DeliveryError{Stage: "tls_required", Retryable: false}
		}
		if err := client.StartTLS(&tls.Config{MinVersion: tls.VersionTLS13, ServerName: s.host, RootCAs: s.config.RootCAs}); err != nil {
			return deliveryError(ctx, "tls", err)
		}
	}
	if s.config.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.config.Username, s.config.Password, s.host)); err != nil {
			return deliveryError(ctx, "authentication", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return deliveryError(ctx, "sender", err)
	}
	if err := client.Rcpt(to); err != nil {
		return deliveryError(ctx, "recipient", err)
	}
	writer, err := client.Data()
	if err != nil {
		return deliveryError(ctx, "data", err)
	}
	if _, err := writer.Write(wire); err != nil {
		return deliveryError(ctx, "body", err)
	}
	if err := writer.Close(); err != nil {
		return deliveryError(ctx, "acknowledgement", err)
	}
	// DATA has been accepted. A failed QUIT must not turn this into a retry.
	_ = client.Quit()
	return nil
}

func deliveryError(ctx context.Context, stage string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// The connection deadline and the context timer are independent. The socket
	// may time out before the context's timer goroutine has set Err.
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	result := &DeliveryError{Stage: stage, Retryable: true}
	var reply *textproto.Error
	if errors.As(err, &reply) && reply.Code >= 400 && reply.Code <= 599 {
		result.Code = reply.Code
		result.Retryable = reply.Code < 500
	}
	return result
}

func encodeMessage(recipient, id string, message Message) (string, string, []byte, error) {
	invalid := errors.New("mail: invalid outgoing message")
	if len(message.HTML)+len(message.Text) > maxMessageBytes/2 || len(message.HTML) == 0 || len(message.Text) == 0 || !utf8.Valid(message.HTML) || !utf8.Valid(message.Text) || !validHeader(message.Subject, 240) || !validHeader(message.From, 320) || !validHeader(recipient, 254) {
		return "", "", nil, invalid
	}
	if len(id) < 16 || len(id) > 64 || strings.IndexFunc(id, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-')
	}) >= 0 {
		return "", "", nil, invalid
	}
	from, err := netmail.ParseAddress(message.From)
	if err != nil || !asciiMailbox(from.Address) {
		return "", "", nil, invalid
	}
	to, err := netmail.ParseAddress(recipient)
	if err != nil || to.Name != "" || to.Address != recipient || !asciiMailbox(to.Address) {
		return "", "", nil, invalid
	}
	var body bytes.Buffer
	multi := multipart.NewWriter(&body)
	for _, part := range []struct {
		kind string
		data []byte
	}{{"text/plain", message.Text}, {"text/html", message.HTML}} {
		header := textproto.MIMEHeader{"Content-Type": {part.kind + "; charset=utf-8"}, "Content-Transfer-Encoding": {"quoted-printable"}}
		writer, err := multi.CreatePart(header)
		if err != nil {
			return "", "", nil, invalid
		}
		encoded := quotedprintable.NewWriter(writer)
		if _, err := encoded.Write(part.data); err != nil {
			return "", "", nil, invalid
		}
		if err := encoded.Close(); err != nil {
			return "", "", nil, invalid
		}
	}
	if err := multi.Close(); err != nil {
		return "", "", nil, invalid
	}
	var wire bytes.Buffer
	// Address.String and WordEncoder encode display names/subjects; every header
	// input is checked for controls before assembly (including CR/LF injection).
	fmt.Fprintf(&wire, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n", from.String(), to.String(), mime.QEncoding.Encode("utf-8", message.Subject), time.Now().UTC().Format(time.RFC1123Z), id, strings.Split(from.Address, "@")[1], multi.Boundary())
	wire.Write(body.Bytes())
	if wire.Len() > maxMessageBytes {
		return "", "", nil, invalid
	}
	return from.Address, to.Address, wire.Bytes(), nil
}

func validHeader(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}
func asciiMailbox(value string) bool {
	return len(value) <= 254 && strings.Count(value, "@") == 1 && strings.IndexFunc(value, func(r rune) bool { return r < 33 || r > 126 || strings.ContainsRune("<>\"\\(),;:", r) }) < 0
}
