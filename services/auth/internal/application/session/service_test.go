package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
)

var errSecretBackend = errors.New("backend-password-must-not-leak")

type refreshState struct {
	id   domain.ID
	used bool
}

type fakeStore struct {
	mu                                                     sync.Mutex
	records                                                map[domain.ID]domain.Record
	refresh                                                map[domain.Digest]refreshState
	createErr, findErr, rotateErr, revokeErr, revokeAllErr error
	revokeReason                                           string
	revokeAllReason                                        string
}

func newFakeStore() *fakeStore {
	return &fakeStore{records: make(map[domain.ID]domain.Record), refresh: make(map[domain.Digest]refreshState)}
}

func (store *fakeStore) Create(_ context.Context, record domain.Record, digest domain.Digest) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.createErr != nil {
		return store.createErr
	}
	store.records[record.ID] = record
	store.refresh[digest] = refreshState{id: record.ID}
	return nil
}

func (store *fakeStore) Find(_ context.Context, id domain.ID) (domain.Record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.findErr != nil {
		return domain.Record{}, store.findErr
	}
	record, ok := store.records[id]
	if !ok {
		return domain.Record{}, domain.ErrInvalidSession
	}
	return record, nil
}

func (store *fakeStore) Rotate(_ context.Context, id domain.ID, previous, next domain.Digest, now time.Time, accessTTL, idleTTL time.Duration) (domain.Record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.rotateErr != nil {
		return domain.Record{}, store.rotateErr
	}
	state, ok := store.refresh[previous]
	if !ok || state.id != id {
		return domain.Record{}, domain.ErrInvalidSession
	}
	record, ok := store.records[id]
	if !ok {
		return domain.Record{}, domain.ErrInvalidSession
	}
	if state.used {
		revokedAt := now
		record.RevokedAt = &revokedAt
		store.records[id] = record
		return domain.Record{}, domain.ErrRefreshReuse
	}
	if !record.Active(now) || !now.Before(record.RefreshExpiresAt) {
		return domain.Record{}, domain.ErrInvalidSession
	}
	state.used = true
	store.refresh[previous] = state
	record.Version++
	record.AccessExpiresAt = earlier(now.Add(accessTTL), record.ExpiresAt)
	record.RefreshExpiresAt = earlier(now.Add(idleTTL), record.ExpiresAt)
	store.records[id] = record
	store.refresh[next] = refreshState{id: id}
	return record, nil
}

func (store *fakeStore) Revoke(_ context.Context, id domain.ID, now time.Time, reason string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.revokeReason = reason
	if store.revokeErr != nil {
		return store.revokeErr
	}
	record, ok := store.records[id]
	if !ok {
		return domain.ErrInvalidSession
	}
	record.RevokedAt = &now
	store.records[id] = record
	return nil
}

func (store *fakeStore) RevokeAll(_ context.Context, subject credential.SubjectID, now time.Time, reason string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.revokeAllReason = reason
	if store.revokeAllErr != nil {
		return store.revokeAllErr
	}
	for id, record := range store.records {
		if record.SubjectID == subject {
			revokedAt := now
			record.RevokedAt = &revokedAt
			store.records[id] = record
		}
	}
	return nil
}

type fakeAccessStore struct {
	mu             sync.Mutex
	values         map[domain.ID]domain.Access
	putErr, getErr error
}

func newFakeAccessStore() *fakeAccessStore {
	return &fakeAccessStore{values: make(map[domain.ID]domain.Access)}
}
func (store *fakeAccessStore) Put(_ context.Context, record domain.Record, digest domain.Digest, _ time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.putErr != nil {
		return store.putErr
	}
	store.values[record.ID] = domain.Access{Digest: digest, Version: record.Version, ExpiresAt: record.AccessExpiresAt}
	return nil
}
func (store *fakeAccessStore) Get(_ context.Context, id domain.ID) (domain.Access, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.getErr != nil {
		return domain.Access{}, store.getErr
	}
	value, ok := store.values[id]
	if !ok {
		return domain.Access{}, domain.ErrInvalidSession
	}
	return value, nil
}

type fakeIssuer struct {
	token     string
	err       error
	expiresAt time.Time
	record    domain.Record
	audience  string
}

func (issuer *fakeIssuer) Issue(_ context.Context, record domain.Record, audience string, expiresAt time.Time) (string, error) {
	issuer.record, issuer.audience, issuer.expiresAt = record, audience, expiresAt
	if issuer.err != nil {
		return "", issuer.err
	}
	return issuer.token, nil
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

type serviceFixture struct {
	service *Service
	store   *fakeStore
	access  *fakeAccessStore
	issuer  *fakeIssuer
	now     time.Time
	subject credential.SubjectID
}

func newFixture(t *testing.T) serviceFixture {
	t.Helper()
	now := time.Date(2026, 9, 9, 10, 11, 12, 987654321, time.FixedZone("test", 3*60*60))
	var subject credential.SubjectID
	subject[0] = 7
	store, access, issuer := newFakeStore(), newFakeAccessStore(), &fakeIssuer{token: "assertion"}
	random := make([]byte, 4096)
	for i := range random {
		random[i] = byte(i%251 + 1)
	}
	service, err := New(store, access, issuer, Config{
		AccessTTL: 10 * time.Minute, IdleTTL: 24 * time.Hour, AbsoluteTTL: 30 * 24 * time.Hour,
		AssertionTTL: 5 * time.Minute, Clock: func() time.Time { return now }, Random: bytes.NewReader(random),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return serviceFixture{service: service, store: store, access: access, issuer: issuer, now: now.UTC().Truncate(time.Second), subject: subject}
}

func TestStartCreatesCanonicalCredentialsAndLifetimes(t *testing.T) {
	fx := newFixture(t)
	tokens, err := fx.service.Start(context.Background(), fx.subject)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if len(tokens.Access.Reveal()) != 80 || len(tokens.Refresh.Reveal()) != 80 || tokens.Access.Reveal() == tokens.Refresh.Reveal() {
		t.Fatal("Start() did not mint distinct canonical credentials")
	}
	record := tokens.Record
	if record.Version != 1 || record.SubjectID != fx.subject || record.CreatedAt != fx.now {
		t.Fatalf("unexpected record: %+v", record)
	}
	if !record.AccessExpiresAt.Equal(fx.now.Add(10*time.Minute)) || !record.RefreshExpiresAt.Equal(fx.now.Add(24*time.Hour)) || !record.ExpiresAt.Equal(fx.now.Add(30*24*time.Hour)) {
		t.Fatalf("unexpected expiries: %+v", record)
	}
	if tokens.Access.ID() != record.ID || tokens.Refresh.ID() != record.ID {
		t.Fatal("credential IDs do not match session")
	}
	if _, err := fx.service.Authenticate(context.Background(), tokens.Access.Reveal()); err != nil {
		t.Fatalf("Authenticate(access) error = %v", err)
	}
	if _, err := fx.service.Authenticate(context.Background(), tokens.Refresh.Reveal()); err != domain.ErrInvalidSession {
		t.Fatalf("Authenticate(refresh) error = %v", err)
	}
}

func TestRefreshRotatesCredentialsAndDetectsReplay(t *testing.T) {
	fx := newFixture(t)
	started, _ := fx.service.Start(context.Background(), fx.subject)
	refreshed, err := fx.service.Refresh(context.Background(), started.Refresh.Reveal())
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if refreshed.Record.Version != 2 || refreshed.Access.Reveal() == started.Access.Reveal() || refreshed.Refresh.Reveal() == started.Refresh.Reveal() {
		t.Fatal("Refresh() did not rotate credentials and version")
	}
	if refreshed.Access.ID() != started.Record.ID || refreshed.Refresh.ID() != started.Record.ID {
		t.Fatal("rotated credentials changed the session ID")
	}
	if _, err := fx.service.Authenticate(context.Background(), refreshed.Access.Reveal()); err != nil {
		t.Fatalf("Authenticate(rotated access) error = %v", err)
	}
	if _, err := fx.service.Authenticate(context.Background(), started.Access.Reveal()); err != domain.ErrInvalidSession {
		t.Fatalf("old access error = %v", err)
	}
	if _, err := fx.service.Refresh(context.Background(), started.Refresh.Reveal()); err != domain.ErrRefreshReuse {
		t.Fatalf("replayed refresh error = %v", err)
	}
	if _, err := fx.service.Authenticate(context.Background(), refreshed.Access.Reveal()); err != domain.ErrInvalidSession {
		t.Fatalf("access after family revocation error = %v", err)
	}
}

func TestAuthenticateRejectsWrongDigestAndStaleAccessVersion(t *testing.T) {
	fx := newFixture(t)
	tokens, _ := fx.service.Start(context.Background(), fx.subject)
	wrongBytes := make([]byte, 32)
	wrongBytes[0] = 99
	wrong, _ := domain.NewToken(tokens.Record.ID, wrongBytes)
	if _, err := fx.service.Authenticate(context.Background(), wrong.Reveal()); err != domain.ErrInvalidSession {
		t.Fatalf("wrong digest error = %v", err)
	}
	fx.access.mu.Lock()
	stale := fx.access.values[tokens.Record.ID]
	stale.Version--
	fx.access.values[tokens.Record.ID] = stale
	fx.access.mu.Unlock()
	if _, err := fx.service.Authenticate(context.Background(), tokens.Access.Reveal()); err != domain.ErrInvalidSession {
		t.Fatalf("stale access version error = %v", err)
	}
}

func TestRevokeSingleAndAll(t *testing.T) {
	fx := newFixture(t)
	one, _ := fx.service.Start(context.Background(), fx.subject)
	two, _ := fx.service.Start(context.Background(), fx.subject)
	if err := fx.service.Revoke(context.Background(), one.Access.Reveal()); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if fx.store.revokeReason != "logout" {
		t.Fatalf("revoke reason = %q", fx.store.revokeReason)
	}
	if _, err := fx.service.Authenticate(context.Background(), one.Access.Reveal()); err != domain.ErrInvalidSession {
		t.Fatalf("revoked access error = %v", err)
	}
	if _, err := fx.service.Authenticate(context.Background(), two.Access.Reveal()); err != nil {
		t.Fatalf("other access unexpectedly revoked: %v", err)
	}
	if err := fx.service.RevokeAll(context.Background(), two.Access.Reveal()); err != nil {
		t.Fatalf("RevokeAll() error = %v", err)
	}
	if fx.store.revokeAllReason != "logout_all" {
		t.Fatalf("revoke-all reason = %q", fx.store.revokeAllReason)
	}
	if _, err := fx.service.Authenticate(context.Background(), two.Access.Reveal()); err != domain.ErrInvalidSession {
		t.Fatalf("revoke-all access error = %v", err)
	}
}

func TestBackendAndRandomErrorsAreSafeAndReturnNoTokens(t *testing.T) {
	t.Run("random", func(t *testing.T) {
		fx := newFixture(t)
		fx.service.config.Random = io.MultiReader(bytes.NewReader(make([]byte, 16)), errorReader{errSecretBackend})
		tokens, err := fx.service.Start(context.Background(), fx.subject)
		assertSafeFailure(t, tokens, err)
	})
	t.Run("create", func(t *testing.T) {
		fx := newFixture(t)
		fx.store.createErr = errSecretBackend
		tokens, err := fx.service.Start(context.Background(), fx.subject)
		assertSafeFailure(t, tokens, err)
	})
	t.Run("access put on start revokes", func(t *testing.T) {
		fx := newFixture(t)
		fx.access.putErr = errSecretBackend
		tokens, err := fx.service.Start(context.Background(), fx.subject)
		assertSafeFailure(t, tokens, err)
		if fx.store.revokeReason != "issuance_failed" {
			t.Fatalf("cleanup reason = %q", fx.store.revokeReason)
		}
	})
	t.Run("access put after consumed refresh", func(t *testing.T) {
		fx := newFixture(t)
		started, _ := fx.service.Start(context.Background(), fx.subject)
		fx.access.putErr = errSecretBackend
		tokens, err := fx.service.Refresh(context.Background(), started.Refresh.Reveal())
		assertSafeFailure(t, tokens, err)
		if _, err := fx.service.Refresh(context.Background(), started.Refresh.Reveal()); err != domain.ErrRefreshReuse {
			t.Fatalf("consumed refresh replay error = %v", err)
		}
	})
	t.Run("find", func(t *testing.T) {
		fx := newFixture(t)
		started, _ := fx.service.Start(context.Background(), fx.subject)
		fx.store.findErr = errSecretBackend
		_, err := fx.service.Authenticate(context.Background(), started.Access.Reveal())
		assertSafeError(t, err)
	})
	t.Run("access get", func(t *testing.T) {
		fx := newFixture(t)
		started, _ := fx.service.Start(context.Background(), fx.subject)
		fx.access.getErr = errSecretBackend
		_, err := fx.service.Authenticate(context.Background(), started.Access.Reveal())
		assertSafeError(t, err)
	})
	t.Run("rotate", func(t *testing.T) {
		fx := newFixture(t)
		started, _ := fx.service.Start(context.Background(), fx.subject)
		fx.store.rotateErr = errSecretBackend
		tokens, err := fx.service.Refresh(context.Background(), started.Refresh.Reveal())
		assertSafeFailure(t, tokens, err)
	})
	t.Run("revoke", func(t *testing.T) {
		fx := newFixture(t)
		started, _ := fx.service.Start(context.Background(), fx.subject)
		fx.store.revokeErr = errSecretBackend
		assertSafeError(t, fx.service.Revoke(context.Background(), started.Access.Reveal()))
	})
	t.Run("revoke all", func(t *testing.T) {
		fx := newFixture(t)
		started, _ := fx.service.Start(context.Background(), fx.subject)
		fx.store.revokeAllErr = errSecretBackend
		assertSafeError(t, fx.service.RevokeAll(context.Background(), started.Access.Reveal()))
	})
	t.Run("issuer", func(t *testing.T) {
		fx := newFixture(t)
		started, _ := fx.service.Start(context.Background(), fx.subject)
		fx.issuer.err = errSecretBackend
		_, _, err := fx.service.Exchange(context.Background(), started.Access.Reveal(), "orders")
		assertSafeError(t, err)
	})
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }
func assertSafeFailure(t *testing.T, tokens Tokens, err error) {
	t.Helper()
	assertSafeError(t, err)
	if tokens.Record.ID != (domain.ID{}) || tokens.Access.Reveal() != "" || tokens.Refresh.Reveal() != "" {
		t.Fatalf("failure exposed partial tokens: %#v", tokens)
	}
}
func assertSafeError(t *testing.T, err error) {
	t.Helper()
	if err != domain.ErrUnavailable {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if strings.Contains(fmt.Sprint(err), "backend-password") {
		t.Fatalf("error leaked backend text: %v", err)
	}
}

func TestExchangeCapsAssertionToCanonicalExpiry(t *testing.T) {
	for name, remaining := range map[string]time.Duration{"access": 90 * time.Second, "absolute": 45 * time.Second} {
		t.Run(name, func(t *testing.T) {
			fx := newFixture(t)
			tokens, _ := fx.service.Start(context.Background(), fx.subject)
			fx.store.mu.Lock()
			record := fx.store.records[tokens.Record.ID]
			if name == "access" {
				record.AccessExpiresAt = fx.now.Add(remaining)
			} else {
				record.ExpiresAt = fx.now.Add(remaining)
				record.AccessExpiresAt = record.ExpiresAt
			}
			fx.store.records[record.ID] = record
			fx.store.mu.Unlock()
			fx.access.mu.Lock()
			access := fx.access.values[record.ID]
			access.ExpiresAt = record.AccessExpiresAt
			fx.access.values[record.ID] = access
			fx.access.mu.Unlock()
			got, expiry, err := fx.service.Exchange(context.Background(), tokens.Access.Reveal(), "orders")
			if err != nil || got != "assertion" {
				t.Fatalf("Exchange() = (%q,%v)", got, err)
			}
			if !fx.issuer.expiresAt.Equal(fx.now.Add(remaining)) || !expiry.Equal(fx.now.Add(remaining)) || fx.issuer.audience != "orders" {
				t.Fatalf("issue deadline/audience/expiry = %v/%q/%v", fx.issuer.expiresAt, fx.issuer.audience, expiry)
			}
		})
	}
}

func TestCheckValidatesSubjectAuthTimeAndRevocation(t *testing.T) {
	fx := newFixture(t)
	tokens, _ := fx.service.Start(context.Background(), fx.subject)
	if err := fx.service.Check(context.Background(), tokens.Record.ID, fx.subject, tokens.Record.CreatedAt); err != nil {
		t.Fatalf("Check(valid) error = %v", err)
	}
	wrong := fx.subject
	wrong[0]++
	if err := fx.service.Check(context.Background(), tokens.Record.ID, wrong, tokens.Record.CreatedAt); err != domain.ErrInvalidSession {
		t.Fatalf("Check(subject) error = %v", err)
	}
	if err := fx.service.Check(context.Background(), tokens.Record.ID, fx.subject, tokens.Record.CreatedAt.Add(time.Second)); err != domain.ErrInvalidSession {
		t.Fatalf("Check(auth time) error = %v", err)
	}
	_ = fx.service.Revoke(context.Background(), tokens.Access.Reveal())
	if err := fx.service.Check(context.Background(), tokens.Record.ID, fx.subject, tokens.Record.CreatedAt); err != domain.ErrInvalidSession {
		t.Fatalf("Check(revoked) error = %v", err)
	}
}

func TestNewValidatesConfigurationBounds(t *testing.T) {
	valid := Config{AccessTTL: time.Minute, IdleTTL: time.Hour, AbsoluteTTL: 24 * time.Hour, AssertionTTL: 30 * time.Second}
	store, access, issuer := newFakeStore(), newFakeAccessStore(), &fakeIssuer{}
	if _, err := New(store, access, issuer, valid); err != nil {
		t.Fatalf("valid config error = %v", err)
	}
	tests := map[string]Config{
		"fractional":                  {AccessTTL: time.Minute + time.Millisecond, IdleTTL: time.Hour, AbsoluteTTL: 24 * time.Hour, AssertionTTL: 30 * time.Second},
		"below second":                {AccessTTL: time.Minute, IdleTTL: time.Hour, AbsoluteTTL: 24 * time.Hour, AssertionTTL: time.Millisecond},
		"assertion over five minutes": {AccessTTL: 10 * time.Minute, IdleTTL: time.Hour, AbsoluteTTL: 24 * time.Hour, AssertionTTL: 6 * time.Minute},
		"assertion over access":       {AccessTTL: time.Minute, IdleTTL: time.Hour, AbsoluteTTL: 24 * time.Hour, AssertionTTL: 2 * time.Minute},
		"access over idle":            {AccessTTL: 2 * time.Hour, IdleTTL: time.Hour, AbsoluteTTL: 24 * time.Hour, AssertionTTL: time.Minute},
		"idle over absolute":          {AccessTTL: time.Minute, IdleTTL: 48 * time.Hour, AbsoluteTTL: 24 * time.Hour, AssertionTTL: time.Minute},
		"absolute over ninety days":   {AccessTTL: time.Minute, IdleTTL: time.Hour, AbsoluteTTL: 91 * 24 * time.Hour, AssertionTTL: time.Minute},
	}
	for name, config := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := New(store, access, issuer, config); err == nil {
				t.Fatal("New() accepted invalid config")
			}
		})
	}
	if _, err := New(nil, access, issuer, valid); err == nil {
		t.Fatal("New() accepted nil store")
	}
}

func TestTokensFormattingIsRedacted(t *testing.T) {
	fx := newFixture(t)
	tokens, _ := fx.service.Start(context.Background(), fx.subject)
	for _, format := range []string{"%v", "%#v"} {
		got := fmt.Sprintf(format, tokens)
		if !strings.Contains(got, "[REDACTED]") || strings.Contains(got, tokens.Access.Reveal()) || strings.Contains(got, tokens.Refresh.Reveal()) {
			t.Fatalf("format %s exposed tokens: %q", format, got)
		}
	}
}
