package fixture

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestGenerateIsIdempotentAndSeparatesSecrets(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if err := Generate(root, "8443"); err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(root, "auth/keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = Generate(root, "8443"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "auth/keys.json"))
	if string(key) != string(after) {
		t.Fatal("keys rotated")
	}
	if err = Generate(root, "9443"); err == nil {
		t.Fatal("origin change accepted")
	}
	entries, _ := os.ReadDir(filepath.Join(root, "browser"))
	if len(entries) != 1 || entries[0].Name() != "ca.pem" {
		t.Fatal("browser received private fixture material")
	}
	for _, name := range []string{"auth", "user", "gateway-in", "gateway-out", "frontdoor", "nats", "provision"} {
		raw, _ := os.ReadFile(filepath.Join(root, name, "cert.pem"))
		block, _ := pem.Decode(raw)
		if block == nil {
			t.Fatal(name)
		}
		cert, e := x509.ParseCertificate(block.Bytes)
		if e != nil {
			t.Fatal(e)
		}
		if cert.PublicKeyAlgorithm != x509.ECDSA {
			t.Fatal("TLS certificates must be browser-compatible ECDSA")
		}
		if cert.NotAfter.After(time.Now().Add(lifetime)) {
			t.Fatal("excessive lifetime")
		}
		if e = cert.VerifyHostname(name); e != nil {
			t.Fatal(e)
		}
		info, _ := os.Stat(filepath.Join(root, name, "key.pem"))
		if info.Mode().Perm() != 0600 {
			t.Fatal("key permissions")
		}
		if name == "frontdoor" {
			for _, host := range []string{"localhost", "127.0.0.1"} {
				if e = cert.VerifyHostname(host); e != nil {
					t.Fatal(e)
				}
			}
		}
	}
	if _, err = os.Stat(filepath.Join(root, "ca-key.pem")); !os.IsNotExist(err) {
		t.Fatal("CA private key persisted")
	}
	env, err := os.ReadFile(filepath.Join(root, "gateway-out/env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "ENVIRONMENT='test'\n") || !strings.Contains(string(env), "TUNNEL_PERIODIC_REDISCOVERY_ENABLED='false'\n") {
		t.Fatal("fixed test topology must disable periodic redistribution")
	}
}

func TestGenerateRejectsPartialAndInvalidInput(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "partial"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, port := range []string{"8443", "443", "invalid", "99999"} {
		if err := Generate(root, port); err == nil {
			t.Fatal("unsafe generation accepted", port)
		}
	}
	if err := Generate("relative", "8443"); err == nil {
		t.Fatal("relative state accepted")
	}
}

func TestFrontdoorRoutesAndBrowserHeaders(t *testing.T) {
	count := 0
	proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Header.Get("Origin") != "https://localhost:8443" {
			t.Error("origin lost")
		}
		w.WriteHeader(401)
	})
	handler := frontdoorHandler(fstest.MapFS{"index.html": {Data: []byte("fixture SPA")}, "assets/app.js": {Data: []byte("fixture JS")}}, proxy)
	for _, tc := range []struct {
		path, method string
		want         int
		body         string
	}{
		{"/account", "GET", 200, "fixture SPA"}, {"/account/addresses", "GET", 200, "fixture SPA"}, {"/account/unknown", "GET", 404, ""}, {"/register", "GET", 200, "fixture SPA"}, {"/assets/app.js", "GET", 200, "fixture JS"}, {"/robots.txt", "GET", 200, "Disallow"},
		{"/user.v1.UserService/ListAddresses", "POST", 401, ""},
		{"/user.v1.UserService/CreateAddress", "POST", 401, ""},
		{"/user.v1.UserService/UpdateAddress", "POST", 401, ""},
		{"/user.v1.UserService/DeleteAddress", "POST", 401, ""},
		{"/user.v1.UserService/SetDefaultAddress", "POST", 401, ""},
		{"/auth.v1.AuthService/Login", "POST", 401, ""},
		{"/auth.v1.AuthService/RegisterCredentials", "POST", 401, ""},
		{"/auth.v1.AuthService/RefreshSession", "POST", 401, ""}, {"/user.v1.UserService/GetMe", "GET", 405, ""}, {"/gateway.v1.UserBrowserService/BrowserGetMe", "POST", 404, ""}, {"/auth.v1.AuthInternalService/ExchangeBrowserSession", "GET", 404, ""}, {"/missing.RPC/Call", "GET", 404, ""}, {"/assets/../secret", "GET", 404, ""},
	} {
		t.Run(tc.path+tc.method, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header.Set("Origin", "https://localhost:8443")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d", w.Code)
			}
			if tc.body != "" && !strings.Contains(w.Body.String(), tc.body) {
				t.Fatal("missing content")
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
				t.Fatal("browser headers missing")
			}
		})
	}
	if count != 8 {
		t.Fatal("private request forwarded", count)
	}
}
