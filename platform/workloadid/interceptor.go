package workloadid

import (
	"context"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// InterceptorOption настраивает серверные interceptor'ы авторизации.
type InterceptorOption func(*interceptorConfig)

type interceptorConfig struct {
	revocation RevocationList
	onVerify   func(Identity, error)
}

// WithRevocationList подключает список отзыва: вызов с отозванным
// сертификатом (по серийному номеру leaf) отклоняется как Unauthenticated.
func WithRevocationList(list RevocationList) InterceptorOption {
	return func(cfg *interceptorConfig) { cfg.revocation = list }
}

// WithOnVerify задаёт колбэк, вызываемый на каждое решение interceptor'а:
// идентичность пира (нулевая, если аутентификация не пройдена) и итоговая
// ошибка (nil при успехе). Предназначен для метрик проверки по ADR-0004.
func WithOnVerify(callback func(Identity, error)) InterceptorOption {
	return func(cfg *interceptorConfig) { cfg.onVerify = callback }
}

// UnaryServerInterceptor возвращает unary interceptor, который извлекает
// идентичность пира из контекста, проверяет отзыв и применяет политику:
// отсутствие или недействительность идентичности — codes.Unauthenticated,
// отсутствие разрешения на метод — codes.PermissionDenied. Тексты ошибок
// содержат только безопасные метки и не раскрывают чужие правила.
func UnaryServerInterceptor(policy *Policy, opts ...InterceptorOption) grpcgo.UnaryServerInterceptor {
	cfg := newInterceptorConfig(opts)

	return func(
		ctx context.Context,
		request any,
		info *grpcgo.UnaryServerInfo,
		handler grpcgo.UnaryHandler,
	) (any, error) {
		identity, err := cfg.authorize(ctx, policy, info.FullMethod)
		cfg.report(identity, err)
		if err != nil {
			return nil, err
		}

		return handler(ctx, request)
	}
}

// StreamServerInterceptor возвращает stream interceptor с той же семантикой,
// что и UnaryServerInterceptor.
func StreamServerInterceptor(policy *Policy, opts ...InterceptorOption) grpcgo.StreamServerInterceptor {
	cfg := newInterceptorConfig(opts)

	return func(
		service any,
		stream grpcgo.ServerStream,
		info *grpcgo.StreamServerInfo,
		handler grpcgo.StreamHandler,
	) error {
		identity, err := cfg.authorize(stream.Context(), policy, info.FullMethod)
		cfg.report(identity, err)
		if err != nil {
			return err
		}

		return handler(service, stream)
	}
}

func newInterceptorConfig(opts []InterceptorOption) interceptorConfig {
	var cfg interceptorConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	return cfg
}

// authorize выполняет полную цепочку проверки вызова: идентичность пира,
// отзыв сертификата и разрешение метода политикой.
func (cfg interceptorConfig) authorize(
	ctx context.Context,
	policy *Policy,
	fullMethod string,
) (Identity, error) {
	identity, leaf, err := FromContext(ctx)
	if err != nil {
		return Identity{}, status.Error(codes.Unauthenticated, "workloadid: unauthenticated peer")
	}
	if cfg.revocation != nil && cfg.revocation.Revoked(SerialString(leaf.SerialNumber)) {
		return identity, status.Error(codes.Unauthenticated, "workloadid: revoked certificate")
	}
	if !policy.Allow(identity, fullMethod) {
		return identity, status.Error(
			codes.PermissionDenied,
			"workloadid: method "+fullMethod+" is not allowed for this workload",
		)
	}

	return identity, nil
}

func (cfg interceptorConfig) report(identity Identity, err error) {
	if cfg.onVerify != nil {
		cfg.onVerify(identity, err)
	}
}
