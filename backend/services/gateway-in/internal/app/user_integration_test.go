//go:build integration && authbrowserintegration

package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/v0hmly/marketmesh/api/gen/go/e2e/v1/e2ev1connect"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"github.com/v0hmly/marketmesh/api/gen/go/user/v1/userv1connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
)

func startBrowserUserFixture(t *testing.T, ctx context.Context, admin *pgxpool.Pool, pki *authFixturePKI, certificate, key, authAddress string, logs *authFixtureLogs) (*pgxpool.Pool, *authFixtureProcess, string) {
	t.Helper()
	for _, sql := range []string{
		"CREATE ROLE browser_user_rw LOGIN PASSWORD 'browser_user_rw_password'",
		"CREATE ROLE browser_user_ro LOGIN PASSWORD 'browser_user_ro_password'",
		"CREATE DATABASE browser_user OWNER browser_user_rw",
	} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatal("creating isolated User database fixture:", err)
		}
	}
	dsn := func(raw, role, password string) string {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		u.Path = "/browser_user"
		u.User = url.UserPassword(role, password)
		return u.String()
	}
	adminURL, err := url.Parse(os.Getenv("MARKETMESH_AUTH_POSTGRES_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	adminURL.Path = "/browser_user"
	userDB, err := pgxpool.New(ctx, adminURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		userDB.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cleanupAdmin, err := pgxpool.New(cleanupCtx, os.Getenv("MARKETMESH_AUTH_POSTGRES_DSN"))
		if err != nil {
			t.Error("opening fixture cleanup database:", err)
			return
		}
		defer cleanupAdmin.Close()
		for _, sql := range []string{"DROP DATABASE browser_user WITH (FORCE)", "DROP ROLE browser_user_rw", "DROP ROLE browser_user_ro"} {
			if _, err := cleanupAdmin.Exec(cleanupCtx, sql); err != nil {
				t.Error("cleaning isolated User fixture:", err)
			}
		}
	})
	raw, err := os.ReadFile(filepath.Join("../../../user/migrations", "000001_profiles.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = userDB.Exec(ctx, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err = userDB.Exec(ctx, "GRANT USAGE ON SCHEMA users TO browser_user_rw,browser_user_ro; GRANT SELECT,INSERT,UPDATE ON ALL TABLES IN SCHEMA users TO browser_user_rw; GRANT SELECT ON ALL TABLES IN SCHEMA users TO browser_user_ro"); err != nil {
		t.Fatal(err)
	}
	rw := dsn(os.Getenv("MARKETMESH_AUTH_POSTGRES_DSN"), "browser_user_rw", "browser_user_rw_password")
	ro := dsn(os.Getenv("MARKETMESH_AUTH_POSTGRES_RO_DSN"), "browser_user_ro", "browser_user_ro_password")
	replica, err := pgxpool.New(ctx, ro)
	if err != nil {
		t.Fatal(err)
	}
	defer replica.Close()
	authAwait(t, ctx, func() bool {
		var n int
		return replica.QueryRow(ctx, "SELECT count(*) FROM users.profiles").Scan(&n) == nil
	}, "User replica schema")
	address, httpAddress := authFreeAddress(t), authFreeAddress(t)
	process := authStartProcess(t, ctx, authFixtureBinary("USER_BROWSER_USER_BIN", "/usr/local/bin/user"), map[string]string{
		"SERVICE_VERSION": "test", "ENVIRONMENT": "test", "SERVICE_INSTANCE_ID": "browser-user-fixture", "HTTP_ADDRESS": httpAddress, "SHUTDOWN_TIMEOUT": "2s",
		"USER_PROFILE_ENABLED": "true", "USER_GRPC_ADDRESS": address, "USER_TRUST_DOMAIN": "marketmesh.test",
		"USER_TLS_CERT_FILE": certificate, "USER_TLS_KEY_FILE": key, "USER_TLS_CLIENT_CA_FILE": pki.caPath,
		"USER_AUTH_TARGET": authAddress, "USER_AUTH_SERVER_NAME": "localhost", "USER_AUTH_CA_FILE": pki.caPath, "USER_AUTH_ISSUER": "auth.marketmesh",
		"POSTGRES_RW_DSN": rw, "POSTGRES_RO_DSN": ro,
	}, logs)
	authAwait(t, ctx, func() bool {
		res, e := http.Get("http://" + httpAddress + "/readyz")
		if e != nil {
			return false
		}
		defer res.Body.Close()
		return res.StatusCode == http.StatusNoContent
	}, "User ready", process.check)
	return userDB, process, address
}

type browserUserChecks struct {
	beforeOutage func()
	outage       func(string)
	healthy      func() bool
}

func exerciseBrowserUser(t *testing.T, ctx context.Context, origin string, first, bare *http.Client, authDB, userDB *pgxpool.Pool, logs *authFixtureLogs) browserUserChecks {
	t.Helper()
	client := userv1connect.NewUserServiceClient(first, origin)
	anonymous := userv1connect.NewUserServiceClient(bare, origin)
	headers := func(header http.Header) { header.Set("Origin", origin); header.Set("Sec-Fetch-Site", "same-origin") }
	get := func(c userv1connect.UserServiceClient, modify func(http.Header)) (*connect.Response[userv1.GetMeResponse], error) {
		r := connect.NewRequest(new(userv1.GetMeRequest))
		headers(r.Header())
		if modify != nil {
			modify(r.Header())
		}
		res, err := c.GetMe(ctx, r)
		if err != nil {
			var ce *connect.Error
			if !errors.As(err, &ce) || ce.Meta().Get("Cache-Control") != "no-store" {
				t.Fatal("User error missing no-store")
			}
		} else if res.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("User response missing no-store")
		}
		return res, err
	}
	update := func(body *userv1.UpdateMeRequest) (*connect.Response[userv1.UpdateMeResponse], error) {
		r := connect.NewRequest(body)
		headers(r.Header())
		res, err := client.UpdateMe(ctx, r)
		if err == nil && res.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("UpdateMe missing no-store")
		}
		if err != nil {
			var ce *connect.Error
			if !errors.As(err, &ce) || ce.Meta().Get("Cache-Control") != "no-store" {
				t.Fatal("UpdateMe error missing no-store")
			}
		}
		return res, err
	}
	_, err := get(client, nil)
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unprovisioned profile: %v", connect.CodeOf(err))
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatal("missing Connect error")
	}
	detailFound := false
	for _, detail := range ce.Details() {
		value, e := detail.Value()
		if e == nil {
			if info, ok := value.(*errdetails.ErrorInfo); ok && info.GetDomain() == "marketmesh.user" && info.GetReason() == "PROFILE_NOT_READY" && len(info.GetMetadata()) == 0 {
				detailFound = true
			}
		}
	}
	if !detailFound {
		t.Fatal("PROFILE_NOT_READY detail lost")
	}
	var subject []byte
	if err := authDB.QueryRow(ctx, "SELECT subject_id FROM auth.credentials WHERE identifier=$1", "browser-fixture@example.test").Scan(&subject); err != nil {
		t.Fatal(err)
	}
	// Step 06 explicitly seeds provisioning only after proving PROFILE_NOT_READY;
	// the real Auth registration event/consumer path is covered separately.
	if _, err := userDB.Exec(ctx, "INSERT INTO users.profiles(subject_id) VALUES($1)", subject); err != nil {
		t.Fatal(err)
	}
	profile, err := get(client, nil)
	if err != nil || !bytes.Equal(profile.Msg.GetProfile().GetSubjectId(), subject) {
		t.Fatal("GetMe did not resolve the cookie subject")
	}
	display, bio := "private-user-display-marker-924", "private-user-biography-marker-582"
	changed, err := update(&userv1.UpdateMeRequest{DisplayName: display, Bio: bio, ExpectedVersion: 1})
	if err != nil || changed.Msg.GetProfile().GetVersion() != 2 || changed.Msg.GetProfile().GetDisplayName() != display {
		t.Fatalf("real UpdateMe failed: %v", connect.CodeOf(err))
	}
	profile, err = get(client, nil)
	if err != nil || profile.Msg.GetProfile().GetVersion() != 2 || profile.Msg.GetProfile().GetBio() != bio {
		t.Fatal("read-after-write failed")
	}
	if _, err = update(&userv1.UpdateMeRequest{DisplayName: "stale write", ExpectedVersion: 1}); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatal("stale version was not Aborted")
	}
	if _, err = update(&userv1.UpdateMeRequest{DisplayName: strings.Repeat("x", 81), ExpectedVersion: 2}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatal("invalid profile accepted")
	}
	for _, modify := range []func(http.Header){func(h http.Header) { h.Del("Origin") }, func(h http.Header) { h.Add("Origin", origin) }, func(h http.Header) { h.Set("Origin", "https://attacker.test") }, func(h http.Header) { h.Set("Sec-Fetch-Site", "cross-site") }} {
		if _, err := get(client, modify); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatal("unsafe browser origin accepted")
		}
		denied := connect.NewRequest(&userv1.UpdateMeRequest{DisplayName: "must not commit", ExpectedVersion: 2})
		headers(denied.Header())
		modify(denied.Header())
		if _, err := client.UpdateMe(ctx, denied); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatal("unsafe mutation origin accepted")
		}

	}
	if _, err := get(anonymous, nil); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatal("missing cookie accepted")
	}
	originURL, _ := url.Parse(origin)
	cookie := authCookieHeader(first.Jar.Cookies(originURL))
	if _, err := get(anonymous, func(h http.Header) { h.Set("Cookie", cookie+"; "+cookie) }); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatal("duplicate access cookie accepted")
	}
	// A second real account proves the public caller cannot select another subject.
	jar, _ := cookiejar.New(nil)
	second := &http.Client{Transport: bare.Transport, Jar: jar, Timeout: 5 * time.Second}
	credentialMarker := "different-account-password-marker-635!"
	credentialBody, _ := json.Marshal(map[string]string{"identifier": "other-user-fixture@example.test", "password": base64.StdEncoding.EncodeToString([]byte(credentialMarker))})
	authCall := func(method string, body []byte) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/auth.v1.AuthService/"+method, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
		headers(req.Header)
		res, e := second.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("second account %s: %d", method, res.StatusCode)
		}
	}
	authCall("RegisterCredentials", credentialBody)
	authCall("Login", credentialBody)
	var secondSubject []byte
	if err := authDB.QueryRow(ctx, "SELECT subject_id FROM auth.credentials WHERE identifier=$1", "other-user-fixture@example.test").Scan(&secondSubject); err != nil {
		t.Fatal(err)
	}
	if _, err := userDB.Exec(ctx, "INSERT INTO users.profiles(subject_id,display_name) VALUES($1,$2)", secondSubject, "second-private-profile"); err != nil {
		t.Fatal(err)
	}
	secondClient := userv1connect.NewUserServiceClient(second, origin)
	secondProfile, err := get(secondClient, func(h http.Header) {
		h.Set("X-Subject-Id", base64.StdEncoding.EncodeToString(subject))
		h.Set("Authorization", "Bearer forged.assertion.value")
		h.Set("Marketmesh-Session-Assertion-Bin", base64.StdEncoding.EncodeToString([]byte("forged.assertion.value")))
	})
	if err != nil || !bytes.Equal(secondProfile.Msg.GetProfile().GetSubjectId(), secondSubject) || secondProfile.Msg.GetProfile().GetBio() == bio {
		t.Fatal("forged identity headers selected another profile")
	}
	// Unknown identity in the public body may be rejected or ignored, never used.
	injection, _ := json.Marshal(map[string]string{"subjectId": base64.StdEncoding.EncodeToString(subject)})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, origin+userv1connect.UserServiceGetMeProcedure, bytes.NewReader(injection))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	headers(req.Header)
	res, e := second.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.Header.Get("Cache-Control") != "no-store" || bytes.Contains(raw, []byte(bio)) || (res.StatusCode != 200 && res.StatusCode != 400) {
		t.Fatal("unsafe identity body handling")
	}
	secondCookie := authCookieHeader(second.Jar.Cookies(originURL))
	authCall("Logout", []byte(`{}`))
	if _, err := get(anonymous, func(h http.Header) { h.Set("Cookie", secondCookie) }); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatal("revoked cookie accepted")
	}
	for _, path := range []string{"/gateway.v1.UserBrowserService/BrowserGetMe", "/auth.v1.AuthInternalService/ExchangeBrowserSession", "/auth.v1.AuthInternalService/ExchangeSession", e2ev1connect.FakeInternalServiceReadProcedure} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, origin+path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		res, e := bare.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("private/fake route exposed: %s", path)
		}
	}
	credentialValues := []string{}
	for _, line := range []string{cookie, secondCookie} {
		parsed, err := http.ParseCookie(line)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range parsed {
			credentialValues = append(credentialValues, c.Value)
		}
	}
	t.Cleanup(func() {
		for _, marker := range append(credentialValues, []string{display, bio, "second-private-profile", "browser-fixture@example.test", "other-user-fixture@example.test", "forged.assertion.value", credentialMarker, cookie, secondCookie, base64.StdEncoding.EncodeToString(subject), hex.EncodeToString(subject)}...) {
			if marker != "" && strings.Contains(logs.String(), marker) {
				t.Error("credential or profile data leaked into logs")
			}
		}
	})
	return browserUserChecks{
		healthy: func() bool { _, err := get(client, nil); return err == nil },
		beforeOutage: func() {
			if _, err := get(client, nil); err != nil {
				t.Fatal("User unavailable before outage injection")
			}
		},
		outage: func(name string) {
			if _, err := get(client, nil); connect.CodeOf(err) != connect.CodeUnavailable {
				t.Fatalf("%s outage: got %v", name, connect.CodeOf(err))
			}
		},
	}
}
