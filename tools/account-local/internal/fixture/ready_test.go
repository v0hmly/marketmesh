package fixture

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadyRequiresBothAuthenticatedBoundaryAndBuiltSPA(t *testing.T) {
	for _, tc := range []struct {
		name              string
		rpc, static       int
		contentType, body string
		want              bool
	}{
		{"ready", 401, 200, "text/html; charset=utf-8", `<div id="app"></div>`, true},
		{"missing site", 401, 404, "text/html", "missing", false},
		{"wrong content", 401, 200, "text/plain", `id="app"`, false},
		{"wrong index", 401, 200, "text/html", "unrelated", false},
		{"broken tunnel", 503, 200, "text/html", `id="app"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/user.v1.UserService/GetMe" {
					w.WriteHeader(tc.rpc)
					return
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.static)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			if got := readyOnce(context.Background(), server.Client(), server.URL); got != tc.want {
				t.Fatalf("ready=%v", got)
			}
		})
	}
}
