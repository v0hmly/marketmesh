package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/services/staff/internal/application"
)

type store struct{}

func (store) SaveLogin(context.Context, application.Login) error { return nil }
func (store) ConsumeLogin(context.Context, string, string, time.Time) (application.Login, error) {
	return application.Login{}, application.ErrInvalidLogin
}
func (store) SaveSession(context.Context, string, application.Principal, time.Time, time.Time) error {
	return nil
}
func (store) Session(context.Context, string, time.Time, time.Time) (application.Session, error) {
	return application.Session{}, application.ErrUnauthenticated
}
func (store) DeleteSession(context.Context, string) error { return nil }
func (store) Invite(context.Context, string, application.Principal, time.Time, bool) (application.Invite, error) {
	return application.Invite{}, application.ErrInvalidInvite
}

type provider struct{}

func (provider) Authorize(string, string, string) string { return "https://idp.test/authorize" }
func (provider) Exchange(context.Context, string, string, string) (application.Principal, error) {
	return application.Principal{}, application.ErrInvalidLogin
}
func TestServingBoundaryAndCookie(t *testing.T) {
	handler := New(application.New(store{}, provider{}, time.Minute, time.Hour), "https://staff.test", t.TempDir(), time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)), []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")})
	call := func(host, origin, method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://"+host+path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		req.Header.Set("X-Staff-Subject", "admin")
		req.Header.Set("Cookie", "__Host-mm-staff-session=invalid; access_token=buyer-token")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	for _, tc := range []struct {
		host, origin, method, path string
		status                     int
	}{
		{"public.test", "https://staff.test", "POST", "/staff.v1.StaffService/StartSso", 403},
		{"staff.test", "https://buyer.test", "POST", "/staff.v1.StaffService/StartSso", 403},
		{"staff.test", "", "POST", "/staff.v1.StaffService/StartSso", 403},
		{"staff.test", "https://staff.test", "GET", "/staff.v1.StaffService/StartSso", 404},
		{"staff.test", "https://staff.test", "POST", "/staff.v1.StaffService/GetSession", 401},
		{"staff.test", "https://staff.test", "POST", "/staff.v1.StaffService/GetInvite", 400},
		{"staff.test", "https://staff.test", "GET", "/sso/callback?state=forged&code=code", 303},
	} {
		t.Run(tc.host+tc.origin+tc.path, func(t *testing.T) {
			w := call(tc.host, tc.origin, tc.method, tc.path)
			if w.Code != tc.status {
				t.Fatalf("got %d want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
				t.Fatal("missing serving policy")
			}
		})
	}
	for _, path := range []string{"/staff", "/assets/app.js", "/staff.v1.StaffService/StartSso"} {
		req := httptest.NewRequest("POST", "https://staff.test"+path, strings.NewReader("{}"))
		req.RemoteAddr = "198.51.100.10:12345"
		req.Header.Set("Origin", "https://staff.test")
		req.Header.Set("X-Forwarded-For", "192.0.2.1")
		req.Header.Set("X-Real-IP", "192.0.2.1")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatal("outside peer bypassed corporate network boundary")
		}
	}

	w := call("staff.test", "https://staff.test", "POST", "/staff.v1.StaffService/StartSso")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("login cookie missing")
	}
	c := cookies[0]
	if c.Name != loginCookie || !c.Secure || !c.HttpOnly || c.Domain != "" || c.Path != "/" || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 600 {
		t.Fatalf("unsafe login cookie: %+v", c)
	}
	h := http.Header{}
	setCookie(h, sessionCookie, "opaque", 3600, http.SameSiteStrictMode)
	resp := http.Response{Header: h}
	c = resp.Cookies()[0]
	if !c.Secure || !c.HttpOnly || c.Domain != "" || c.SameSite != http.SameSiteStrictMode || c.Name != sessionCookie {
		t.Fatal("unsafe staff session cookie")
	}
}
