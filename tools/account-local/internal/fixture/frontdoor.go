package fixture

import (
	"context"
	"crypto/tls"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
)

var publicRPC = map[string]bool{
	"/auth.v1.AuthService/RegisterCredentials": true, "/auth.v1.AuthService/Login": true,
	"/auth.v1.AuthService/RefreshSession": true, "/auth.v1.AuthService/Logout": true, "/auth.v1.AuthService/LogoutAll": true,
	"/user.v1.UserService/GetMe": true, "/user.v1.UserService/UpdateMe": true,
	"/user.v1.UserService/ListAddresses":     true,
	"/user.v1.UserService/CreateAddress":     true,
	"/user.v1.UserService/UpdateAddress":     true,
	"/user.v1.UserService/DeleteAddress":     true,
	"/user.v1.UserService/SetDefaultAddress": true,
}

func frontdoorHandler(files fs.FS, proxy http.Handler) http.Handler {
	static := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/robots.txt" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
			}
			return
		}
		if publicRPC[r.URL.Path] {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			proxy.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		// Only known client-side routes fall back to index; private/unknown RPC paths never do.
		switch r.URL.Path {
		case "/", "/account", "/account/addresses", "/login", "/register":
			clone := r.Clone(r.Context())
			u := *r.URL
			clone.URL = &u
			clone.URL.Path = "/"
			static.ServeHTTP(w, clone)
		default:
			if !strings.HasPrefix(r.URL.Path, "/assets/") || strings.Contains(r.URL.Path, "..") {
				http.NotFound(w, r)
				return
			}
			static.ServeHTTP(w, r)
		}
	})
}

func Serve(ctx context.Context) error {
	cfg, err := clientTLS("/secrets", "gateway-in")
	if err != nil {
		return err
	}
	target, _ := url.Parse("https://gateway-in:8080")
	transport := &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true, ResponseHeaderTimeout: 15 * time.Second}
	defer transport.CloseIdleConnections()
	proxy := &httputil.ReverseProxy{Transport: transport, Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
		// Browser security context remains the original Cookie/Origin/Sec-Fetch-Site.
		// No forwarded header is used to assert secure transport or client identity.
		r.Out.Host = r.In.Host
		r.Out.Header.Del("Forwarded")
		r.Out.Header.Del("X-Forwarded-For")
		r.Out.Header.Del("X-Forwarded-Host")
		r.Out.Header.Del("X-Forwarded-Proto")
	}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "gateway unavailable", http.StatusBadGateway)
	}}
	server := &http.Server{TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13}, Addr: ":8443", Handler: frontdoorHandler(os.DirFS("/site"), proxy), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16384}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		case <-done:
		}
	}()
	err = server.ListenAndServeTLS("/secrets/cert.pem", "/secrets/key.pem")
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
