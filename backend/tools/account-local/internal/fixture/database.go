package fixture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// CheckDatabases uses a disposable fixture table to check both databases before
// and after a restart. It never reads or changes account data.
func CheckDatabases(ctx context.Context, phase string) error {
	if phase != "seed" && phase != "check" && phase != "clean" {
		return errors.New("unknown database check phase")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for _, service := range []string{"auth", "user"} {
		if err := checkDatabase(ctx, service, phase); err != nil {
			return fmt.Errorf("%s database check: %w", service, err)
		}
	}
	return nil
}

func checkDatabase(ctx context.Context, service, phase string) error {
	prefix := strings.ToUpper(service)
	admin, err := pgx.Connect(ctx, os.Getenv(prefix+"_ADMIN_DSN"))
	if err != nil {
		return errors.New("admin connection unavailable")
	}
	defer func() { _ = admin.Close(ctx) }()
	for {
		var ready bool
		err = admin.QueryRow(ctx, `SELECT current_setting('synchronous_commit') = 'remote_apply' AND (SELECT count(*) = 1 FROM pg_stat_replication WHERE application_name='account_sync' AND state='streaming' AND sync_state='sync')`).Scan(&ready)
		if err != nil {
			return errors.New("replication state unavailable")
		}
		if ready {
			break
		}
		select {
		case <-ctx.Done():
			return errors.New("synchronous replica unavailable")
		case <-time.After(200 * time.Millisecond):
		}
	}
	if phase == "clean" {
		_, err = admin.Exec(ctx, `DROP TABLE IF EXISTS public.local_fixture_probe; REVOKE USAGE ON SCHEMA public FROM `+pgx.Identifier{service + "_rw"}.Sanitize()+`,`+pgx.Identifier{service + "_ro"}.Sanitize())
		if err != nil {
			return errors.New("probe cleanup failed")
		}
		return nil
	}
	if phase == "seed" {
		rw, ro := pgx.Identifier{service + "_rw"}.Sanitize(), pgx.Identifier{service + "_ro"}.Sanitize()
		_, err = admin.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.local_fixture_probe (marker text PRIMARY KEY); GRANT USAGE ON SCHEMA public TO `+rw+`,`+ro+`; GRANT SELECT,INSERT,UPDATE ON public.local_fixture_probe TO `+rw+`; GRANT SELECT ON public.local_fixture_probe TO `+ro)
		if err != nil {
			return errors.New("probe setup failed")
		}
	}
	rw, err := pgx.Connect(ctx, os.Getenv(prefix+"_RW_DSN"))
	if err != nil {
		return errors.New("RW connection unavailable")
	}
	defer func() { _ = rw.Close(ctx) }()
	ro, err := pgx.Connect(ctx, os.Getenv(prefix+"_RO_DSN"))
	if err != nil {
		return errors.New("RO connection unavailable")
	}
	defer func() { _ = ro.Close(ctx) }()
	for _, item := range []struct {
		conn     *pgx.Conn
		recovery bool
		role     string
	}{{rw, false, service + "_rw"}, {ro, true, service + "_ro"}} {
		var safe bool
		err = item.conn.QueryRow(ctx, `SELECT pg_is_in_recovery()=$1 AND current_user=$2 AND NOT (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls) FROM pg_roles WHERE rolname=current_user`, item.recovery, item.role).Scan(&safe)
		if err != nil || !safe {
			return errors.New("endpoint or role privileges differ from policy")
		}
	}
	if phase == "seed" {
		if _, err = rw.Exec(ctx, `INSERT INTO public.local_fixture_probe(marker) VALUES($1) ON CONFLICT(marker) DO UPDATE SET marker=excluded.marker`, service); err != nil {
			return errors.New("RW write failed")
		}
	}
	var marker string
	if err = ro.QueryRow(ctx, `SELECT marker FROM public.local_fixture_probe WHERE marker=$1`, service).Scan(&marker); err != nil || marker != service {
		return errors.New("read-after-write or persistence failed")
	}
	if _, err = ro.Exec(ctx, `INSERT INTO public.local_fixture_probe(marker) VALUES('forbidden')`); !databaseErrorCode(err, "25006") {
		return errors.New("RO write was not rejected as read-only")
	}
	// Prove CONNECT isolation using each real credential on both database endpoints.
	other := "auth"
	if service == "auth" {
		other = "user"
	}
	for _, dsn := range []string{os.Getenv(prefix + "_RW_DSN"), os.Getenv(prefix + "_RO_DSN")} {
		for _, database := range []string{other, "postgres", "template1"} {
			cfg, e := pgx.ParseConfig(dsn)
			if e != nil {
				return errors.New("invalid fixture DSN")
			}
			cfg.Database = database
			conn, e := pgx.ConnectConfig(ctx, cfg)
			if conn != nil {
				_ = conn.Close(ctx)
			}
			if !databaseErrorCode(e, "42501") {
				return errors.New("cross-database connection was not rejected by privileges")
			}
		}
	}
	return nil
}

func databaseErrorCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}
