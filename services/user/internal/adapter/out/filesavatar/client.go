// Package filesavatar accesses only the private workload-authorized avatar API.
package filesavatar

import (
	"context"
	"errors"
	"time"

	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/avatar"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type Client struct {
	rpc filesv1.FileAvatarServiceClient
}

func New(rpc filesv1.FileAvatarServiceClient) (*Client, error) {
	if rpc == nil {
		return nil, errors.New("avatar files: client required")
	}
	return &Client{rpc: rpc}, nil
}
func (c *Client) Inspect(ctx context.Context, owner profile.SubjectID, id avatar.FileID, session string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.MD{})
	r, err := c.rpc.InspectOwnedAvatar(ctx, &filesv1.InspectOwnedAvatarRequest{SubjectId: owner.Bytes(), FileId: id[:], SessionId: session}, grpc.WaitForReady(false))
	if err != nil {
		return safe(err)
	}
	if r == nil || (r.CleanMediaType != "image/png" && r.CleanMediaType != "image/jpeg") || r.CleanSizeBytes == 0 || r.CleanSizeBytes > 20*1024*1024 || len(r.CleanSha256) != 32 {
		return errors.New("avatar files: invalid response")
	}
	return nil
}
func (c *Client) Retire(ctx context.Context, owner profile.SubjectID, id avatar.FileID) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.MD{})
	_, err := c.rpc.RetireOwnedAvatar(ctx, &filesv1.RetireOwnedAvatarRequest{SubjectId: owner.Bytes(), FileId: id[:]}, grpc.WaitForReady(false))
	if err != nil {
		return safe(err)
	}
	return nil
}
func safe(err error) error {
	switch status.Code(err) {
	case codes.NotFound, codes.FailedPrecondition:
		return avatar.ErrUnavailable
	case codes.Canceled:
		return context.Canceled
	case codes.DeadlineExceeded:
		return context.DeadlineExceeded
	default:
		return errors.New("avatar files: dependency unavailable")
	}
}
