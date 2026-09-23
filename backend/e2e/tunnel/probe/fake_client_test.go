package probe

import (
	"context"
	"crypto/sha256"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	e2ev1 "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1"
	"google.golang.org/grpc"
)

func TestFakeInvokerCallsReadAndMutateWithoutRetry(t *testing.T) {
	t.Parallel()

	directory, err := NewInstanceDirectory([]Instance{
		{Source: "fake-a-1", DataCenter: DataCenterA},
		{Source: "fake-b-1", DataCenter: DataCenterB},
	})
	if err != nil {
		t.Fatalf("NewInstanceDirectory() error = %v", err)
	}
	var readCalls atomic.Uint32
	var mutateCalls atomic.Uint32
	client := fakeTrafficClient{
		read: func(
			_ context.Context,
			request *connect.Request[e2ev1.ReadRequest],
		) (*connect.Response[e2ev1.ReadResponse], error) {
			readCalls.Add(1)
			if got := request.Msg.GetRequestId(); !slices.Equal(got, requestIDBytes(1)) {
				t.Fatalf("Read request ID = %x", got)
			}
			return connect.NewResponse(&e2ev1.ReadResponse{
				InstanceId: "fake-a-1",
				Sequence:   11,
			}), nil
		},
		mutate: func(
			_ context.Context,
			request *connect.Request[e2ev1.MutateRequest],
		) (*connect.Response[e2ev1.MutateResponse], error) {
			mutateCalls.Add(1)
			if got := request.Header().Values("Idempotency-Key"); !slices.Equal(got, []string{requestID2}) {
				t.Fatalf("Idempotency-Key = %q", got)
			}
			return connect.NewResponse(&e2ev1.MutateResponse{
				InstanceId: "fake-b-1",
				Sequence:   12,
				Duplicate:  true,
			}), nil
		},
	}
	invoker, err := NewFakeInvoker(client, directory)
	if err != nil {
		t.Fatalf("NewFakeInvoker() error = %v", err)
	}

	read := invoker.Invoke(t.Context(), Request{
		ID: requestID1, Class: TrafficClassRead, Sequence: 1,
	})
	if read.Outcome != OutcomeSuccess || read.RouteID != FakeReadRoute ||
		read.DataCenter != DataCenterA || read.Source != "fake-a-1" ||
		read.InternalSequence != 11 {
		t.Fatalf("read response = %#v", read)
	}
	mutating := invoker.Invoke(t.Context(), Request{
		ID: requestID2, IdempotencyKey: requestID2,
		Class: TrafficClassMutating, Sequence: 1,
	})
	if mutating.Outcome != OutcomeSuccess || mutating.RouteID != FakeMutatingRoute ||
		mutating.DataCenter != DataCenterB || !mutating.Duplicate ||
		mutating.InternalSequence != 12 {
		t.Fatalf("mutating response = %#v", mutating)
	}
	if readCalls.Load() != 1 || mutateCalls.Load() != 1 {
		t.Fatalf("calls = read %d mutate %d, want exactly one each", readCalls.Load(), mutateCalls.Load())
	}
}

func TestFakeInvokerMapsErrorsAndInvalidMetadataSafely(t *testing.T) {
	t.Parallel()

	directory, err := NewInstanceDirectory([]Instance{{
		Source: "fake-a-1", DataCenter: DataCenterA,
	}})
	if err != nil {
		t.Fatalf("NewInstanceDirectory() error = %v", err)
	}
	client := fakeTrafficClient{
		read: func(
			context.Context,
			*connect.Request[e2ev1.ReadRequest],
		) (*connect.Response[e2ev1.ReadResponse], error) {
			return nil, connect.NewError(
				connect.CodeUnavailable,
				errors.New("secret endpoint and payload"),
			)
		},
		mutate: func(
			context.Context,
			*connect.Request[e2ev1.MutateRequest],
		) (*connect.Response[e2ev1.MutateResponse], error) {
			return connect.NewResponse(&e2ev1.MutateResponse{
				InstanceId: "unknown-instance", Sequence: 1,
			}), nil
		},
	}
	invoker, err := NewFakeInvoker(client, directory)
	if err != nil {
		t.Fatalf("NewFakeInvoker() error = %v", err)
	}

	read := invoker.Invoke(t.Context(), Request{
		ID: requestID1, Class: TrafficClassRead, Sequence: 1,
	})
	if read != (Response{Outcome: OutcomeUnavailable}) {
		t.Fatalf("read response = %#v", read)
	}
	mutating := invoker.Invoke(t.Context(), Request{
		ID: requestID2, IdempotencyKey: requestID2,
		Class: TrafficClassMutating, Sequence: 1,
	})
	if mutating != (Response{Outcome: OutcomeInvalidMetadata}) {
		t.Fatalf("mutating response = %#v", mutating)
	}
}

func TestFakeInvokerRejectsNilSuccessfulResponse(t *testing.T) {
	t.Parallel()

	directory, err := NewInstanceDirectory([]Instance{{
		Source: "fake-a-1", DataCenter: DataCenterA,
	}})
	if err != nil {
		t.Fatalf("NewInstanceDirectory() error = %v", err)
	}
	client := fakeTrafficClient{
		read: func(
			context.Context,
			*connect.Request[e2ev1.ReadRequest],
		) (*connect.Response[e2ev1.ReadResponse], error) {
			return nil, nil
		},
		mutate: func(
			context.Context,
			*connect.Request[e2ev1.MutateRequest],
		) (*connect.Response[e2ev1.MutateResponse], error) {
			return nil, nil
		},
	}
	invoker, err := NewFakeInvoker(client, directory)
	if err != nil {
		t.Fatalf("NewFakeInvoker() error = %v", err)
	}

	if response := invoker.Invoke(t.Context(), Request{
		ID: requestID1, Class: TrafficClassRead, Sequence: 1,
	}); response != (Response{Outcome: OutcomeInvalidMetadata}) {
		t.Fatalf("read response = %#v", response)
	}
	if response := invoker.Invoke(t.Context(), Request{
		ID: requestID2, IdempotencyKey: requestID2,
		Class: TrafficClassMutating, Sequence: 1,
	}); response != (Response{Outcome: OutcomeInvalidMetadata}) {
		t.Fatalf("mutating response = %#v", response)
	}
}

func TestFakeInvokerUsesDynamicInstanceResolver(t *testing.T) {
	t.Parallel()

	resolver := &mutableInstanceResolver{instances: map[string]DataCenter{
		"fake-a-old": DataCenterA,
	}}
	client := fakeTrafficClient{
		read: func(
			context.Context,
			*connect.Request[e2ev1.ReadRequest],
		) (*connect.Response[e2ev1.ReadResponse], error) {
			return connect.NewResponse(&e2ev1.ReadResponse{
				InstanceId: "fake-a-new",
				Sequence:   7,
			}), nil
		},
	}
	invoker, err := NewFakeInvokerWithResolver(client, resolver)
	if err != nil {
		t.Fatalf("NewFakeInvokerWithResolver() error = %v", err)
	}

	request := Request{ID: requestID1, Class: TrafficClassRead, Sequence: 1}
	if response := invoker.Invoke(t.Context(), request); response.Outcome != OutcomeInvalidMetadata {
		t.Fatalf("Invoke() before discovery = %#v", response)
	}
	resolver.add("fake-a-new", DataCenterA)
	response := invoker.Invoke(t.Context(), request)
	if response.Outcome != OutcomeSuccess || response.DataCenter != DataCenterA ||
		response.Source != "fake-a-new" || response.InternalSequence != 7 {
		t.Fatalf("Invoke() after discovery = %#v", response)
	}
}

func TestLedgerCollectorDiscoversAndCollectsBoundedSnapshots(t *testing.T) {
	t.Parallel()

	digest := sha256.Sum256([]byte(requestID2))
	collector, err := NewLedgerCollector([]LedgerSource{
		{
			DataCenter: DataCenterB,
			Client: fakeLedgerClient{response: &e2ev1.LedgerResponse{
				InstanceId: "fake-b-1",
				Entries: []*e2ev1.LedgerEntry{{
					Sequence: 2, Operation: e2ev1.Operation_OPERATION_MUTATE,
					RequestId: requestIDBytes(2), IdempotencyKeySha256: digest[:],
					Attempts: 1,
				}},
			}},
		},
		{
			DataCenter: DataCenterA,
			Client: fakeLedgerClient{response: &e2ev1.LedgerResponse{
				InstanceId: "fake-a-1",
				Entries: []*e2ev1.LedgerEntry{{
					Sequence: 1, Operation: e2ev1.Operation_OPERATION_READ,
					RequestId: requestIDBytes(1), Attempts: 1,
				}},
			}},
		},
	}, 4)
	if err != nil {
		t.Fatalf("NewLedgerCollector() error = %v", err)
	}

	directory, err := collector.Discover(t.Context())
	if err != nil {
		t.Fatalf("LedgerCollector.Discover() error = %v", err)
	}
	if dc, found := directory.Resolve("fake-a-1"); !found || dc != DataCenterA {
		t.Fatalf("Resolve(fake-a-1) = %q, %v", dc, found)
	}
	snapshot := collector.Collect(t.Context())
	if !snapshot.IsComplete || len(snapshot.IncompleteReasons) != 0 {
		t.Fatalf("snapshot completeness = %v, reasons %v", snapshot.IsComplete, snapshot.IncompleteReasons)
	}
	if len(snapshot.Records) != 2 {
		t.Fatalf("record count = %d, want 2", len(snapshot.Records))
	}
	if snapshot.Records[0].DataCenter != DataCenterA ||
		snapshot.Records[0].RequestID != requestID1 ||
		snapshot.Records[1].DataCenter != DataCenterB ||
		snapshot.Records[1].IdempotencyKeySHA256 != digestString(requestID2) {
		t.Fatalf("records = %#v", snapshot.Records)
	}
}

func TestLedgerCollectorFailsClosedWithoutRawErrors(t *testing.T) {
	t.Parallel()

	collector, err := NewLedgerCollector([]LedgerSource{{
		DataCenter: DataCenterA,
		Client:     fakeLedgerClient{err: errors.New("token=must-not-leak")},
	}}, 1)
	if err != nil {
		t.Fatalf("NewLedgerCollector() error = %v", err)
	}

	snapshot := collector.Collect(t.Context())
	if snapshot.IsComplete {
		t.Fatal("snapshot is complete after RPC failure")
	}
	assertStrings(t, snapshot.IncompleteReasons, []string{"ledger_rpc_failed"})
	if len(snapshot.Records) != 0 {
		t.Fatalf("records = %#v, want empty", snapshot.Records)
	}
}

func TestLedgerCollectorFailsClosedWhenClientPanics(t *testing.T) {
	t.Parallel()

	collector, err := NewLedgerCollector([]LedgerSource{{
		DataCenter: DataCenterA,
		Client:     panicLedgerClient{},
	}}, 4)
	if err != nil {
		t.Fatalf("NewLedgerCollector() error = %v", err)
	}

	snapshot := collector.Collect(t.Context())
	if snapshot.IsComplete {
		t.Fatal("snapshot is complete after client panic")
	}
	assertStrings(t, snapshot.IncompleteReasons, []string{"ledger_rpc_failed"})
}

func TestLedgerCollectorMarksLimitAndInvalidEntryIncomplete(t *testing.T) {
	t.Parallel()

	collector, err := NewLedgerCollector([]LedgerSource{{
		DataCenter: DataCenterA,
		Client: fakeLedgerClient{response: &e2ev1.LedgerResponse{
			InstanceId: "fake-a-1",
			Entries: []*e2ev1.LedgerEntry{{
				Sequence: 1, Operation: e2ev1.Operation_OPERATION_READ,
				RequestId: []byte("short"), Attempts: 1,
			}},
		}},
	}}, 1)
	if err != nil {
		t.Fatalf("NewLedgerCollector() error = %v", err)
	}

	snapshot := collector.Collect(t.Context())
	if snapshot.IsComplete {
		t.Fatal("snapshot is complete after truncation and invalid entry")
	}
	assertStrings(
		t,
		snapshot.IncompleteReasons,
		[]string{"ledger_limit_reached", "ledger_entry_invalid"},
	)
}

func TestNewInstanceDirectoryRejectsCrossDCInstanceConflict(t *testing.T) {
	t.Parallel()

	_, err := NewInstanceDirectory([]Instance{
		{Source: "same-instance", DataCenter: DataCenterA},
		{Source: "same-instance", DataCenter: DataCenterB},
	})
	if err == nil {
		t.Fatal("NewInstanceDirectory() error = nil for cross-DC conflict")
	}
}

func FuzzInternalRecordRejectsMalformedLedgerEntry(f *testing.F) {
	f.Add(
		requestIDBytes(1),
		[]byte{},
		int32(e2ev1.Operation_OPERATION_READ),
		uint64(1),
		uint32(1),
	)
	digest := sha256.Sum256([]byte(requestID2))
	f.Add(
		requestIDBytes(2),
		digest[:],
		int32(e2ev1.Operation_OPERATION_MUTATE),
		uint64(2),
		uint32(1),
	)
	f.Fuzz(func(
		t *testing.T,
		requestID []byte,
		idempotencyDigest []byte,
		operation int32,
		sequence uint64,
		attempts uint32,
	) {
		record, valid := internalRecord(&e2ev1.LedgerEntry{
			RequestId:            requestID,
			IdempotencyKeySha256: idempotencyDigest,
			Operation:            e2ev1.Operation(operation),
			Sequence:             sequence,
			Attempts:             attempts,
		}, "fake-a-1", DataCenterA)
		if valid && !validInternalRecord(record) {
			t.Fatalf("internalRecord() returned invalid record %#v", record)
		}
	})
}

func TestLedgerCollectorRejectsNilContext(t *testing.T) {
	t.Parallel()

	collector, err := NewLedgerCollector([]LedgerSource{{
		DataCenter: DataCenterA,
		Client: fakeLedgerClient{response: &e2ev1.LedgerResponse{
			InstanceId: "fake-a-1",
		}},
	}}, 4)
	if err != nil {
		t.Fatalf("NewLedgerCollector() error = %v", err)
	}

	//lint:ignore SA1012 This test verifies the explicit fail-closed nil context contract.
	if _, err := collector.Discover(nil); err == nil { //nolint:staticcheck // Explicit nil contract test.
		t.Fatal("LedgerCollector.Discover(nil) error = nil")
	}
	//lint:ignore SA1012 This test verifies the explicit fail-closed nil context contract.
	snapshot := collector.Collect(nil) //nolint:staticcheck // Explicit nil contract test.
	assertStrings(t, snapshot.IncompleteReasons, []string{"ledger_context_invalid"})
}

func TestLedgerCollectorAddsDeadlineWhenCallerHasNone(t *testing.T) {
	t.Parallel()

	client := &deadlineObservingLedgerClient{response: &e2ev1.LedgerResponse{
		InstanceId: "fake-a-1",
	}}
	collector, err := NewLedgerCollector([]LedgerSource{{
		DataCenter: DataCenterA,
		Client:     client,
	}}, 4)
	if err != nil {
		t.Fatalf("NewLedgerCollector() error = %v", err)
	}

	if _, err := collector.Discover(context.Background()); err != nil {
		t.Fatalf("LedgerCollector.Discover() error = %v", err)
	}
	snapshot := collector.Collect(context.Background())
	if !snapshot.IsComplete {
		t.Fatalf("LedgerCollector.Collect() incomplete reasons = %v", snapshot.IncompleteReasons)
	}
	if calls := client.calls.Load(); calls != 2 {
		t.Fatalf("Ledger() calls = %d, want 2", calls)
	}
	if client.sawCallWithoutDeadline.Load() {
		t.Fatal("Ledger() received a context without deadline")
	}
}

func TestLedgerCollectorBoundsHungEndpointWithoutCallerDeadline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call func(*testing.T, *LedgerCollector)
	}{
		{
			name: "discover",
			call: func(t *testing.T, collector *LedgerCollector) {
				t.Helper()
				if _, err := collector.Discover(context.Background()); err == nil {
					t.Fatal("LedgerCollector.Discover() error = nil for timed out endpoint")
				}
			},
		},
		{
			name: "collect",
			call: func(t *testing.T, collector *LedgerCollector) {
				t.Helper()
				snapshot := collector.Collect(context.Background())
				if snapshot.IsComplete {
					t.Fatal("LedgerCollector.Collect() complete after endpoint timeout")
				}
				assertStrings(t, snapshot.IncompleteReasons, []string{"ledger_rpc_failed"})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := &cancelAwareLedgerClient{returned: make(chan struct{})}
			collector, err := NewLedgerCollector([]LedgerSource{{
				DataCenter: DataCenterA,
				Client:     client,
			}}, 4)
			if err != nil {
				t.Fatalf("NewLedgerCollector() error = %v", err)
			}
			collector.readTimeout = 20 * time.Millisecond

			startedAt := time.Now()
			test.call(t, collector)
			if elapsed := time.Since(startedAt); elapsed > time.Second {
				t.Fatalf("ledger call elapsed = %s, want bounded return", elapsed)
			}
			select {
			case <-client.returned:
			default:
				t.Fatal("ledger client goroutine did not return after timeout")
			}
		})
	}
}

type fakeTrafficClient struct {
	read func(
		context.Context,
		*connect.Request[e2ev1.ReadRequest],
	) (*connect.Response[e2ev1.ReadResponse], error)
	mutate func(
		context.Context,
		*connect.Request[e2ev1.MutateRequest],
	) (*connect.Response[e2ev1.MutateResponse], error)
}

type mutableInstanceResolver struct {
	mu        sync.RWMutex
	instances map[string]DataCenter
}

func (resolver *mutableInstanceResolver) Resolve(source string) (DataCenter, bool) {
	resolver.mu.RLock()
	defer resolver.mu.RUnlock()

	dataCenter, found := resolver.instances[source]
	return dataCenter, found
}

func (resolver *mutableInstanceResolver) add(source string, dataCenter DataCenter) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()

	resolver.instances[source] = dataCenter
}

func (client fakeTrafficClient) Read(
	ctx context.Context,
	request *connect.Request[e2ev1.ReadRequest],
) (*connect.Response[e2ev1.ReadResponse], error) {
	return client.read(ctx, request)
}

func (client fakeTrafficClient) Mutate(
	ctx context.Context,
	request *connect.Request[e2ev1.MutateRequest],
) (*connect.Response[e2ev1.MutateResponse], error) {
	return client.mutate(ctx, request)
}

type fakeLedgerClient struct {
	response *e2ev1.LedgerResponse
	err      error
}

type deadlineObservingLedgerClient struct {
	response               *e2ev1.LedgerResponse
	calls                  atomic.Uint32
	sawCallWithoutDeadline atomic.Bool
}

type cancelAwareLedgerClient struct {
	returned chan struct{}
}

type panicLedgerClient struct{}

func (panicLedgerClient) Ledger(
	context.Context,
	*e2ev1.LedgerRequest,
	...grpc.CallOption,
) (*e2ev1.LedgerResponse, error) {
	panic("sensitive panic value")
}

func (client fakeLedgerClient) Ledger(
	context.Context,
	*e2ev1.LedgerRequest,
	...grpc.CallOption,
) (*e2ev1.LedgerResponse, error) {
	return client.response, client.err
}

func (client *deadlineObservingLedgerClient) Ledger(
	ctx context.Context,
	_ *e2ev1.LedgerRequest,
	_ ...grpc.CallOption,
) (*e2ev1.LedgerResponse, error) {
	client.calls.Add(1)
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		client.sawCallWithoutDeadline.Store(true)
	}
	return client.response, nil
}

func (client *cancelAwareLedgerClient) Ledger(
	ctx context.Context,
	_ *e2ev1.LedgerRequest,
	_ ...grpc.CallOption,
) (*e2ev1.LedgerResponse, error) {
	<-ctx.Done()
	close(client.returned)
	return nil, ctx.Err()
}

func requestIDBytes(value byte) []byte {
	requestID := make([]byte, requestIDSize)
	requestID[len(requestID)-1] = value
	return requestID
}
