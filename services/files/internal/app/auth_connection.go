package app

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

// connectAuth gives initial DNS/TCP/mTLS setup its own bounded startup budget.
// Auth readiness and session verification retain their separate RPC deadlines.
func connectAuth(ctx context.Context, connection *grpc.ClientConn) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	connection.Connect()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		state := connection.GetState()
		if state == connectivity.Ready {
			return nil
		}
		if state == connectivity.Shutdown {
			return errors.New("Auth connection closed")
		}
		if !connection.WaitForStateChange(ctx, state) {
			return ctx.Err()
		}
	}
}
