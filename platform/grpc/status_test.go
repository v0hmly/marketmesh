package grpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSanitizedStatusError(t *testing.T) {
	t.Parallel()

	domainErr := errors.New("domain not found with private database details")
	tests := []struct {
		name        string
		err         error
		mapper      ErrorCodeMapper
		wantCode    codes.Code
		wantMessage string
	}{
		{
			name:        "context canceled",
			err:         fmt.Errorf("operation: %w", context.Canceled),
			wantCode:    codes.Canceled,
			wantMessage: "request canceled",
		},
		{
			name:        "domain mapping",
			err:         domainErr,
			mapper:      func(error) (codes.Code, bool) { return codes.NotFound, true },
			wantCode:    codes.NotFound,
			wantMessage: "not found",
		},
		{
			name:        "existing status message is removed",
			err:         status.Error(codes.PermissionDenied, "private policy name"),
			wantCode:    codes.PermissionDenied,
			wantMessage: "permission denied",
		},
		{
			name:        "unknown becomes internal",
			err:         status.Error(codes.Unknown, "driver failed"),
			wantCode:    codes.Internal,
			wantMessage: "internal error",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result := sanitizedStatusError(test.err, test.mapper)
			grpcStatus := status.Convert(result)
			if grpcStatus.Code() != test.wantCode || grpcStatus.Message() != test.wantMessage {
				t.Fatalf(
					"sanitizedStatusError() = %s %q, want %s %q",
					grpcStatus.Code(),
					grpcStatus.Message(),
					test.wantCode,
					test.wantMessage,
				)
			}
			if strings.Contains(result.Error(), "private") || strings.Contains(result.Error(), "driver") {
				t.Fatalf("sanitizedStatusError() leaked details: %v", result)
			}
		})
	}
}
