package fixture

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"time"
)

var analyticsPaths = map[string]bool{"/login": true, "/register": true, "/account": true, "/account/addresses": true, "/account/settings": true}

// analyticsHandler exposes only local event ingestion, never the Rybbit admin
// API or arbitrary proxying. A fresh request excludes cookies and auth headers.
func analyticsHandler(site string, client *http.Client, target string) http.Handler {
	if !regexp.MustCompile(`^[1-9][0-9]{0,9}$`).MatchString(site) {
		return http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" || r.URL.RawQuery != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var event struct {
			Type      string `json:"type"`
			Pathname  string `json:"pathname"`
			EventName string `json:"event_name,omitempty"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&event) != nil || decoder.Decode(new(any)) != io.EOF || !analyticsPaths[event.Pathname] {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		valid := event.Type == "pageview" && event.EventName == "" || event.Type == "custom_event" && (event.EventName == "login_succeeded" && event.Pathname == "/login" || event.EventName == "registration_request_completed" && event.Pathname == "/register")
		if !valid {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		payload := map[string]string{"site_id": site, "type": event.Type, "pathname": event.Pathname, "hostname": "localhost", "querystring": "", "referrer": "", "page_title": ""}
		if event.EventName != "" {
			payload["event_name"] = event.EventName
		}
		raw, _ := json.Marshal(payload)
		request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(raw))
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("User-Agent", r.UserAgent())
		response, err := client.Do(request)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func localAnalyticsHandler(site string) http.Handler {
	return analyticsHandler(site, &http.Client{Timeout: time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, "http://backend:3001/api/track")
}
