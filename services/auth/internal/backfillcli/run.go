// Package backfillcli implements an explicit, bounded operational command.
package backfillcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/postgresbackfill"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/registrationevent"
	app "github.com/v0hmly/marketmesh/services/auth/internal/application/backfillregistration"
	"io"
	"time"
)

func Run(ctx context.Context, args []string, env func(string) string, out io.Writer) error {
	flags := flag.NewFlagSet("auth-registration-backfill", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var o app.Options
	flags.StringVar(&o.After, "after", "", "opaque resume cursor")
	flags.IntVar(&o.Limit, "limit", 100, "maximum subjects (1..1000)")
	flags.BoolVar(&o.Apply, "apply", false, "write Auth outbox")
	flags.BoolVar(&o.ReplayPublished, "replay-published", false, "requeue previously published events using original ID")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return errors.New("registration backfill: invalid arguments")
	}
	if _, err := o.Validate(); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("registration backfill: context required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dsn := env("MARKETMESH_AUTH_POSTGRES_DSN")
	if dsn == "" {
		return errors.New("registration backfill: MARKETMESH_AUTH_POSTGRES_DSN required")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return errors.New("registration backfill: invalid database configuration")
	}
	cfg.MaxConns = 2
	cfg.MinConns = 0
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.ConnConfig.RuntimeParams["application_name"] = "auth-registration-backfill"
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return errors.New("registration backfill: connection failed")
	}
	defer db.Close()
	store, _ := postgresbackfill.New(db)
	service, _ := app.New(store, registrationevent.New())
	result, runErr := service.Page(ctx, o)
	if err = json.NewEncoder(out).Encode(result); err != nil {
		return errors.New("registration backfill: output failed")
	}
	return runErr
}
