// Package fakeinternal implements the bounded metadata-only workload used by
// tunnel E2E tests. It is not a production domain service.
package fakeinternal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"

	e2ev1 "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const requestIDBytes = 16

// Config bounds one fake workload instance.
type Config struct {
	InstanceID       string
	MaxLedgerEntries int
}

// Service stores a bounded, in-memory metadata ledger. Raw idempotency keys
// and request payloads are never retained.
type Service struct {
	e2ev1.UnimplementedFakeInternalServiceServer

	instanceID string
	maxEntries int

	mu        sync.Mutex
	entries   []*e2ev1.LedgerEntry
	mutations map[[sha256.Size]byte]int
	next      uint64
}

// New validates the bounded fake workload configuration.
func New(config Config) (*Service, error) {
	instanceID := strings.TrimSpace(config.InstanceID)
	if instanceID == "" || len(instanceID) > 128 {
		return nil, errors.New("fake internal: instance id is outside bounds")
	}
	for _, character := range []byte(instanceID) {
		if character < '!' || character > '~' {
			return nil, errors.New("fake internal: instance id must be printable ascii")
		}
	}
	if config.MaxLedgerEntries <= 0 || config.MaxLedgerEntries > 100_000 {
		return nil, errors.New("fake internal: max ledger entries is outside bounds")
	}

	return &Service{
		instanceID: instanceID,
		maxEntries: config.MaxLedgerEntries,
		entries:    make([]*e2ev1.LedgerEntry, 0, config.MaxLedgerEntries),
		mutations:  make(map[[sha256.Size]byte]int, config.MaxLedgerEntries),
	}, nil
}

// Read records one non-mutating request without retaining a payload.
func (s *Service) Read(
	_ context.Context,
	request *e2ev1.ReadRequest,
) (*e2ev1.ReadResponse, error) {
	if err := validateRequestID(request.GetRequestId()); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) >= s.maxEntries {
		return nil, status.Error(codes.ResourceExhausted, "ledger capacity exhausted")
	}

	s.next++
	sequence := s.next
	s.entries = append(s.entries, &e2ev1.LedgerEntry{
		Sequence:  sequence,
		Operation: e2ev1.Operation_OPERATION_READ,
		RequestId: slices.Clone(request.GetRequestId()),
		Attempts:  1,
	})

	return &e2ev1.ReadResponse{InstanceId: s.instanceID, Sequence: sequence}, nil
}

// Mutate applies one mutation per idempotency key and records only its SHA-256
// digest. Repeated calls return the original sequence without applying again.
func (s *Service) Mutate(
	ctx context.Context,
	request *e2ev1.MutateRequest,
) (*e2ev1.MutateResponse, error) {
	if err := validateRequestID(request.GetRequestId()); err != nil {
		return nil, err
	}
	key, err := idempotencyKey(ctx)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(key)

	s.mu.Lock()
	defer s.mu.Unlock()
	if index, found := s.mutations[digest]; found {
		entry := s.entries[index]
		if !bytes.Equal(entry.GetRequestId(), request.GetRequestId()) {
			return nil, status.Error(codes.FailedPrecondition, "idempotency key conflict")
		}
		if entry.GetAttempts() == math.MaxUint32 {
			return nil, status.Error(codes.ResourceExhausted, "mutation attempts exhausted")
		}
		entry.Attempts++

		return &e2ev1.MutateResponse{
			InstanceId: s.instanceID,
			Sequence:   entry.GetSequence(),
			Duplicate:  true,
		}, nil
	}
	if len(s.entries) >= s.maxEntries {
		return nil, status.Error(codes.ResourceExhausted, "ledger capacity exhausted")
	}

	s.next++
	sequence := s.next
	s.entries = append(s.entries, &e2ev1.LedgerEntry{
		Sequence:             sequence,
		Operation:            e2ev1.Operation_OPERATION_MUTATE,
		RequestId:            slices.Clone(request.GetRequestId()),
		IdempotencyKeySha256: slices.Clone(digest[:]),
		Attempts:             1,
	})
	s.mutations[digest] = len(s.entries) - 1

	return &e2ev1.MutateResponse{InstanceId: s.instanceID, Sequence: sequence}, nil
}

// Ledger returns a defensive copy of the most recent bounded entries.
func (s *Service) Ledger(
	_ context.Context,
	request *e2ev1.LedgerRequest,
) (*e2ev1.LedgerResponse, error) {
	limit := int(request.GetLimit())
	if limit <= 0 || limit > s.maxEntries {
		return nil, status.Error(codes.InvalidArgument, "ledger limit is outside bounds")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	start := max(len(s.entries)-limit, 0)
	entries := make([]*e2ev1.LedgerEntry, 0, len(s.entries)-start)
	for _, entry := range s.entries[start:] {
		entries = append(entries, &e2ev1.LedgerEntry{
			Sequence:             entry.GetSequence(),
			Operation:            entry.GetOperation(),
			RequestId:            slices.Clone(entry.GetRequestId()),
			IdempotencyKeySha256: slices.Clone(entry.GetIdempotencyKeySha256()),
			Attempts:             entry.GetAttempts(),
		})
	}

	return &e2ev1.LedgerResponse{InstanceId: s.instanceID, Entries: entries}, nil
}

func validateRequestID(requestID []byte) error {
	if len(requestID) != requestIDBytes {
		return status.Error(codes.InvalidArgument, "request id must be 16 bytes")
	}

	return nil
}

func idempotencyKey(ctx context.Context) ([]byte, error) {
	if ctx == nil {
		return nil, status.Error(codes.InvalidArgument, "idempotency key is required")
	}
	incoming, found := metadata.FromIncomingContext(ctx)
	if !found {
		return nil, status.Error(codes.InvalidArgument, "idempotency key is required")
	}
	values := incoming.Get(protocolv1.InternalIdempotencyKeyMetadata)
	if len(values) != 1 {
		return nil, status.Error(codes.InvalidArgument, "idempotency key is required")
	}
	key := []byte(values[0])
	if len(key) == 0 || len(key) > protocolv1.MaxIdempotencyKeyBytes {
		return nil, status.Error(codes.InvalidArgument, "idempotency key is outside bounds")
	}

	return slices.Clone(key), nil
}
