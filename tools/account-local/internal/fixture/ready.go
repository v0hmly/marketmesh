package fixture

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Ready verifies both the real anonymous RPC boundary and the built storefront
// through the generated CA, independent of shared browser/system trust settings.
func Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cfg, err := clientTLS(filepath.Join(os.Getenv("FIXTURE_ROOT"), "browser"), "localhost")
	if err != nil {
		return err
	}
	transport := &http.Transport{TLSClientConfig: cfg}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	origin := "https://localhost:" + os.Getenv("ACCOUNT_LOCAL_PORT")
	for ctx.Err() == nil {
		if readyOnce(ctx, client, origin) {
			return nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	return errors.New("account stack did not become ready with verified TLS")
}

func readyOnce(ctx context.Context, client *http.Client, origin string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/user.v1.UserService/GetMe", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Origin", origin)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	response, err := client.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		return false
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, origin+"/account", nil)
	if err != nil {
		return false
	}
	response, err = client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/html") {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	return err == nil && len(body) <= 64*1024 && bytes.Contains(body, []byte(`id="app"`))
}
