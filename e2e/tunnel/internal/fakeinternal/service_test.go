package fakeinternal

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"

	e2ev1 "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestNewRejectsUnboundedConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config Config
	}{
		{name: "empty instance", config: Config{MaxLedgerEntries: 1}},
		{name: "control character", config: Config{InstanceID: "pod\nname", MaxLedgerEntries: 1}},
		{name: "empty ledger", config: Config{InstanceID: "pod-1"}},
		{name: "oversized ledger", config: Config{InstanceID: "pod-1", MaxLedgerEntries: 100_001}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(test.config); err == nil {
				t.Fatal("New() error = nil")
			}
		})
	}
}

func TestServiceRecordsReadDefensively(t *testing.T) {
	t.Parallel()

	service := newTestService(t, 4)
	requestID := bytes.Repeat([]byte{0x11}, requestIDBytes)
	response, err := service.Read(t.Context(), &e2ev1.ReadRequest{RequestId: requestID})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if response.GetInstanceId() != "fake-internal-1" || response.GetSequence() != 1 {
		t.Fatalf("Read() response = %v", response)
	}
	requestID[0] = 0xff

	ledger, err := service.Ledger(t.Context(), &e2ev1.LedgerRequest{Limit: 4})
	if err != nil {
		t.Fatalf("Ledger() error = %v", err)
	}
	if len(ledger.GetEntries()) != 1 || ledger.GetEntries()[0].GetRequestId()[0] != 0x11 {
		t.Fatalf("Ledger() entries = %v, want defensive request id copy", ledger.GetEntries())
	}
	ledger.GetEntries()[0].RequestId[0] = 0xee
	second, err := service.Ledger(t.Context(), &e2ev1.LedgerRequest{Limit: 4})
	if err != nil {
		t.Fatalf("second Ledger() error = %v", err)
	}
	if second.GetEntries()[0].GetRequestId()[0] != 0x11 {
		t.Fatal("Ledger() returned mutable internal request id")
	}
}

func TestServiceDeduplicatesMutationWithoutRetainingRawKey(t *testing.T) {
	t.Parallel()

	service := newTestService(t, 4)
	requestID := bytes.Repeat([]byte{0x22}, requestIDBytes)
	rawKey := "mm29-sensitive-idempotency-key"
	ctx := metadata.NewIncomingContext(
		t.Context(),
		metadata.Pairs(protocolv1.InternalIdempotencyKeyMetadata, rawKey),
	)

	first, err := service.Mutate(ctx, &e2ev1.MutateRequest{RequestId: requestID})
	if err != nil {
		t.Fatalf("first Mutate() error = %v", err)
	}
	second, err := service.Mutate(ctx, &e2ev1.MutateRequest{RequestId: requestID})
	if err != nil {
		t.Fatalf("second Mutate() error = %v", err)
	}
	if first.GetSequence() != second.GetSequence() || first.GetDuplicate() || !second.GetDuplicate() {
		t.Fatalf("mutation responses = first %v, second %v", first, second)
	}

	ledger, err := service.Ledger(t.Context(), &e2ev1.LedgerRequest{Limit: 4})
	if err != nil {
		t.Fatalf("Ledger() error = %v", err)
	}
	if len(ledger.GetEntries()) != 1 || ledger.GetEntries()[0].GetAttempts() != 2 {
		t.Fatalf("Ledger() entries = %v", ledger.GetEntries())
	}
	digest := sha256.Sum256([]byte(rawKey))
	if !bytes.Equal(ledger.GetEntries()[0].GetIdempotencyKeySha256(), digest[:]) {
		t.Fatal("Ledger() idempotency digest mismatch")
	}
	if strings.Contains(ledger.String(), rawKey) {
		t.Fatal("Ledger() retained raw idempotency key")
	}
}

func TestServiceRejectsMutationPolicyViolations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		metadata metadata.MD
		wantCode codes.Code
	}{
		{name: "missing", wantCode: codes.InvalidArgument},
		{
			name: "duplicate",
			metadata: metadata.MD{
				protocolv1.InternalIdempotencyKeyMetadata: []string{"first", "second"},
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "oversized",
			metadata: metadata.Pairs(
				protocolv1.InternalIdempotencyKeyMetadata,
				strings.Repeat("k", protocolv1.MaxIdempotencyKeyBytes+1),
			),
			wantCode: codes.InvalidArgument,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service := newTestService(t, 2)
			ctx := metadata.NewIncomingContext(t.Context(), test.metadata)
			_, err := service.Mutate(ctx, &e2ev1.MutateRequest{
				RequestId: bytes.Repeat([]byte{0x33}, requestIDBytes),
			})
			if code := status.Code(err); code != test.wantCode {
				t.Fatalf("Mutate() code = %s, want %s (error %v)", code, test.wantCode, err)
			}
		})
	}
}

func TestServiceBoundsLedgerAndDetectsKeyConflict(t *testing.T) {
	t.Parallel()

	service := newTestService(t, 1)
	ctx := metadata.NewIncomingContext(
		t.Context(),
		metadata.Pairs(protocolv1.InternalIdempotencyKeyMetadata, "same-key"),
	)
	_, err := service.Mutate(ctx, &e2ev1.MutateRequest{
		RequestId: bytes.Repeat([]byte{0x44}, requestIDBytes),
	})
	if err != nil {
		t.Fatalf("Mutate() error = %v", err)
	}
	_, err = service.Mutate(ctx, &e2ev1.MutateRequest{
		RequestId: bytes.Repeat([]byte{0x45}, requestIDBytes),
	})
	if code := status.Code(err); code != codes.FailedPrecondition {
		t.Fatalf("conflicting Mutate() code = %s, want FailedPrecondition", code)
	}
	_, err = service.Read(t.Context(), &e2ev1.ReadRequest{
		RequestId: bytes.Repeat([]byte{0x46}, requestIDBytes),
	})
	if code := status.Code(err); code != codes.ResourceExhausted {
		t.Fatalf("Read() code = %s, want ResourceExhausted", code)
	}
	_, err = service.Ledger(t.Context(), &e2ev1.LedgerRequest{})
	if code := status.Code(err); code != codes.InvalidArgument {
		t.Fatalf("Ledger(0) code = %s, want InvalidArgument", code)
	}
}

func newTestService(t *testing.T, maxEntries int) *Service {
	t.Helper()

	service, err := New(Config{InstanceID: "fake-internal-1", MaxLedgerEntries: maxEntries})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return service
}
