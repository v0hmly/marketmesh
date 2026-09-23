package probe

import (
	"context"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	e2ev1 "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1"
	e2ev1connect "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1/e2ev1connect"
)

func TestFrontDoorInvokerUsesPublishedReadAndMutateSurface(t *testing.T) {
	t.Parallel()

	_, handler := e2ev1connect.NewFakeInternalServiceHandler(frontDoorService{})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	directory, err := NewInstanceDirectory([]Instance{
		{Source: "fake-a-1", DataCenter: DataCenterA},
		{Source: "fake-b-1", DataCenter: DataCenterB},
	})
	if err != nil {
		t.Fatalf("NewInstanceDirectory() error = %v", err)
	}
	invoker, err := NewFrontDoorInvoker(server.URL, directory)
	if err != nil {
		t.Fatalf("NewFrontDoorInvoker() error = %v", err)
	}
	t.Cleanup(invoker.Close)

	read := invoker.Invoke(t.Context(), Request{
		ID: requestID1, Class: TrafficClassRead, Sequence: 1,
	})
	if read.Outcome != OutcomeSuccess || read.DataCenter != DataCenterA ||
		read.Source != "fake-a-1" || read.InternalSequence != 1 {
		t.Fatalf("read response = %#v", read)
	}
	mutating := invoker.Invoke(t.Context(), Request{
		ID: requestID2, IdempotencyKey: requestID2,
		Class: TrafficClassMutating, Sequence: 1,
	})
	if mutating.Outcome != OutcomeSuccess || mutating.DataCenter != DataCenterB ||
		mutating.Source != "fake-b-1" || mutating.InternalSequence != 2 {
		t.Fatalf("mutating response = %#v", mutating)
	}
}

func TestFrontDoorInvokerRejectsUnsafeEndpoint(t *testing.T) {
	t.Parallel()

	directory, err := NewInstanceDirectory([]Instance{{
		Source: "fake-a-1", DataCenter: DataCenterA,
	}})
	if err != nil {
		t.Fatalf("NewInstanceDirectory() error = %v", err)
	}
	tests := []string{
		"https://127.0.0.1:18080",
		"http://localhost:18080",
		"http://10.0.0.1:18080",
		"http://127.0.0.1:80",
		"http://127.0.0.1:18080/path",
		"http://user@127.0.0.1:18080",
		"http://127.0.0.1:18080?target=other",
	}
	for _, endpoint := range tests {
		t.Run(endpoint, func(t *testing.T) {
			t.Parallel()
			if _, err := NewFrontDoorInvoker(endpoint, directory); err == nil {
				t.Fatalf("NewFrontDoorInvoker(%q) error = nil", endpoint)
			}
		})
	}
}

type frontDoorService struct{}

func (frontDoorService) Read(
	context.Context,
	*connect.Request[e2ev1.ReadRequest],
) (*connect.Response[e2ev1.ReadResponse], error) {
	return connect.NewResponse(&e2ev1.ReadResponse{
		InstanceId: "fake-a-1",
		Sequence:   1,
	}), nil
}

func (frontDoorService) Mutate(
	_ context.Context,
	request *connect.Request[e2ev1.MutateRequest],
) (*connect.Response[e2ev1.MutateResponse], error) {
	if request.Header().Get("Idempotency-Key") != requestID2 {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}
	return connect.NewResponse(&e2ev1.MutateResponse{
		InstanceId: "fake-b-1",
		Sequence:   2,
	}), nil
}

func (frontDoorService) Ledger(
	context.Context,
	*connect.Request[e2ev1.LedgerRequest],
) (*connect.Response[e2ev1.LedgerResponse], error) {
	panic("front door adapter must not read ledger")
}
