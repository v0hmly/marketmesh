// Package httpapi exposes only staff session and invite capabilities on the
// corporate origin. Network reachability is restricted by the serving topology.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"connectrpc.com/connect"
	staffv1 "github.com/v0hmly/marketmesh/api/gen/go/staff/v1"
	"github.com/v0hmly/marketmesh/api/gen/go/staff/v1/staffv1connect"
	"github.com/v0hmly/marketmesh/services/staff/internal/application"
)

const sessionCookie = "__Host-mm-staff-session"
const loginCookie = "__Host-mm-staff-login"

type Handler struct {
	service            *application.Service
	origin, host, site string
	lifetime           time.Duration
	logger             *slog.Logger
}

func New(service *application.Service, origin, site string, lifetime time.Duration, logger *slog.Logger, networks []netip.Prefix) http.Handler {
	u, _ := url.Parse(origin)
	h := &Handler{service: service, origin: origin, host: u.Host, site: site, lifetime: lifetime, logger: logger}
	mux := http.NewServeMux()
	route, rpc := staffv1connect.NewStaffServiceHandler(h, connect.WithReadMaxBytes(16<<10))
	mux.Handle("POST "+route, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Origin") != origin || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		rpc.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /sso/callback", h.callback)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /", h.static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		peer, parseErr := netip.ParseAddr(host)
		corporate := false
		if err == nil && parseErr == nil {
			for _, prefix := range networks {
				if prefix.Contains(peer.Unmap()) {
					corporate = true
					break
				}
			}
		}
		if !corporate {
			logger.WarnContext(r.Context(), "staff network denied", "peer", host)
			http.Error(w, "corporate network required", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if r.Host != h.host {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}
func cookie(header http.Header, name string) string {
	r := http.Request{Header: header}
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}
func sessionFrom(header http.Header) string { return cookie(header, sessionCookie) }
func setCookie(header http.Header, name, value string, maxAge int, sameSite http.SameSite) {
	header.Add("Set-Cookie", (&http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: sameSite, MaxAge: maxAge}).String())
}
func rpcError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, application.ErrUnauthenticated):
		return connect.NewError(connect.CodeUnauthenticated, errors.New("staff session expired"))
	case errors.Is(err, application.ErrInvalidLogin), errors.Is(err, application.ErrInvalidInvite):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("invalid request"))
	default:
		return connect.NewError(connect.CodeUnavailable, errors.New("staff service unavailable"))
	}
}
func (h *Handler) audit(ctx context.Context, operation, cookie string, err error, principal ...application.Principal) {
	// A token digest identifies the session without logging reusable credentials,
	// invite URLs, code, state, email or raw errors from IdP/database drivers.
	result := "ok"
	if err != nil {
		result = connect.CodeOf(rpcError(err)).String()
	}
	attrs := []any{"operation", operation, "result", result}
	if application.ValidToken(cookie) {
		attrs = append(attrs, "session_digest", application.Digest(cookie))
	}
	if len(principal) > 0 && principal[0].Subject != "" {
		attrs = append(attrs, "issuer", principal[0].Issuer, "subject", principal[0].Subject)
	}
	h.logger.InfoContext(ctx, "staff access", attrs...)
}
func (h *Handler) StartSso(ctx context.Context, req *connect.Request[staffv1.StartSsoRequest]) (*connect.Response[staffv1.StartSsoResponse], error) {
	authorize, browser, err := h.service.Start(ctx, req.Msg.InviteToken, sessionFrom(req.Header()))
	h.audit(ctx, "start_sso", "", err)
	if err != nil {
		return nil, rpcError(err)
	}
	response := connect.NewResponse(&staffv1.StartSsoResponse{AuthorizeUrl: authorize})
	setCookie(response.Header(), loginCookie, browser, 600, http.SameSiteLaxMode)
	return response, nil
}
func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query()
	state, code := raw.Get("state"), raw.Get("code")
	if len(raw["state"]) != 1 || len(raw["code"]) != 1 || raw.Get("error") != "" {
		http.Error(w, "invalid login", http.StatusBadRequest)
		return
	}
	session, invite, err := h.service.Complete(r.Context(), state, cookie(r.Header, loginCookie), code)
	setCookie(w.Header(), loginCookie, "", -1, http.SameSiteLaxMode)
	h.audit(r.Context(), "sso_callback", session, err)
	if err != nil {
		http.Redirect(w, r, "/staff/login?failed=1", http.StatusSeeOther)
		return
	}

	setCookie(w.Header(), sessionCookie, session, int(h.lifetime.Seconds()), http.SameSiteStrictMode)
	target := "/staff"
	if invite != "" {
		target = "/staff/invite#token=" + url.QueryEscape(invite)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
func (h *Handler) GetSession(ctx context.Context, req *connect.Request[staffv1.GetSessionRequest]) (*connect.Response[staffv1.GetSessionResponse], error) {
	c := sessionFrom(req.Header())
	s, err := h.service.Session(ctx, c)
	h.audit(ctx, "get_session", c, err, s.Principal)
	if err != nil {
		return nil, rpcError(err)
	}
	return connect.NewResponse(&staffv1.GetSessionResponse{Email: s.Email, DisplayName: s.Name, Role: s.Role, ExpiresAtUnix: s.ExpiresAt.Unix()}), nil
}
func (h *Handler) Logout(ctx context.Context, req *connect.Request[staffv1.LogoutRequest]) (*connect.Response[staffv1.LogoutResponse], error) {
	c := sessionFrom(req.Header())
	err := h.service.Logout(ctx, c)
	h.audit(ctx, "logout", c, err)
	if err != nil {
		return nil, rpcError(err)
	}
	response := connect.NewResponse(&staffv1.LogoutResponse{})
	setCookie(response.Header(), sessionCookie, "", -1, http.SameSiteStrictMode)
	return response, nil
}
func (h *Handler) GetInvite(ctx context.Context, req *connect.Request[staffv1.GetInviteRequest]) (*connect.Response[staffv1.GetInviteResponse], error) {
	c := sessionFrom(req.Header())
	v, err := h.service.Invite(ctx, c, req.Msg.InviteToken, false)
	h.audit(ctx, "get_invite", c, err, v.Principal)
	if err != nil {
		return nil, rpcError(err)
	}
	states := map[string]staffv1.InviteState{"active": staffv1.InviteState_INVITE_STATE_ACTIVE, "expired": staffv1.InviteState_INVITE_STATE_EXPIRED, "used": staffv1.InviteState_INVITE_STATE_USED, "wrong_account": staffv1.InviteState_INVITE_STATE_WRONG_ACCOUNT}
	return connect.NewResponse(&staffv1.GetInviteResponse{State: states[v.State], Role: v.Role, InviterName: v.Inviter}), nil
}
func (h *Handler) AcceptInvite(ctx context.Context, req *connect.Request[staffv1.AcceptInviteRequest]) (*connect.Response[staffv1.AcceptInviteResponse], error) {
	c := sessionFrom(req.Header())
	v, err := h.service.Invite(ctx, c, req.Msg.InviteToken, true)
	if err == nil && v.State != "active" {
		err = application.ErrInvalidInvite
	}
	h.audit(ctx, "accept_invite", c, err, v.Principal)
	if err != nil {
		return nil, rpcError(err)
	}
	return connect.NewResponse(&staffv1.AcceptInviteResponse{}), nil
}
func (h *Handler) static(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/", "/staff", "/staff/login", "/staff/invite":
		http.ServeFile(w, r, h.site+"/index.html")
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/assets/") || path.Clean(r.URL.Path) != r.URL.Path || strings.Contains(r.URL.Path, "\\") {
		http.NotFound(w, r)
		return
	}
	// Vite content-hashed assets may be cached; HTML and APIs never are.
	file := h.site + r.URL.Path
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, file)
}

var _ staffv1connect.StaffServiceHandler = (*Handler)(nil)
