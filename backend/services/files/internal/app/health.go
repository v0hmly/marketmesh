package app

import (
	"context"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// Health is reachable only over the same scoped mTLS listener as file control.
type filesHealth struct {
	grpc_health_v1.UnimplementedHealthServer
	ready func(context.Context) error
}

func (h *filesHealth) Check(ctx context.Context, _ *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	state := grpc_health_v1.HealthCheckResponse_SERVING
	if h.ready(ctx) != nil {
		state = grpc_health_v1.HealthCheckResponse_NOT_SERVING
	}
	return &grpc_health_v1.HealthCheckResponse{Status: state}, nil
}
