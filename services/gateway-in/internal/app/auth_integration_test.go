//go:build integration && authbrowserintegration

package app

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/v0hmly/marketmesh/platform/logger"
)

// This exercises a verified HTTPS cookie jar, not a browser engine. Auth and
// gateway-out and User are real binaries with isolated Auth and User databases.
func TestIntegrationAuthBrowser(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	required := func(k string) string {
		t.Helper()
		v := os.Getenv(k)
		if v == "" {
			t.Fatalf("%s required; use disposable auth fixture", k)
		}
		return v
	}
	db, err := pgxpool.New(ctx, required("MARKETMESH_AUTH_POSTGRES_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{"000001_credentials.up.sql", "000002_sessions.up.sql"} {
		raw, err := os.ReadFile(filepath.Join("../../../auth/migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { _, _ = db.Exec(context.Background(), "DROP SCHEMA auth CASCADE") }()
	pki := newAuthFixturePKI(t)
	edgeCert, edgeKey := pki.issue(t, "gateway-in")
	outCert, outKey := pki.issue(t, "gateway-out")
	authCert, authKey := pki.issue(t, "auth")
	userCert, userKey := pki.issue(t, "user")
	edgeHTTP, edgeGRPC, authHTTP, authGRPC, outHTTP := authFreeAddress(t), authFreeAddress(t), authFreeAddress(t), authFreeAddress(t), authFreeAddress(t)
	origin := "https://" + edgeHTTP
	keyPub, keyPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	keyJSON, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{"kid": "fixture", "private_key": base64.RawURLEncoding.EncodeToString(keyPriv), "public_key": base64.RawURLEncoding.EncodeToString(keyPub), "sign_from": now.Add(-time.Hour), "sign_until": now.Add(time.Hour), "verify_until": now.Add(2 * time.Hour)}}})
	keysPath := filepath.Join(t.TempDir(), "keys.json")
	if err = os.WriteFile(keysPath, keyJSON, 0600); err != nil {
		t.Fatal(err)
	}
	logs := &authFixtureLogs{}
	authProcess := authStartProcess(t, ctx, authFixtureBinary("AUTH_BROWSER_AUTH_BIN", "/usr/local/bin/auth"), map[string]string{
		"SERVICE_VERSION": "test", "ENVIRONMENT": "test", "SERVICE_INSTANCE_ID": "auth-browser-fixture", "HTTP_ADDRESS": authHTTP, "SHUTDOWN_TIMEOUT": "2s",
		"POSTGRES_RW_DSN": required("MARKETMESH_AUTH_POSTGRES_DSN"), "POSTGRES_RO_DSN": required("MARKETMESH_AUTH_POSTGRES_RO_DSN"),
		"BCRYPT_COST":           "10",
		"AUTH_SESSIONS_ENABLED": "true", "AUTH_REGISTRATION_PUBLISH_ENABLED": "false", "AUTH_SESSION_ISSUER": "auth.marketmesh", "AUTH_SESSION_KEYS_FILE": keysPath, "AUTH_TRUST_DOMAIN": "marketmesh.test",
		"AUTH_INTERNAL_ADDRESS": authGRPC, "AUTH_INTERNAL_TLS_CERT_FILE": authCert, "AUTH_INTERNAL_TLS_KEY_FILE": authKey, "AUTH_INTERNAL_CLIENT_CA_FILE": pki.caPath,
		"AUTH_ALLOWED_ORIGINS": origin, "AUTH_SESSION_AUDIENCES": `{"user":["user:profile:read","user:profile:write"]}`,
		"AUTH_REDIS_ADDRESS": required("MARKETMESH_AUTH_REDIS_ADDRESS"), "AUTH_REDIS_PASSWORD": required("MARKETMESH_AUTH_REDIS_PASSWORD"), "AUTH_REDIS_PLAINTEXT_REASON": "isolated disposable integration network",
	}, logs)
	authAwait(t, ctx, func() bool {
		c, e := net.DialTimeout("tcp", authGRPC, 100*time.Millisecond)
		if e != nil {
			return false
		}
		_ = c.Close()
		return true
	}, "Auth listener", authProcess.check)
	userDB, userProcess, userAddress := startBrowserUserFixture(t, ctx, db, pki, userCert, userKey, authGRPC, logs)
	cfg := config{serviceVersion: "test", environment: "test", instanceID: "auth-browser-edge", dataCenter: "dc-a", httpAddress: edgeHTTP, grpcAddress: edgeGRPC, tlsCertificate: edgeCert, tlsPrivateKey: edgeKey, tlsClientCA: pki.caPath, expectedGatewayOutURI: "spiffe://marketmesh.test/test/gateway-out", requestTimeout: 3 * time.Second, tunnelSessionTimeout: time.Minute, shutdownTimeout: 2 * time.Second, healthTimeout: time.Second, authBrowserEnabled: true, userBrowserEnabled: true, publicTLSCertificate: edgeCert, publicTLSPrivateKey: edgeKey}
	log, err := logger.New(logger.Config{Service: "gateway-in", Version: "test", Environment: "test", Output: logs})
	if err != nil {
		t.Fatal(err)
	}
	edgeCtx, edgeCancel := context.WithCancel(ctx)
	defer edgeCancel()
	edgeDone := make(chan error, 1)
	go func() { edgeDone <- runService(edgeCtx, cfg, log, net.Listen) }()
	defer func() {
		edgeCancel()
		select {
		case err := <-edgeDone:
			if err != nil {
				t.Errorf("gateway-in shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("gateway-in shutdown timeout")
		}
	}()
	outProcess := authStartProcess(t, ctx, authFixtureBinary("AUTH_BROWSER_GATEWAY_OUT_BIN", "/usr/local/bin/gateway-out"), map[string]string{
		"SERVICE_VERSION": "test", "ENVIRONMENT": "test", "SERVICE_INSTANCE_ID": "auth-browser-out", "HTTP_ADDRESS": outHTTP, "SHUTDOWN_TIMEOUT": "2s",
		"GATEWAY_IN_TARGET": edgeGRPC, "GATEWAY_IN_SERVER_NAME": "localhost", "EXPECTED_GATEWAY_IN_URI": "spiffe://marketmesh.test/test/gateway-in",
		"INTERNAL_TARGET": userAddress, "INTERNAL_SERVER_NAME": "localhost", "EXPECTED_INTERNAL_URI": "spiffe://marketmesh.test/test/user",
		"TUNNEL_TLS_CERT_FILE": outCert, "TUNNEL_TLS_KEY_FILE": outKey, "TUNNEL_TLS_ROOT_CA_FILE": pki.caPath,
		"INTERNAL_TLS_CERT_FILE": outCert, "INTERNAL_TLS_KEY_FILE": outKey, "INTERNAL_TLS_ROOT_CA_FILE": pki.caPath,
		"AUTH_BROWSER_ENABLED": "true", "USER_BROWSER_ENABLED": "true", "AUTH_TARGET": authGRPC, "AUTH_SERVER_NAME": "localhost", "EXPECTED_AUTH_URI": "spiffe://marketmesh.test/test/auth",
		"AUTH_TLS_CERT_FILE": outCert, "AUTH_TLS_KEY_FILE": outKey, "AUTH_TLS_ROOT_CA_FILE": pki.caPath,
	}, logs)
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pki.roots}}
	defer transport.CloseIdleConnections()
	bare := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	authAwait(t, ctx, func() bool {
		r, e := bare.Get(origin + "/readyz")
		if e != nil {
			return false
		}
		defer r.Body.Close()
		return r.StatusCode == http.StatusNoContent
	}, "edge routes ready", authProcess.check, outProcess.check, func() error {
		select {
		case err := <-edgeDone:
			edgeDone <- err
			return fmt.Errorf("gateway-in exited: %v", err)
		default:
			return nil
		}
	})
	jar1, _ := cookiejar.New(nil)
	jar2, _ := cookiejar.New(nil)
	first := &http.Client{Transport: transport, Jar: jar1, Timeout: 5 * time.Second}
	second := &http.Client{Transport: transport, Jar: jar2, Timeout: 5 * time.Second}
	marker := "password-marker-DoNotLog-731!"
	payload, _ := json.Marshal(map[string]string{"identifier": "browser-fixture@example.test", "password": base64.StdEncoding.EncodeToString([]byte(marker))})
	var secrets []string
	call := func(client *http.Client, method string, body []byte, origins []string, fetch string, cookie string) (int, *http.Response) {
		t.Helper()
		if body == nil {
			body = []byte(`{}`)
		}
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/auth.v1.AuthService/"+method, bytes.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
		for _, v := range origins {
			req.Header.Add("Origin", v)
		}
		if fetch != "" {
			req.Header.Set("Sec-Fetch-Site", fetch)
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if e != nil {
			t.Fatal(e)
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s missing no-store (%d)", method, res.StatusCode)
		}
		for _, s := range append(secrets, marker) {
			if s != "" && bytes.Contains(raw, []byte(s)) {
				t.Fatal("public body leaked credential")
			}
		}
		if bytes.Contains(raw, []byte("set_cookie")) || bytes.Contains(raw, []byte("setCookie")) {
			t.Fatal("private wrapper exposed")
		}
		return res.StatusCode, res
	}
	ok := func(client *http.Client, method string, body []byte) *http.Response {
		t.Helper()
		code, res := call(client, method, body, []string{origin}, "same-origin", "")
		if code != 200 {
			t.Fatalf("%s status %d", method, code)
		}
		return res
	}
	cookies := func(res *http.Response) {
		t.Helper()
		cs := res.Cookies()
		if len(cs) != 2 {
			t.Fatalf("expected two cookies, got %d", len(cs))
		}
		for _, c := range cs {
			if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" || !strings.HasPrefix(c.Name, "__Host-mm-") {
				t.Fatal("unsafe cookie attributes")
			}
			secrets = append(secrets, c.Value)
		}
	}
	for _, o := range [][]string{nil, {"null"}, {"https://evil.example"}, {origin, origin}} {
		code, _ := call(bare, "RegisterCredentials", payload, o, "same-origin", "")
		if code == 200 {
			t.Fatal("invalid origin accepted")
		}
	}
	if code, _ := call(bare, "RegisterCredentials", payload, []string{origin}, "cross-site", ""); code == 200 {
		t.Fatal("cross-site accepted")
	}
	if code, _ := call(bare, "Login", payload, []string{origin}, "same-origin", "x="+strings.Repeat("a", 8193)); code == 200 {
		t.Fatal("oversized cookie accepted")
	}
	if code, _ := call(bare, "Login", bytes.Repeat([]byte("x"), 64*1024+1), []string{origin}, "same-origin", ""); code != http.StatusRequestEntityTooLarge {
		t.Fatal("platform body limit not applied")
	}
	ok(first, "RegisterCredentials", payload)
	cookies(ok(first, "Login", payload))
	userChecks := exerciseBrowserUser(t, ctx, origin, first, bare, db, userDB, logs)
	originURL, _ := url.Parse(origin)
	old := authCookieHeader(jar1.Cookies(originURL))
	cookies(ok(first, "RefreshSession", nil))
	if old == authCookieHeader(jar1.Cookies(originURL)) {
		t.Fatal("refresh did not rotate cookies")
	}
	code, _ := call(bare, "RefreshSession", nil, []string{origin}, "same-origin", old)
	if code == 200 {
		t.Fatal("refresh replay accepted")
	}
	if code, _ = call(first, "Logout", nil, []string{origin}, "same-origin", ""); code == 200 {
		t.Fatal("replayed family not revoked")
	}
	cookies(ok(first, "Login", payload))
	cookies(ok(second, "Login", payload))
	cookies(ok(first, "LogoutAll", nil))
	if code, _ = call(second, "RefreshSession", nil, []string{origin}, "same-origin", ""); code == 200 {
		t.Fatal("LogoutAll did not revoke second session")
	}
	cookies(ok(first, "Login", payload))
	cookies(ok(first, "Logout", nil))
	if len(jar1.Cookies(originURL)) != 0 {
		t.Fatal("logout did not clear jar")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/auth.v1.AuthBrowserService/BrowserLogin", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := bare.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatal("private Auth service exposed")
	}
	callBrowserProbe(t, ctx, origin, edgeCert)
	cookies(ok(first, "Login", payload))
	userChecks.beforeOutage()
	_ = authProcess.Process.Kill()
	<-authProcess.done
	userChecks.outage("Auth")
	if code, _ = call(first, "Login", payload, []string{origin}, "same-origin", ""); code == 200 {
		t.Fatal("Auth outage accepted")
	}
	authProcess = authRestartProcess(t, ctx, authProcess, logs)
	authAwait(t, ctx, userChecks.healthy, "User recovered after Auth restart", authProcess.check, userProcess.check)
	_ = userProcess.Process.Kill()
	<-userProcess.done
	userChecks.outage("User")
	for _, secret := range append(secrets, marker) {
		if secret != "" && strings.Contains(logs.String(), secret) {
			t.Fatal("logs leaked browser credential")
		}
	}
}

func authCookieHeader(cookies []*http.Cookie) string {
	var values []string
	for _, c := range cookies {
		values = append(values, c.Name+"="+c.Value)
	}
	return strings.Join(values, "; ")
}
func authFreeAddress(t *testing.T) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	s := l.Addr().String()
	_ = l.Close()
	return s
}
func authAwait(t *testing.T, ctx context.Context, check func() bool, name string, monitors ...func() error) {
	t.Helper()
	timer := time.NewTicker(50 * time.Millisecond)
	defer timer.Stop()
	for {
		for _, monitor := range monitors {
			if err := monitor(); err != nil {
				t.Fatalf("waiting for %s: %v", name, err)
			}
		}
		if check() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timeout waiting for %s", name)
		case <-timer.C:
		}
	}
}

type authFixtureLogs struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *authFixtureLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *authFixtureLogs) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }
func authStartProcess(t *testing.T, ctx context.Context, path string, env map[string]string, logs io.Writer) *authFixtureProcess {
	t.Helper()
	cmd := exec.CommandContext(ctx, path)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	process := &authFixtureProcess{Cmd: cmd, done: make(chan struct{})}
	go func() { process.err = cmd.Wait(); close(process.done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-process.done })
	return process
}

type authFixturePKI struct {
	ca     *x509.Certificate
	key    *ecdsa.PrivateKey
	caPath string
	roots  *x509.CertPool
	dir    string
}

func newAuthFixturePKI(t *testing.T) *authFixturePKI {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ephemeral fixture CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	raw, e := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	ca, e := x509.ParseCertificate(raw)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.pem")
	if e = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}), 0600); e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	return &authFixturePKI{ca, key, path, roots, dir}
}
func (p *authFixturePKI) issue(t *testing.T, role string) (string, string) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	uri, _ := url.Parse("spiffe://marketmesh.test/test/" + role)
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if e != nil {
		t.Fatal(e)
	}
	tmpl := &x509.Certificate{SerialNumber: serial, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), URIs: []*url.URL{uri}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	raw, e := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &key.PublicKey, p.key)
	if e != nil {
		t.Fatal(e)
	}
	pk, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	certPath, keyPath := filepath.Join(p.dir, role+".pem"), filepath.Join(p.dir, role+".key")
	for path, block := range map[string]*pem.Block{certPath: {Type: "CERTIFICATE", Bytes: raw}, keyPath: {Type: "PRIVATE KEY", Bytes: pk}} {
		if e = os.WriteFile(path, pem.EncodeToMemory(block), 0600); e != nil {
			t.Fatal(e)
		}
	}
	return certPath, keyPath
}

func authFixtureBinary(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func callBrowserProbe(t *testing.T, ctx context.Context, origin, certificatePath string) {
	t.Helper()
	node := os.Getenv("AUTH_BROWSER_NODE_BIN")
	if node == "" {
		t.Log("Chrome probe not configured; HTTPS cookie-jar integration only")
		return
	}
	chrome := os.Getenv("AUTH_BROWSER_CHROME_BIN")
	if chrome == "" {
		t.Fatal("AUTH_BROWSER_CHROME_BIN required with Node probe")
	}
	raw, err := os.ReadFile(certificatePath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("invalid public certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	input, err := json.Marshal(map[string]string{"origin": origin, "spki": base64.StdEncoding.EncodeToString(digest[:]), "chrome": chrome, "profile": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, node, "testdata/auth/browser.mjs")
	cmd.Stdin = bytes.NewReader(input)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		safe := regexp.MustCompile(`^Browser Auth check failed: (configuration|browser startup|HTTPS navigation|registration|login|refresh|logout|logout all)( timeout)?$`).FindString(strings.TrimSpace(output.String()))
		if safe != "" {
			t.Fatal(safe)
		}
		t.Fatal("real Chrome browser probe failed (output suppressed to protect credentials)")
	}
	if strings.TrimSpace(output.String()) != "Browser Auth HTTPS checks passed" {
		t.Fatal("real Chrome probe did not report PASS")
	}
	t.Log("real Chrome browser probe PASS")
}

type authFixtureProcess struct {
	*exec.Cmd
	done chan struct{}
	err  error
}

func (p *authFixtureProcess) check() error {
	select {
	case <-p.done:
		return fmt.Errorf("%s exited: %v (private output withheld)", filepath.Base(p.Path), p.err)
	default:
		return nil
	}
}

// authRestartProcess preserves the same immutable fixture configuration and keys.
func authRestartProcess(t *testing.T, ctx context.Context, stopped *authFixtureProcess, logs io.Writer) *authFixtureProcess {
	t.Helper()
	env := make(map[string]string)
	for _, entry := range stopped.Env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key != "PATH" {
			env[key] = value
		}
	}
	return authStartProcess(t, ctx, stopped.Path, env, logs)
}
