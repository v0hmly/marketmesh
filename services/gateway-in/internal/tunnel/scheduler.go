package tunnel

import (
	"context"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
)

const schedulerLanes = 4

type outboundQueue struct {
	lanes           [schedulerLanes]chan *contractv1.ConnectResponse
	wake            chan struct{}
	next            int
	instrumentation *instrumentation
}

func newOutboundQueue(limits QueueLimits, instrumentation *instrumentation) *outboundQueue {
	queue := &outboundQueue{
		wake:            make(chan struct{}, 1),
		instrumentation: instrumentation,
	}
	queue.lanes[0] = make(chan *contractv1.ConnectResponse, limits.TunnelControl)
	queue.lanes[1] = make(chan *contractv1.ConnectResponse, limits.ControlAuth)
	queue.lanes[2] = make(chan *contractv1.ConnectResponse, limits.Regular)
	queue.lanes[3] = make(chan *contractv1.ConnectResponse, limits.Realtime)

	return queue
}

func (q *outboundQueue) enqueue(ctx context.Context, frame *contractv1.ConnectResponse) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	class := frame.GetHeader().GetTrafficClass()
	lane, valid := queueLane(class)
	if !valid {
		return ErrProtocolViolation
	}
	select {
	case q.lanes[lane] <- frame:
		q.instrumentation.queueDelta(ctx, class, 1)
		select {
		case q.wake <- struct{}{}:
		default:
		}
		return nil
	default:
		return ErrQueueFull
	}
}

func (q *outboundQueue) dequeue(ctx context.Context) (*contractv1.ConnectResponse, error) {
	for {
		for offset := range schedulerLanes {
			lane := (q.next + offset) % schedulerLanes
			select {
			case frame := <-q.lanes[lane]:
				q.next = (lane + 1) % schedulerLanes
				q.instrumentation.queueDelta(
					ctx,
					frame.GetHeader().GetTrafficClass(),
					-1,
				)
				return frame, nil
			default:
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-q.wake:
		}
	}
}

func (q *outboundQueue) discard(ctx context.Context) {
lanes:
	for lane := range schedulerLanes {
		for {
			select {
			case frame := <-q.lanes[lane]:
				q.instrumentation.queueDelta(
					ctx,
					frame.GetHeader().GetTrafficClass(),
					-1,
				)
			default:
				continue lanes
			}
		}
	}
}

func queueLane(class contractv1.TrafficClass) (int, bool) {
	switch class {
	case contractv1.TrafficClass_TRAFFIC_CLASS_UNSPECIFIED:
		return 0, true
	case contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH:
		return 1, true
	case contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR:
		return 2, true
	case contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME:
		return 3, true
	default:
		return 0, false
	}
}
