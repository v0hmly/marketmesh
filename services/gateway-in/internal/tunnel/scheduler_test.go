package tunnel

import (
	"context"
	"errors"
	"testing"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
)

func TestOutboundQueue_RealtimeOverflowDoesNotBlockControlAuth(t *testing.T) {
	t.Parallel()

	instrumentation, err := newInstrumentation(nil, nil)
	if err != nil {
		t.Fatalf("newInstrumentation() error = %v", err)
	}
	queue := newOutboundQueue(QueueLimits{
		TunnelControl: 2,
		ControlAuth:   2,
		Regular:       2,
		Realtime:      2,
	}, instrumentation)
	ctx := context.Background()
	for range 2 {
		if err := queue.enqueue(ctx, queueTestFrame(
			contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME,
		)); err != nil {
			t.Fatalf("enqueue realtime error = %v", err)
		}
	}
	if err := queue.enqueue(ctx, queueTestFrame(
		contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME,
	)); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("realtime overflow error = %v, want ErrQueueFull", err)
	}
	if err := queue.enqueue(ctx, queueTestFrame(
		contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
	)); err != nil {
		t.Fatalf("control/auth enqueue behind realtime overflow error = %v", err)
	}

	frame, err := queue.dequeue(ctx)
	if err != nil {
		t.Fatalf("dequeue() error = %v", err)
	}
	if frame.GetHeader().GetTrafficClass() !=
		contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH {
		t.Fatalf("first dequeued class = %s, want control/auth", frame.GetHeader().GetTrafficClass())
	}
}

func TestOutboundQueue_RoundRobinFairness(t *testing.T) {
	t.Parallel()

	instrumentation, err := newInstrumentation(nil, nil)
	if err != nil {
		t.Fatalf("newInstrumentation() error = %v", err)
	}
	queue := newOutboundQueue(QueueLimits{
		TunnelControl: 2,
		ControlAuth:   2,
		Regular:       2,
		Realtime:      2,
	}, instrumentation)
	ctx := context.Background()
	classes := []contractv1.TrafficClass{
		contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
		contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
		contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME,
	}
	for range 2 {
		for _, class := range classes {
			if err := queue.enqueue(ctx, queueTestFrame(class)); err != nil {
				t.Fatalf("enqueue %s error = %v", class, err)
			}
		}
	}
	for index := range 6 {
		frame, err := queue.dequeue(ctx)
		if err != nil {
			t.Fatalf("dequeue() error = %v", err)
		}
		expectedClass := classes[index%len(classes)]
		if frame.GetHeader().GetTrafficClass() != expectedClass {
			t.Fatalf(
				"dequeue %d class = %s, want %s",
				index,
				frame.GetHeader().GetTrafficClass(),
				expectedClass,
			)
		}
	}
}

func queueTestFrame(class contractv1.TrafficClass) *contractv1.ConnectResponse {
	return &contractv1.ConnectResponse{
		Header: &contractv1.FrameHeader{TrafficClass: class},
	}
}
