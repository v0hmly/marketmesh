// Package clamav scans over a private Unix socket with bounded protocol framing.
package clamav

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

type Client struct {
	socket string
	now    func() time.Time
}

func New(socket string) (*Client, error) {
	if !filepath.IsAbs(socket) {
		return nil, file.ErrInvalid
	}
	return &Client{socket: socket, now: time.Now}, nil
}

func (c *Client) connect(ctx context.Context) (net.Conn, func(), error) {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, func() {}, file.ErrUnavailable
	}
	deadline := c.now().Add(2 * time.Minute)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	if err = conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return nil, func() {}, file.ErrUnavailable
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	return conn, func() { stop(); conn.Close() }, nil
}
func response(conn net.Conn) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(conn, 512)).ReadString(0)
	if err != nil || len(line) > 511 {
		return "", file.ErrUnavailable
	}
	return strings.TrimSuffix(line, "\x00"), nil
}
func (c *Client) fresh(ctx context.Context) error {
	conn, closeConn, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer closeConn()
	if _, err = io.WriteString(conn, "zVERSION\x00"); err != nil {
		return file.ErrUnavailable
	}
	version, err := response(conn)
	if err != nil {
		return err
	}
	parts := strings.Split(version, "/")
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "ClamAV ") {
		return file.ErrUnavailable
	}
	built, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", parts[2], time.UTC)
	if err != nil || built.After(c.now().Add(5*time.Minute)) || c.now().Sub(built) > 48*time.Hour {
		return file.ErrUnavailable
	}
	return nil
}

// Ready requires a responsive scanner with current signatures.
func (c *Client) Ready(ctx context.Context) error { return c.fresh(ctx) }

func (c *Client) Scan(ctx context.Context, source io.Reader, size int64) error {
	if source == nil || size <= 0 || size > file.MaxSize {
		return file.ErrInvalid
	}
	if err := c.fresh(ctx); err != nil {
		return err
	}
	conn, closeConn, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer closeConn()
	if _, err = io.WriteString(conn, "zINSTREAM\x00"); err != nil {
		return file.ErrUnavailable
	}
	buffer := make([]byte, 64*1024)
	remaining := size
	for remaining > 0 {
		n, err := io.ReadFull(source, buffer[:min(remaining, int64(len(buffer)))])
		if err != nil {
			return file.ErrRejected
		}
		if err = binary.Write(conn, binary.BigEndian, uint32(n)); err != nil {
			return file.ErrUnavailable
		}
		if _, err = conn.Write(buffer[:n]); err != nil {
			return file.ErrUnavailable
		}
		remaining -= int64(n)
	}
	var extra [1]byte
	if n, err := source.Read(extra[:]); n != 0 || err != io.EOF {
		return file.ErrRejected
	}
	if err = binary.Write(conn, binary.BigEndian, uint32(0)); err != nil {
		return file.ErrUnavailable
	}
	result, err := response(conn)
	if err != nil {
		return err
	}
	if result == "stream: OK" {
		return nil
	}
	if strings.HasPrefix(result, "stream: ") && strings.HasSuffix(result, " FOUND") {
		return file.ErrRejected
	}
	return file.ErrUnavailable // Includes limits, timeouts, errors and ambiguous responses.
}
