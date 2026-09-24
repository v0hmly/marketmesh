// Package provider implements ONLY the local test IdP used by the dev fixture.
// It refuses non-local issuer/callback configuration and is never a production IdP.
package provider

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const Issuer = "https://oidc.localhost:18445"
const Callback = "https://staff.localhost:18444/sso/callback"

type User struct{ Subject, Email, Name, Password string }
type Config struct {
	ClientID, ClientSecret, Cert, Key string
	Users                             []User
}
type request struct {
	State, Nonce, Challenge, Binding string
	Expires                          time.Time
	User                             User
}
type Provider struct {
	cfg             Config
	signer          jose.Signer
	jwk             jose.JSONWebKey
	mu              sync.Mutex
	requests, codes map[string]request
}

func random() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func hash(value string) string {
	v := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(v[:])
}
func New(cfg Config) (*Provider, error) {
	if cfg.ClientID == "" || len(cfg.ClientSecret) < 32 || len(cfg.Users) < 1 {
		return nil, errors.New("invalid test IdP configuration")
	}
	for _, user := range cfg.Users {
		if user.Subject == "" || user.Email == "" || len(user.Password) < 32 {
			return nil, errors.New("invalid test identity")
		}
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "local"))
	if err != nil {
		return nil, err
	}
	return &Provider{cfg: cfg, signer: signer, jwk: jose.JSONWebKey{Key: &key.PublicKey, KeyID: "local", Algorithm: "RS256", Use: "sig"}, requests: make(map[string]request), codes: make(map[string]request)}, nil
}
func (p *Provider) prune() {
	for _, m := range []map[string]request{p.requests, p.codes} {
		for key, value := range m {
			if time.Now().After(value.Expires) {
				delete(m, key)
			}
		}
	}
}
func (p *Provider) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"issuer": Issuer, "authorization_endpoint": Issuer + "/authorize", "token_endpoint": Issuer + "/token", "jwks_uri": Issuer + "/keys", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"client_secret_basic"}})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{p.jwk}})
	})
	mux.HandleFunc("GET /authorize", p.authorize)
	mux.HandleFunc("POST /login", p.login)
	mux.HandleFunc("POST /token", p.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		// HTML form POST needs its Origin header; suppress path/query without turning Origin into null.
		w.Header().Set("Referrer-Policy", "strict-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self' https://staff.localhost:18444/sso/callback; frame-ancestors 'none'; base-uri 'none'")
		if r.Host != "oidc.localhost:18445" {
			http.Error(w, "local fixture only", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		mux.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != p.cfg.ClientID || q.Get("redirect_uri") != Callback || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 || len(q.Get("state")) != 43 || len(q.Get("nonce")) != 43 {
		http.Error(w, "invalid request", 400)
		return
	}
	id, binding := random(), random()
	p.mu.Lock()
	p.prune()
	if len(p.requests)+len(p.codes) >= 1000 {
		p.mu.Unlock()
		http.Error(w, "too many requests", 429)
		return
	}
	p.requests[id] = request{State: q.Get("state"), Nonce: q.Get("nonce"), Challenge: q.Get("code_challenge"), Binding: hash(binding), Expires: time.Now().Add(5 * time.Minute)}
	p.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "__Host-mm-test-oidc", Value: binding, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 300})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = loginForm.Execute(w, id)
}

var loginForm = template.Must(template.New("login").Parse(`<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Тестовый OIDC — MarketMesh</title><main><h1>Локальный тестовый OIDC</h1><p>Учётные данные: .cache/dev-stack/staff/credentials.json</p><form action="/login" method="post"><input type="hidden" name="request" value="{{.}}"><p><label>Рабочая почта <input name="email" type="email" autocomplete="username" required></label></p><p><label>Пароль <input name="password" type="password" autocomplete="current-password" required></label></p><button>Войти</button></form></main></html>`))

func (p *Provider) login(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != Issuer || r.ParseForm() != nil {
		http.Error(w, "forbidden", 403)
		return
	}
	binding, err := r.Cookie("__Host-mm-test-oidc")
	if err != nil {
		http.Error(w, "invalid login", 400)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	id := r.PostForm.Get("request")
	req, ok := p.requests[id]
	if !ok || !equal(req.Binding, hash(binding.Value)) {
		http.Error(w, "invalid login", 400)
		return
	}
	delete(p.requests, id)
	var identity *User
	for i := range p.cfg.Users {
		user := &p.cfg.Users[i]
		if equal(user.Email, r.PostForm.Get("email")) && equal(user.Password, r.PostForm.Get("password")) {
			identity = user
		}
	}
	if identity == nil {
		http.Error(w, "invalid credentials; restart sign-in", 401)
		return
	}
	req.User = *identity
	req.Expires = time.Now().Add(time.Minute)
	code := random()
	p.codes[hash(code)] = req
	target := Callback + "?" + url.Values{"code": {code}, "state": {req.State}}.Encode()
	http.Redirect(w, r, target, http.StatusSeeOther)
}
func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	client, secret, ok := r.BasicAuth()
	if !ok || !equal(client, p.cfg.ClientID) || !equal(secret, p.cfg.ClientSecret) {
		http.Error(w, "invalid client", 401)
		return
	}
	if r.ParseForm() != nil || r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("redirect_uri") != Callback {
		http.Error(w, "invalid grant", 400)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	key := hash(r.PostForm.Get("code"))
	req, ok := p.codes[key]
	delete(p.codes, key)
	if !ok || !equal(req.Challenge, hash(r.PostForm.Get("code_verifier"))) {
		http.Error(w, "invalid grant", 400)
		return
	}
	now := time.Now()
	raw, err := jwt.Signed(p.signer).Claims(jwt.Claims{Issuer: Issuer, Subject: req.User.Subject, Audience: jwt.Audience{p.cfg.ClientID}, IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(5 * time.Minute))}).Claims(map[string]any{"nonce": req.Nonce, "email": req.User.Email, "email_verified": true, "name": req.User.Name}).Serialize()
	if err != nil {
		http.Error(w, "signing failed", 500)
		return
	}
	writeJSON(w, map[string]any{"access_token": random(), "token_type": "Bearer", "expires_in": 300, "id_token": raw})
}
func Server(cfg Config, handler http.Handler) *http.Server {
	return &http.Server{Addr: ":18445", Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
}
