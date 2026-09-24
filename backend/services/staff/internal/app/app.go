// Package app composes the staff service. Deployment supplies the corporate-only
// listener/network and a trusted IdP; no public gateway can forward staff RPCs.
package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/v0hmly/marketmesh/services/staff/internal/adapter/in/httpapi"
	"github.com/v0hmly/marketmesh/services/staff/internal/adapter/out/oidc"
	"github.com/v0hmly/marketmesh/services/staff/internal/adapter/out/postgres"
	"github.com/v0hmly/marketmesh/services/staff/internal/application"
)

type Config struct {
	CorporateNetworks                                                                  []string
	Listen, Origin, Issuer, ClientID, ClientSecret, CA, ClientCA, Cert, Key, DSN, Site string
	IdleSeconds, LifetimeSeconds                                                       int
}

func (c Config) Validate() error {
	if c.ClientCA == "" {
		return errors.New("staff corporate client CA required")
	}
	if len(c.CorporateNetworks) == 0 {
		return errors.New("staff corporate network allowlist required")
	}
	for _, value := range c.CorporateNetworks {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Bits() == 0 {
			return errors.New("invalid staff corporate network")
		}
	}
	for _, raw := range []string{c.Origin, c.Issuer} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return errors.New("invalid staff origin or issuer")
		}
	}
	if c.Origin == c.Issuer || c.Listen == "" || c.ClientID == "" || c.ClientSecret == "" || c.Site == "" || c.IdleSeconds < 1 || c.IdleSeconds > 1800 || c.LifetimeSeconds < c.IdleSeconds || c.LifetimeSeconds > 28800 {
		return errors.New("invalid staff configuration")
	}
	u, err := url.Parse(c.DSN)
	if err != nil || u.Query().Get("sslmode") != "verify-full" {
		return errors.New("staff database requires verify-full")
	}
	return nil
}
func Run(ctx context.Context, file string) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return errors.New("read staff configuration")
	}
	var c Config
	if err = json.Unmarshal(raw, &c); err != nil {
		return errors.New("invalid staff configuration")
	}
	if err = c.Validate(); err != nil {
		return err
	}
	roots := x509.NewCertPool()
	pem, err := os.ReadFile(c.CA)
	if err != nil || !roots.AppendCertsFromPEM(pem) {
		return errors.New("invalid staff CA")
	}
	serverTLS, err := corporateTLS(c.ClientCA)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}, Timeout: 10 * time.Second}
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	provider, err := oidc.New(initCtx, c.Issuer, c.ClientID, c.ClientSecret, c.Origin+"/sso/callback", client)
	if err != nil {
		return err
	}
	pool, err := pgxpool.New(initCtx, c.DSN)
	if err != nil {
		return errors.New("configure staff database")
	}
	defer pool.Close()
	if err = pool.Ping(initCtx); err != nil {
		return errors.New("staff database unavailable")
	}
	lifetime := time.Duration(c.LifetimeSeconds) * time.Second
	service := application.New(postgres.New(pool), provider, time.Duration(c.IdleSeconds)*time.Second, lifetime)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	networks := make([]netip.Prefix, 0, len(c.CorporateNetworks))
	for _, value := range c.CorporateNetworks {
		networks = append(networks, netip.MustParsePrefix(value))
	}
	server := &http.Server{Addr: c.Listen, Handler: httpapi.New(service, c.Origin, c.Site, lifetime, logger, networks), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, TLSConfig: serverTLS}
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		case <-stopped:
		}
	}()
	logger.Info("staff listening", "address", c.Listen)
	err = server.ListenAndServeTLS(c.Cert, c.Key)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// A corporate certificate gates every HTTP route before cookies or forwarded
// headers are read. Network ACLs alone are insufficient behind source NAT.
func corporateTLS(file string) (*tls.Config, error) {
	roots := x509.NewCertPool()
	pem, err := os.ReadFile(file)
	if err != nil || !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("invalid staff corporate client CA")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}, nil
}
