// Package sandbox sends opaque input to an isolated, credential-free parser.
package sandbox

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/v0hmly/marketmesh/services/files/internal/adapter/out/raster"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

type Client struct{ http *http.Client }

func New(socket string) (*Client, error) {
	if !filepath.IsAbs(socket) {
		return nil, file.ErrInvalid
	}
	dialer := &net.Dialer{Timeout: time.Second}
	transport := &http.Transport{Proxy: nil, MaxConnsPerHost: 1, DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socket)
	}, ResponseHeaderTimeout: 6 * time.Minute}
	return &Client{http: &http.Client{Transport: transport, Timeout: 7 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Clean(ctx context.Context, source io.Reader, size int64, format file.Format, output io.Writer) (file.Format, error) {
	if size <= 0 || size > file.MaxSize || format.Extension() == "" {
		return "", file.ErrInvalid
	}
	if err := c.ready(ctx); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://sandbox/clean", io.LimitReader(source, size))
	if err != nil {
		return "", file.ErrUnavailable
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", string(format))
	response, err := c.http.Do(req)
	if err != nil {
		return "", file.ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnprocessableEntity {
		return "", file.ErrRejected
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/x-marketmesh-rgb" {
		return "", file.ErrUnavailable
	}
	return raster.Reconstruct(io.LimitReader(response.Body, 3*raster.MaxPixels+8*raster.MaxPages+64), format == file.PNG || format == file.JPEG, output)
}

func (c *Client) ready(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	for ctx.Err() == nil {
		probe, stop := context.WithTimeout(ctx, time.Second)
		req, err := http.NewRequestWithContext(probe, http.MethodGet, "http://sandbox/ready", nil)
		if err != nil {
			stop()
			return file.ErrUnavailable
		}
		response, err := c.http.Do(req)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				stop()
				return nil
			}
		}
		stop()
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return file.ErrUnavailable
}
