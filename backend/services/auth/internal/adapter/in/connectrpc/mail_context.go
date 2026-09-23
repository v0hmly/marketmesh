package connectrpc

import (
	"context"
	"net/http"

	"github.com/v0hmly/marketmesh/services/auth/internal/application/security"
)

func mailContext(ctx context.Context, header http.Header) context.Context {
	values := header.Values("X-MarketMesh-Time-Zone")
	name := ""
	if len(values) == 1 {
		name = values[0]
	}
	return security.WithMailTimeZone(ctx, name)
}
