package fixture

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnalyticsForwardsOnlyAllowedPayloadAndHeaders(t *testing.T) {
	var received string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		received = string(raw)
		for _, header := range []string{"Cookie", "Authorization", "Referer", "Origin", "X-Forwarded-For"} {
			if r.Header.Get(header) != "" {
				t.Errorf("private header forwarded: %s", header)
			}
		}
		w.Header().Set("Set-Cookie", "secret=upstream")
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	handler := analyticsHandler("42", upstream.Client(), upstream.URL)
	r := httptest.NewRequest("POST", "/analytics/track", strings.NewReader(`{"type":"pageview","pathname":"/account"}`))
	for _, header := range []string{"Cookie", "Authorization", "Referer", "Origin", "X-Forwarded-For"} {
		r.Header.Set(header, "private-canary")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 204 || w.Header().Get("Set-Cookie") != "" || strings.Contains(received, "private-canary") || !strings.Contains(received, `"site_id":"42"`) {
		t.Fatalf("unsafe response or payload: status %d", w.Code)
	}
}

func TestAnalyticsRejectsSensitiveAndUnsupportedInput(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"query_in_path", `{"type":"pageview","pathname":"/login?password=private"}`},
		{"unknown_field", `{"type":"pageview","pathname":"/login","password":"private"}`},
		{"unknown_event", `{"type":"custom_event","pathname":"/login","event_name":"private"}`},
		{"wrong_event_page", `{"type":"custom_event","pathname":"/account","event_name":"login_succeeded"}`},
		{"replay", `{"type":"session_replay","pathname":"/account"}`},
		{"trailing_json", `{"type":"pageview","pathname":"/account"}{}`},
		{"oversized", strings.Repeat(" ", 1025) + `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := analyticsHandler("1", &http.Client{}, "http://127.0.0.1:1")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("POST", "/analytics/track", strings.NewReader(tc.body)))
			if w.Code != 400 {
				t.Fatalf("status %d", w.Code)
			}
		})
	}
}

func TestAnalyticsUnavailableDoesNotExposeUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "private upstream diagnostic", 500) }))
	defer upstream.Close()
	handler := analyticsHandler("1", upstream.Client(), upstream.URL)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("POST", "/analytics/track", strings.NewReader(`{"type":"custom_event","pathname":"/login","event_name":"login_succeeded"}`)))
	if w.Code != 503 || w.Body.Len() != 0 {
		t.Fatal("upstream diagnostic exposed")
	}
}
