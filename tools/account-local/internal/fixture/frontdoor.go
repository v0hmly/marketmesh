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
	"/user.v1.UserService/GetAvatar":                true,
	"/user.v1.UserService/SetAvatar":                true,
	"/user.v1.UserService/ClearAvatar":              true,
	"/auth.v1.AuthService/StartLoginCodeChange":     true,
	"/auth.v1.AuthService/CompleteLoginCodeChange":  true,
	"/auth.v1.AuthService/ChangePassword":           true,
	"/auth.v1.AuthService/StartLogin":               true,
	"/auth.v1.AuthService/CompleteLogin":            true,
	"/auth.v1.AuthService/ResendLoginCode":          true,
	"/auth.v1.AuthService/RequestEmailVerification": true,
	"/auth.v1.AuthService/ConfirmEmail":             true,
	"/auth.v1.AuthService/RequestPasswordReset":     true,
	"/auth.v1.AuthService/ConfirmPasswordReset":     true,
	"/auth.v1.AuthService/GetCredentials":           true,
	"/auth.v1.AuthService/StartEmailChange":         true,
	"/auth.v1.AuthService/ConfirmEmailChange":       true,
	"/auth.v1.AuthService/CancelEmailChange":        true,
	"/auth.v1.AuthService/ListSessions":             true,
	"/auth.v1.AuthService/RevokeSession":            true,
	"/auth.v1.AuthService/RequestAccountDeletion":   true,
	"/auth.v1.AuthService/CancelAccountDeletion":    true,
	"/files.v1.FileService/CreateUpload":            true,
	"/files.v1.FileService/CompleteUpload":          true,
	"/files.v1.FileService/GetStatus":               true,
	"/files.v1.FileService/CreateDownload":          true,
	"/files.v1.FileService/Delete":                  true,
	"/user.v1.UserService/GetSettings":              true,
	"/user.v1.UserService/UpdateSettings":           true,
	"/auth.v1.AuthService/RegisterCredentials":      true, "/auth.v1.AuthService/Login": true,
	"/auth.v1.AuthService/RefreshSession": true, "/auth.v1.AuthService/Logout": true, "/auth.v1.AuthService/LogoutAll": true,
	"/user.v1.UserService/GetMe": true, "/user.v1.UserService/UpdateMe": true,
	"/user.v1.UserService/ListAddresses":     true,
	"/user.v1.UserService/CreateAddress":     true,
	"/user.v1.UserService/UpdateAddress":     true,
	"/user.v1.UserService/DeleteAddress":     true,
	"/user.v1.UserService/SetDefaultAddress": true,
}

func frontdoorHandler(files fs.FS, proxy http.Handler) http.Handler {
	return frontdoorWithPolicy(files, proxy, defaultContentPolicy)
}

const defaultContentPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

func filesContentPolicy(raw string) (string, error) {
	if raw == "" {
		return defaultContentPolicy, nil
	}
	origins := strings.Split(raw, ",")
	if len(origins) > 3 {
		return "", errors.New("invalid Files origins")
	}
	for _, origin := range origins {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || u.Host == "" || strings.Contains(u.Host, "*") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(origin, " \t\r\n;'\"") {
			return "", errors.New("invalid Files origin")
		}
	}
	result := strings.Replace(defaultContentPolicy, "img-src 'self'", "img-src 'self' blob:", 1)
	return strings.Replace(result, "connect-src 'self'", "connect-src 'self' "+strings.Join(origins, " "), 1), nil
}
func frontdoorWithPolicy(files fs.FS, proxy http.Handler, policy string) http.Handler {
	static := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", policy)
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
		case "/account/security", "/account/security/verify", "/account/security/reset", "/account/security/change_email", "/account/security/cancel_email", "/account/security/cancel_deletion", "/", "/account", "/account/id", "/account/addresses", "/account/settings", "/login", "/register":
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
	policy, err := filesContentPolicy(os.Getenv("ACCOUNT_FILES_ORIGINS"))
	if err != nil {
		return err
	}
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
	handler := frontdoorWithPolicy(os.DirFS("/site"), proxy, policy)
	if os.Getenv("ACCOUNT_RYBBIT_ENABLED") == "true" {
		mux := http.NewServeMux()
		mux.Handle("/analytics/track", localAnalyticsHandler(os.Getenv("RYBBIT_SITE_ID")))
		mux.Handle("/", handler)
		handler = mux
	}
	server := &http.Server{TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13}, Addr: ":8443", Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16384}
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
