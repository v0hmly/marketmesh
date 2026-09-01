package tunnel

import (
	"context"
	"errors"
	"math"
	"slices"
	"sync"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Call is a logical request addressed only by a finite, locally configured
// RouteId. It deliberately has no host, port, URL, or gRPC method field.
type Call struct {
	Route          contractv1.RouteId
	Payload        []byte
	IdempotencyKey []byte
	Metadata       []*contractv1.Metadata
}

// Response is the bounded logical response returned through gateway-out.
type Response struct {
	Payload  []byte
	Metadata []*contractv1.Metadata
}

type logicalRequest struct {
	session  *session
	id       [16]byte
	route    contractv1.RouteId
	policy   RoutePolicy
	deadline time.Time
	started  time.Time

	done        chan struct{}
	creditReady chan struct{}
	mu          sync.Mutex

	outboundSequence uint64
	inboundSequence  uint64
	sendCredit       uint64
	receiveCredit    uint64
	responsePayload  []byte
	isResponseClosed bool
	isCompleted      bool
	response         Response
	err              error
}

func newLogicalRequest(
	session *session,
	id [16]byte,
	route contractv1.RouteId,
	policy RoutePolicy,
	deadline time.Time,
) *logicalRequest {
	return &logicalRequest{
		session:         session,
		id:              id,
		route:           route,
		policy:          policy,
		deadline:        deadline,
		started:         time.Now(),
		done:            make(chan struct{}),
		creditReady:     make(chan struct{}, 1),
		responsePayload: []byte{},
		response: Response{
			Payload:  []byte{},
			Metadata: []*contractv1.Metadata{},
		},
	}
}

func (r *logicalRequest) sendOpen(ctx context.Context, call Call) error {
	open := &contractv1.Open{
		RouteId:        call.Route,
		Deadline:       timestamppb.New(r.deadline),
		IdempotencyKey: slices.Clone(call.IdempotencyKey),
		Metadata:       cloneMetadata(call.Metadata),
	}
	if err := r.enqueuePayload(ctx, &contractv1.ConnectResponse_Open{Open: open}); err != nil {
		return err
	}

	credit := min(
		r.session.settings.initialResponseCredit,
		uint32(r.policy.MaxResponseBytes),
		r.session.limits.GetMaxCreditBytes(),
	)
	r.mu.Lock()
	r.receiveCredit = uint64(credit)
	r.mu.Unlock()

	return r.enqueuePayload(
		ctx,
		&contractv1.ConnectResponse_Credit{Credit: &contractv1.Credit{Bytes: credit}},
	)
}

func (r *logicalRequest) sendBody(ctx context.Context, payload []byte) error {
	remaining := payload
	for len(remaining) > 0 {
		chunkBytes, err := r.takeSendCredit(ctx, len(remaining))
		if err != nil {
			return err
		}
		chunk := slices.Clone(remaining[:chunkBytes])
		if err := r.enqueuePayload(
			ctx,
			&contractv1.ConnectResponse_Data{Data: &contractv1.Data{Payload: chunk}},
		); err != nil {
			return err
		}
		remaining = remaining[chunkBytes:]
	}

	return r.enqueuePayload(
		ctx,
		&contractv1.ConnectResponse_HalfClose{HalfClose: &contractv1.HalfClose{}},
	)
}

func (r *logicalRequest) wait(ctx context.Context) (Response, error) {
	select {
	case <-r.done:
		return r.outcome()
	case <-ctx.Done():
		r.cancel(ctx.Err())
		return r.outcome()
	}
}

func (r *logicalRequest) cancel(err error) {
	reason := contractv1.CancelReason_CANCEL_REASON_CALLER
	if errors.Is(err, context.DeadlineExceeded) {
		reason = contractv1.CancelReason_CANCEL_REASON_DEADLINE
	}
	_ = r.enqueuePayload(
		r.session.ctx,
		&contractv1.ConnectResponse_Cancel{
			Cancel: &contractv1.Cancel{Reason: reason},
		},
	)
	r.complete(Response{}, err)
}

func (r *logicalRequest) takeSendCredit(ctx context.Context, remaining int) (int, error) {
	for {
		r.mu.Lock()
		if r.isCompleted {
			err := r.err
			if err == nil {
				err = ErrTunnelClosed
			}
			r.mu.Unlock()
			return 0, err
		}
		if r.sendCredit > 0 {
			chunkBytes := min(
				uint64(remaining),
				r.sendCredit,
				uint64(r.session.limits.GetMaxDataBytes()),
				uint64(r.policy.MaxRequestBytes),
			)
			r.sendCredit -= chunkBytes
			r.mu.Unlock()
			return int(chunkBytes), nil
		}
		r.mu.Unlock()

		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-r.done:
			return 0, r.completedError()
		case <-r.creditReady:
		}
	}
}

func (r *logicalRequest) handleInbound(frame *contractv1.ConnectRequest) error {
	r.mu.Lock()
	if r.isCompleted {
		r.mu.Unlock()
		return nil
	}
	if frame.GetHeader().GetSequence() != r.inboundSequence+1 {
		r.mu.Unlock()
		return ErrProtocolViolation
	}
	r.inboundSequence++

	switch payload := frame.GetPayload().(type) {
	case *contractv1.ConnectRequest_Credit:
		credit := uint64(payload.Credit.GetBytes())
		maximumOutstanding := uint64(r.policy.MaxRequestBytes)
		if credit > math.MaxUint64-r.sendCredit || r.sendCredit+credit > maximumOutstanding {
			r.mu.Unlock()
			return ErrProtocolViolation
		}
		r.sendCredit += credit
		r.mu.Unlock()
		select {
		case r.creditReady <- struct{}{}:
		default:
		}
		return nil
	case *contractv1.ConnectRequest_Data:
		if r.isResponseClosed {
			r.mu.Unlock()
			return ErrProtocolViolation
		}
		dataBytes := uint64(len(payload.Data.GetPayload()))
		if dataBytes > r.receiveCredit ||
			len(r.responsePayload)+int(dataBytes) > r.policy.MaxResponseBytes {
			r.mu.Unlock()
			return ErrProtocolViolation
		}
		r.receiveCredit -= dataBytes
		r.responsePayload = append(r.responsePayload, payload.Data.GetPayload()...)
		remainingCapacity := uint64(r.policy.MaxResponseBytes - len(r.responsePayload))
		grant := min(dataBytes, remainingCapacity-r.receiveCredit)
		r.mu.Unlock()
		if grant == 0 {
			return nil
		}

		return r.grantReceiveCredit(uint32(grant))
	case *contractv1.ConnectRequest_HalfClose:
		if r.isResponseClosed {
			r.mu.Unlock()
			return ErrProtocolViolation
		}
		r.isResponseClosed = true
		r.mu.Unlock()
		return nil
	case *contractv1.ConnectRequest_Result:
		if !r.isResponseClosed {
			r.mu.Unlock()
			return ErrProtocolViolation
		}
		response := Response{
			Payload:  slices.Clone(r.responsePayload),
			Metadata: cloneMetadata(payload.Result.GetMetadata()),
		}
		var err error
		if payload.Result.GetCode() != contractv1.ResultCode_RESULT_CODE_OK {
			err = newResultError(payload.Result.GetCode())
		}
		r.completeLocked(response, err)
		r.mu.Unlock()
		return nil
	case *contractv1.ConnectRequest_Cancel:
		err := context.Canceled
		if payload.Cancel.GetReason() == contractv1.CancelReason_CANCEL_REASON_DEADLINE {
			err = context.DeadlineExceeded
		}
		r.completeLocked(Response{}, err)
		r.mu.Unlock()
		return nil
	default:
		r.mu.Unlock()
		return ErrProtocolViolation
	}
}

func (r *logicalRequest) grantReceiveCredit(bytes uint32) error {
	r.mu.Lock()
	if r.isCompleted {
		r.mu.Unlock()
		return nil
	}
	r.receiveCredit += uint64(bytes)
	r.mu.Unlock()

	return r.enqueuePayload(
		r.session.ctx,
		&contractv1.ConnectResponse_Credit{Credit: &contractv1.Credit{Bytes: bytes}},
	)
}

func (r *logicalRequest) enqueuePayload(
	ctx context.Context,
	payload any,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.isCompleted {
		if r.err != nil {
			return r.err
		}
		return ErrTunnelClosed
	}
	if r.outboundSequence == math.MaxUint64 {
		return ErrProtocolViolation
	}
	r.outboundSequence++
	frame := &contractv1.ConnectResponse{
		Header: &contractv1.FrameHeader{
			ProtocolVersion: protocolVersion,
			TunnelId:        slices.Clone(r.session.id[:]),
			RequestId:       slices.Clone(r.id[:]),
			Sequence:        r.outboundSequence,
			TrafficClass:    r.policy.TrafficClass,
		},
	}
	switch typedPayload := payload.(type) {
	case *contractv1.ConnectResponse_Open:
		frame.Payload = typedPayload
	case *contractv1.ConnectResponse_Data:
		frame.Payload = typedPayload
	case *contractv1.ConnectResponse_HalfClose:
		frame.Payload = typedPayload
	case *contractv1.ConnectResponse_Cancel:
		frame.Payload = typedPayload
	case *contractv1.ConnectResponse_Credit:
		frame.Payload = typedPayload
	default:
		return ErrProtocolViolation
	}

	return r.session.enqueueFrame(ctx, frame)
}

func (r *logicalRequest) complete(response Response, err error) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.completeLocked(response, err)
}

func (r *logicalRequest) completeLocked(response Response, err error) bool {
	if r.isCompleted {
		return false
	}
	r.isCompleted = true
	r.response = Response{
		Payload:  slices.Clone(response.Payload),
		Metadata: cloneMetadata(response.Metadata),
	}
	r.err = err
	close(r.done)

	return true
}

func (r *logicalRequest) outcome() (Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return Response{
		Payload:  slices.Clone(r.response.Payload),
		Metadata: cloneMetadata(r.response.Metadata),
	}, r.err
}

func (r *logicalRequest) completedError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}

	return ErrTunnelClosed
}

func cloneMetadata(values []*contractv1.Metadata) []*contractv1.Metadata {
	result := make([]*contractv1.Metadata, 0, len(values))
	for _, value := range values {
		if value == nil {
			result = append(result, nil)
			continue
		}
		result = append(result, &contractv1.Metadata{
			Key:   value.GetKey(),
			Value: slices.Clone(value.GetValue()),
		})
	}

	return result
}
