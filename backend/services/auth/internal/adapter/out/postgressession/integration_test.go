//go:build integration

package postgressession_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	pg "github.com/v0hmly/marketmesh/platform/postgres"
	platformredis "github.com/v0hmly/marketmesh/platform/redis"
	runtime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/testkit"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/postgressession"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/redissession"
	applicationsession "github.com/v0hmly/marketmesh/services/auth/internal/application/session"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
	"github.com/v0hmly/marketmesh/services/auth/migrations"
)

func TestIntegrationSessionStores(t *testing.T) {
	database := integrationDatabase(t)
	repository, err := postgressession.New(database)
	if err != nil {
		t.Fatal(err)
	}
	redisClient := integrationRedis(t)
	accessStore, err := redissession.New(redisClient)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	makeRecord := func(n byte) domain.Record {
		record := domain.Record{ID: domain.ID{n}, Version: 1, CreatedAt: now, AccessExpiresAt: now.Add(time.Minute), RefreshExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(2 * time.Hour)}
		record.SubjectID[0] = 1
		return record
	}
	create := func(t *testing.T, n byte) domain.Record {
		t.Helper()
		record := makeRecord(n)
		if err := repository.Create(t.Context(), record, domain.Digest{n}); err != nil {
			t.Fatal(err)
		}
		return record
	}
	t.Run("rotation and consumed replay survive sliding expiry", func(t *testing.T) {
		record := create(t, 1)
		if _, err := repository.Rotate(t.Context(), record.ID, domain.Digest{99}, domain.Digest{100}, now, time.Minute, time.Hour); err != domain.ErrInvalidSession {
			t.Fatalf("unknown digest=%v", err)
		}
		current, err := repository.Find(t.Context(), record.ID)
		if err != nil || current.RevokedAt != nil {
			t.Fatalf("unknown digest revoked family: %v", err)
		}
		rotated, err := repository.Rotate(t.Context(), record.ID, domain.Digest{1}, domain.Digest{2}, now.Add(time.Second), time.Minute, time.Minute)
		if err != nil || rotated.Version != 2 {
			t.Fatalf("Rotate=%v version=%d", err, rotated.Version)
		}
		if _, err := repository.Rotate(t.Context(), record.ID, domain.Digest{1}, domain.Digest{3}, now.Add(2*time.Minute), time.Minute, time.Hour); err != domain.ErrRefreshReuse {
			t.Fatalf("consumed after idle expiry=%v", err)
		}
		current, err = repository.Find(t.Context(), record.ID)
		if err != nil || current.RevokedAt == nil {
			t.Fatalf("replay not durable: %v", err)
		}
		assertCount(t, database, `SELECT count(*) FROM auth.session_revocation_outbox WHERE session_id=$1`, record.ID.Bytes(), 1)
		assertCount(t, database, `SELECT count(*) FROM auth.consumed_refresh_digests WHERE session_id=$1`, record.ID.Bytes(), 1)
	})
	t.Run("concurrent refresh single winner and family revoked", func(t *testing.T) {
		record := create(t, 3)
		const workers = 16
		start := make(chan struct{})
		results := make(chan error, workers)
		var wg sync.WaitGroup
		for i := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := repository.Rotate(t.Context(), record.ID, domain.Digest{3}, domain.Digest{byte(20 + i)}, now.Add(time.Second), time.Minute, time.Hour)
				results <- err
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		wins, replays := 0, 0
		for err := range results {
			switch {
			case err == nil:
				wins++
			case errors.Is(err, domain.ErrRefreshReuse):
				replays++
			case errors.Is(err, domain.ErrInvalidSession):
			default:
				t.Fatalf("race backend error=%v", err)
			}
		}
		if wins != 1 || replays != 1 {
			t.Fatalf("wins=%d replays=%d", wins, replays)
		}
		stored, err := repository.Find(t.Context(), record.ID)
		if err != nil || stored.RevokedAt == nil {
			t.Fatalf("race family active: %v", err)
		}
		assertCount(t, database, `SELECT count(*) FROM auth.session_revocation_outbox WHERE session_id=$1`, record.ID.Bytes(), 1)
	})
	t.Run("exact expiry and absolute cap", func(t *testing.T) {
		record := create(t, 4)
		if _, err := repository.Rotate(t.Context(), record.ID, domain.Digest{4}, domain.Digest{44}, record.RefreshExpiresAt, time.Minute, time.Hour); err != domain.ErrInvalidSession {
			t.Fatalf("idle boundary=%v", err)
		}
		rotated, err := repository.Rotate(t.Context(), record.ID, domain.Digest{4}, domain.Digest{44}, now.Add(time.Minute), 24*time.Hour, 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if !rotated.AccessExpiresAt.Equal(record.ExpiresAt) || !rotated.RefreshExpiresAt.Equal(record.ExpiresAt) {
			t.Fatal("rotation exceeded absolute expiry")
		}
		if _, err := repository.Rotate(t.Context(), record.ID, domain.Digest{44}, domain.Digest{45}, record.ExpiresAt, time.Minute, time.Hour); err != domain.ErrInvalidSession {
			t.Fatalf("absolute boundary=%v", err)
		}
	})
	t.Run("outbox failure rolls back revocation and rotation", func(t *testing.T) {
		record := create(t, 5)
		if _, err := repository.Rotate(t.Context(), record.ID, domain.Digest{5}, domain.Digest{55}, now, time.Minute, time.Hour); err != nil {
			t.Fatal(err)
		}
		if _, err := database.RW().Exec(t.Context(), `ALTER TABLE auth.session_revocation_outbox ADD CONSTRAINT integration_reject CHECK (false) NOT VALID`); err != nil {
			t.Fatal(err)
		}
		_, reuseErr := repository.Rotate(t.Context(), record.ID, domain.Digest{5}, domain.Digest{56}, now, time.Minute, time.Hour)
		revokeErr := repository.Revoke(t.Context(), record.ID, now, "logout")
		if _, err := database.RW().Exec(t.Context(), `ALTER TABLE auth.session_revocation_outbox DROP CONSTRAINT integration_reject`); err != nil {
			t.Fatal(err)
		}
		if reuseErr != domain.ErrUnavailable || revokeErr != domain.ErrUnavailable {
			t.Fatalf("outbox failure reuse=%v revoke=%v", reuseErr, revokeErr)
		}
		stored, err := repository.Find(t.Context(), record.ID)
		if err != nil || stored.RevokedAt != nil || stored.Version != 2 {
			t.Fatalf("outbox rollback failed: %v", err)
		}
	})
	t.Run("Redis monotonic version TTL and malformed state", func(t *testing.T) {
		record := create(t, 6)
		record.Version = 9007199254740993
		if err := accessStore.Put(t.Context(), record, domain.Digest{6}, now); err != nil {
			t.Fatal(err)
		}
		older := record
		older.Version--
		if err := accessStore.Put(t.Context(), older, domain.Digest{66}, now); err != domain.ErrInvalidSession {
			t.Fatalf("older Put=%v", err)
		}
		if err := accessStore.Put(t.Context(), record, domain.Digest{67}, now); err != domain.ErrInvalidSession {
			t.Fatalf("same version new digest=%v", err)
		}
		access, err := accessStore.Get(t.Context(), record.ID)
		if err != nil || access.Version != record.Version || access.Digest != (domain.Digest{6}) {
			t.Fatalf("Redis state mismatch: %v", err)
		}
		if err := redisClient.Execute(t.Context(), func(ctx context.Context, commands goredis.Cmdable) error {
			ttl, err := commands.PTTL(ctx, "auth:session:access:"+record.ID.String()).Result()
			if err != nil {
				return err
			}
			if ttl <= 0 || ttl > time.Minute {
				return errors.New("access TTL is outside its finite lifetime")
			}
			fields, err := commands.HGetAll(ctx, "auth:session:access:"+record.ID.String()).Result()
			if err != nil {
				return err
			}
			if len(fields) != 3 || len(fields["digest"]) != 64 {
				return errors.New("unexpected Redis fields")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		short := makeRecord(7)
		short.AccessExpiresAt = time.Now().Add(80 * time.Millisecond)
		if err := accessStore.Put(t.Context(), short, domain.Digest{7}, time.Now()); err != nil {
			t.Fatal(err)
		}
		testkit.Eventually(t, time.Second, 10*time.Millisecond, func() bool { _, err := accessStore.Get(t.Context(), short.ID); return err == domain.ErrInvalidSession })
		if err := redisClient.Execute(t.Context(), func(ctx context.Context, commands goredis.Cmdable) error {
			return commands.HSet(ctx, "auth:session:access:"+record.ID.String(), "version", "broken").Err()
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := accessStore.Get(t.Context(), record.ID); err != domain.ErrUnavailable {
			t.Fatalf("malformed cache=%v", err)
		}
	})
	t.Run("concurrent Redis writes retain greatest version", func(t *testing.T) {
		base := makeRecord(8)
		const workers = 16
		var wg sync.WaitGroup
		results := make(chan error, workers)
		for i := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				record := base
				record.Version = int64(i + 1)
				results <- accessStore.Put(t.Context(), record, domain.Digest{byte(i + 1)}, now)
			}()
		}
		wg.Wait()
		close(results)
		for err := range results {
			if err != nil && err != domain.ErrInvalidSession {
				t.Fatalf("Redis race=%v", err)
			}
		}
		access, err := accessStore.Get(t.Context(), base.ID)
		if err != nil || access.Version != workers || access.Digest != (domain.Digest{workers}) {
			t.Fatalf("greatest version lost: %v version=%d", err, access.Version)
		}
	})
	t.Run("revoke all is durable and idempotent", func(t *testing.T) {
		record := create(t, 9)
		create(t, 10)
		if err := repository.RevokeAll(t.Context(), record.SubjectID, now, "logout_all"); err != nil {
			t.Fatal(err)
		}
		if err := repository.RevokeAll(t.Context(), record.SubjectID, now, "logout_all"); err != nil {
			t.Fatal(err)
		}
		assertCount(t, database, `SELECT count(*) FROM auth.sessions WHERE subject_id=$1 AND revoked_at IS NULL`, record.SubjectID.Bytes(), 0)
		assertCount(t, database, `SELECT count(*) FROM auth.session_revocation_outbox WHERE session_id=$1`, record.ID.Bytes(), 1)
	})
	t.Run("application lifecycle across PostgreSQL and Redis", func(t *testing.T) {
		clock := time.Now().UTC().Truncate(time.Second)
		issuer := &boundedIssuer{}
		service, err := applicationsession.New(repository, accessStore, issuer, applicationsession.Config{
			AccessTTL: time.Minute, IdleTTL: time.Hour, AbsoluteTTL: 2 * time.Hour, AssertionTTL: 30 * time.Second,
			Clock: func() time.Time { return clock },
		})
		if err != nil {
			t.Fatal(err)
		}
		subject := credential.SubjectID{1}
		unrelated := credential.SubjectID{2}
		if _, err := database.RW().Exec(t.Context(), `INSERT INTO auth.credentials(subject_id,identifier,password_digest) VALUES ($1,'unrelated@example.com','test-digest')`, unrelated.Bytes()); err != nil {
			t.Fatal(err)
		}
		start := func(subject credential.SubjectID) applicationsession.Tokens {
			t.Helper()
			tokens, err := service.Start(t.Context(), subject)
			if err != nil {
				t.Fatal(err)
			}
			authenticated, err := service.Authenticate(t.Context(), tokens.Access.Reveal())
			if err != nil || authenticated.ID != tokens.Record.ID || authenticated.SubjectID != subject {
				t.Fatalf("new session authentication: %v", err)
			}
			return tokens
		}
		original := start(subject)
		clock = clock.Add(50 * time.Second)
		assertion, expiresAt, err := service.Exchange(t.Context(), original.Access.Reveal(), "user-service")
		if err != nil || assertion != "integration-assertion" || !expiresAt.Equal(original.Record.AccessExpiresAt) || !issuer.expiry.Equal(expiresAt) || issuer.record.ID != original.Record.ID || issuer.audience != "user-service" {
			t.Fatalf("bounded exchange: %v expiry=%s", err, expiresAt)
		}
		if err := service.Check(t.Context(), original.Record.ID, subject, original.Record.CreatedAt); err != nil {
			t.Fatalf("online check before rotation: %v", err)
		}
		rotated, err := service.Refresh(t.Context(), original.Refresh.Reveal())
		if err != nil {
			t.Fatal(err)
		}
		if rotated.Record.ID != original.Record.ID || rotated.Record.Version != 2 || rotated.Access.Digest() == original.Access.Digest() || rotated.Refresh.Digest() == original.Refresh.Digest() {
			t.Fatal("refresh did not rotate session credentials")
		}
		if _, err := service.Authenticate(t.Context(), original.Access.Reveal()); err != domain.ErrInvalidSession {
			t.Fatalf("old access accepted after refresh: %v", err)
		}
		if _, err := service.Authenticate(t.Context(), rotated.Access.Reveal()); err != nil {
			t.Fatalf("rotated access rejected: %v", err)
		}
		if _, err := service.Refresh(t.Context(), original.Refresh.Reveal()); err != domain.ErrRefreshReuse {
			t.Fatalf("consumed refresh replay: %v", err)
		}
		stored, err := repository.Find(t.Context(), original.Record.ID)
		if err != nil || stored.RevokedAt == nil {
			t.Fatalf("replay revocation not durable: %v", err)
		}
		if _, err := service.Authenticate(t.Context(), rotated.Access.Reveal()); err != domain.ErrInvalidSession {
			t.Fatalf("replayed family still authenticates: %v", err)
		}
		if err := service.Check(t.Context(), original.Record.ID, subject, original.Record.CreatedAt); err != domain.ErrInvalidSession {
			t.Fatalf("online check accepted revoked assertion session: %v", err)
		}
		assertCount(t, database, `SELECT count(*) FROM auth.session_revocation_outbox WHERE session_id=$1`, original.Record.ID.Bytes(), 1)
		first, second, isolated := start(subject), start(subject), start(unrelated)
		if err := service.RevokeAll(t.Context(), first.Access.Reveal()); err != nil {
			t.Fatal(err)
		}
		for _, tokens := range []applicationsession.Tokens{first, second} {
			if _, err := service.Authenticate(t.Context(), tokens.Access.Reveal()); err != domain.ErrInvalidSession {
				t.Fatalf("logout-all retained access: %v", err)
			}
			if err := service.Check(t.Context(), tokens.Record.ID, subject, tokens.Record.CreatedAt); err != domain.ErrInvalidSession {
				t.Fatalf("logout-all retained online session: %v", err)
			}
			if _, err := service.Refresh(t.Context(), tokens.Refresh.Reveal()); err != domain.ErrInvalidSession {
				t.Fatalf("logout-all retained refresh: %v", err)
			}
			assertCount(t, database, `SELECT count(*) FROM auth.session_revocation_outbox WHERE session_id=$1`, tokens.Record.ID.Bytes(), 1)
		}
		if _, err := service.Authenticate(t.Context(), isolated.Access.Reveal()); err != nil {
			t.Fatalf("logout-all crossed subject boundary: %v", err)
		}
		if err := service.Check(t.Context(), isolated.Record.ID, unrelated, isolated.Record.CreatedAt); err != nil {
			t.Fatalf("unrelated online session rejected: %v", err)
		}
		if _, err := service.Refresh(t.Context(), isolated.Refresh.Reveal()); err != nil {
			t.Fatalf("unrelated refresh rejected: %v", err)
		}
	})
	t.Run("migration reverses cleanly", func(t *testing.T) {
		if _, err := database.RW().Exec(t.Context(), migrations.SessionsDown); err != nil {
			t.Fatal(err)
		}
		if _, err := database.RW().Exec(t.Context(), migrations.SessionsUp); err != nil {
			t.Fatal(err)
		}
	})
}
func assertCount(t *testing.T, database postgressession.Database, query string, arg any, want int) {
	t.Helper()
	var count int
	if err := database.RW().QueryRow(t.Context(), query, arg).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("count=%d want=%d", count, want)
	}
}
func secret(t *testing.T, name string) runtime.Secret {
	t.Helper()
	if os.Getenv(name) == "" {
		t.Fatalf("%s is required", name)
	}
	value, err := runtime.MapEnv(map[string]string{name: os.Getenv(name)}).Secret(name, true)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func integrationDatabase(t *testing.T) *integrationPrimary {
	t.Helper()
	dsn := os.Getenv("MARKETMESH_AUTH_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("MARKETMESH_AUTH_POSTGRES_DSN is required")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 20
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	database := &integrationPrimary{pool: pool}
	for _, sql := range []string{migrations.SessionsDown, migrations.CredentialsDown, migrations.CredentialsUp, migrations.SessionsUp} {
		if _, err := database.RW().Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	subject := make([]byte, 16)
	subject[0] = 1
	if _, err := database.RW().Exec(t.Context(), `INSERT INTO auth.credentials(subject_id,identifier,password_digest) VALUES ($1,'session@example.com','test-digest')`, subject); err != nil {
		t.Fatal(err)
	}
	return database
}
func integrationRedis(t *testing.T) *platformredis.Client {
	t.Helper()
	client, err := platformredis.New(t.Context(), platformredis.Config{
		Role: platformredis.RoleAuth, Address: secret(t, "MARKETMESH_AUTH_REDIS_ADDRESS"), Authentication: platformredis.AuthenticationConfig{Password: secret(t, "MARKETMESH_AUTH_REDIS_PASSWORD")},
		Transport: platformredis.TransportConfig{PlaintextException: &platformredis.PlaintextException{Reason: "MM-14 disposable isolated Docker integration network"}},
		Pool:      platformredis.PoolConfig{Size: 20, MaxIdleConns: 20, MaxActiveConns: 20, MaxConcurrentDials: 10, ConnMaxIdleTime: time.Minute, ConnMaxLifetime: time.Hour},
		Timeouts:  platformredis.TimeoutConfig{Connect: 5 * time.Second, Command: 5 * time.Second, Pool: time.Second, Read: time.Second, Write: time.Second, Readiness: time.Second, Shutdown: time.Second},
	}, testkit.NoopTelemetry(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return client
}

// boundedIssuer isolates storage/application behavior from separately tested JWS signing.
type boundedIssuer struct {
	record   domain.Record
	audience string
	expiry   time.Time
}

func (issuer *boundedIssuer) Issue(_ context.Context, record domain.Record, audience string, expiresAt time.Time) (string, error) {
	if audience != "user-service" || expiresAt.After(record.AccessExpiresAt) || expiresAt.After(record.ExpiresAt) {
		return "", domain.ErrInvalidSession
	}
	issuer.record, issuer.audience, issuer.expiry = record, audience, expiresAt
	return "integration-assertion", nil
}

// integrationPrimary supplies actual PostgreSQL transactions for the disposable
// single-primary fixture. Production pool role validation remains unchanged.
type integrationPrimary struct{ pool *pgxpool.Pool }

func (database *integrationPrimary) RW() pg.Executor { return database.pool }
func (database *integrationPrimary) WithinTransaction(ctx context.Context, options pg.TransactionOptions, callback pg.TransactionFunc) error {
	if options != (pg.TransactionOptions{}) {
		return errors.New("integration fixture requires default transaction options")
	}
	return pgx.BeginTxFunc(ctx, database.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite}, func(tx pgx.Tx) error { return callback(ctx, tx) })
}
