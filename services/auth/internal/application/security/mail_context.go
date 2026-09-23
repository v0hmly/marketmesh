package security

import (
	"context"

	"github.com/v0hmly/marketmesh/services/auth/internal/application/mailtime"
)

type mailTimeZoneKey struct{}

// WithMailTimeZone attaches an optional request display preference, never an
// authorization claim. Queueing snapshots it into the encrypted mail payload.
func WithMailTimeZone(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, mailTimeZoneKey{}, mailtime.Location(name).String())
}

func mailTimeZone(ctx context.Context) string {
	name, _ := ctx.Value(mailTimeZoneKey{}).(string)
	if name == "" {
		return "UTC"
	}
	return name
}
