package grpc

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/v0hmly/marketmesh/platform/logger"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func unaryServerTimeoutInterceptor(timeout time.Duration) grpcgo.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		request any,
		_ *grpcgo.UnaryServerInfo,
		handler grpcgo.UnaryHandler,
	) (any, error) {
		requestCtx, cancel := contextWithMaximumTimeout(ctx, timeout)
		defer cancel()

		return handler(requestCtx, request)
	}
}

func streamServerTimeoutInterceptor(timeout time.Duration) grpcgo.StreamServerInterceptor {
	return func(
		service any,
		stream grpcgo.ServerStream,
		_ *grpcgo.StreamServerInfo,
		handler grpcgo.StreamHandler,
	) error {
		streamCtx, cancel := contextWithMaximumTimeout(stream.Context(), timeout)
		defer cancel()

		return handler(service, &serverStreamWithContext{
			ServerStream: stream,
			ctx:          streamCtx,
		})
	}
}

type serverStreamWithContext struct {
	grpcgo.ServerStream
	ctx context.Context
}

func (stream *serverStreamWithContext) Context() context.Context {
	return stream.ctx
}

func unaryServerRecoveryInterceptor(log *logger.Logger) grpcgo.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		request any,
		info *grpcgo.UnaryServerInfo,
		handler grpcgo.UnaryHandler,
	) (response any, resultErr error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(
					ctx,
					"gRPC unary panic перехвачен",
					logger.String("rpc_method", info.FullMethod),
					logger.String("panic_type", fmt.Sprintf("%T", recovered)),
				)
				response = nil
				resultErr = status.Error(codes.Internal, publicStatusMessage(codes.Internal))
			}
		}()

		return handler(ctx, request)
	}
}

func streamServerRecoveryInterceptor(log *logger.Logger) grpcgo.StreamServerInterceptor {
	return func(
		service any,
		stream grpcgo.ServerStream,
		info *grpcgo.StreamServerInfo,
		handler grpcgo.StreamHandler,
	) (resultErr error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(
					stream.Context(),
					"gRPC stream panic перехвачен",
					logger.String("rpc_method", info.FullMethod),
					logger.String("panic_type", fmt.Sprintf("%T", recovered)),
				)
				resultErr = status.Error(codes.Internal, publicStatusMessage(codes.Internal))
			}
		}()

		return handler(service, stream)
	}
}

func unaryServerLoggingInterceptor(log *logger.Logger) grpcgo.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		request any,
		info *grpcgo.UnaryServerInfo,
		handler grpcgo.UnaryHandler,
	) (any, error) {
		started := time.Now()
		response, err := handler(ctx, request)
		log.InfoContext(
			ctx,
			"gRPC unary запрос завершён",
			logger.String("rpc_method", info.FullMethod),
			logger.String("grpc_code", status.Code(err).String()),
			logger.Duration("duration", time.Since(started)),
		)

		return response, err
	}
}

func streamServerLoggingInterceptor(log *logger.Logger) grpcgo.StreamServerInterceptor {
	return func(
		service any,
		stream grpcgo.ServerStream,
		info *grpcgo.StreamServerInfo,
		handler grpcgo.StreamHandler,
	) error {
		started := time.Now()
		err := handler(service, stream)
		log.InfoContext(
			stream.Context(),
			"gRPC stream завершён",
			logger.String("rpc_method", info.FullMethod),
			logger.String("grpc_code", status.Code(err).String()),
			logger.Duration("duration", time.Since(started)),
		)

		return err
	}
}

func unaryServerStatusInterceptor(mapper ErrorCodeMapper) grpcgo.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		request any,
		_ *grpcgo.UnaryServerInfo,
		handler grpcgo.UnaryHandler,
	) (any, error) {
		response, err := handler(ctx, request)
		return response, sanitizedStatusError(err, mapper)
	}
}

func streamServerStatusInterceptor(mapper ErrorCodeMapper) grpcgo.StreamServerInterceptor {
	return func(
		service any,
		stream grpcgo.ServerStream,
		_ *grpcgo.StreamServerInfo,
		handler grpcgo.StreamHandler,
	) error {
		return sanitizedStatusError(handler(service, stream), mapper)
	}
}

func unaryClientDeadlineInterceptor(timeout time.Duration) grpcgo.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		request any,
		response any,
		connection *grpcgo.ClientConn,
		invoker grpcgo.UnaryInvoker,
		options ...grpcgo.CallOption,
	) error {
		callCtx, cancel := contextWithMaximumTimeout(ctx, timeout)
		defer cancel()

		return invoker(callCtx, method, request, response, connection, options...)
	}
}

func streamClientDeadlineInterceptor(timeout time.Duration) grpcgo.StreamClientInterceptor {
	return func(
		ctx context.Context,
		description *grpcgo.StreamDesc,
		connection *grpcgo.ClientConn,
		method string,
		streamer grpcgo.Streamer,
		options ...grpcgo.CallOption,
	) (grpcgo.ClientStream, error) {
		callCtx, cancel := contextWithMaximumTimeout(ctx, timeout)
		stream, err := streamer(callCtx, description, connection, method, options...)
		if err != nil {
			cancel()
			return nil, err
		}

		return &deadlineClientStream{
			ClientStream: stream,
			cancel:       cancel,
		}, nil
	}
}

type deadlineClientStream struct {
	grpcgo.ClientStream
	cancel context.CancelFunc
	once   sync.Once
}

func (stream *deadlineClientStream) SendMsg(message any) error {
	err := stream.ClientStream.SendMsg(message)
	if err != nil {
		stream.finish()
	}

	return err
}

func (stream *deadlineClientStream) RecvMsg(message any) error {
	err := stream.ClientStream.RecvMsg(message)
	if err != nil {
		stream.finish()
	}

	return err
}

func (stream *deadlineClientStream) finish() {
	stream.once.Do(stream.cancel)
}

func unaryClientLoggingInterceptor(log *logger.Logger) grpcgo.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		request any,
		response any,
		connection *grpcgo.ClientConn,
		invoker grpcgo.UnaryInvoker,
		options ...grpcgo.CallOption,
	) error {
		started := time.Now()
		err := invoker(ctx, method, request, response, connection, options...)
		log.InfoContext(
			ctx,
			"gRPC client unary вызов завершён",
			logger.String("rpc_method", method),
			logger.String("grpc_code", status.Code(err).String()),
			logger.Duration("duration", time.Since(started)),
		)

		return err
	}
}

func streamClientLoggingInterceptor(log *logger.Logger) grpcgo.StreamClientInterceptor {
	return func(
		ctx context.Context,
		description *grpcgo.StreamDesc,
		connection *grpcgo.ClientConn,
		method string,
		streamer grpcgo.Streamer,
		options ...grpcgo.CallOption,
	) (grpcgo.ClientStream, error) {
		started := time.Now()
		stream, err := streamer(ctx, description, connection, method, options...)
		if err != nil {
			logClientStreamResult(log, ctx, method, started, err)
			return nil, err
		}

		logged := &loggingClientStream{
			ClientStream: stream,
			log:          log,
			method:       method,
			started:      started,
		}
		logged.stopContextLog = context.AfterFunc(ctx, func() {
			logged.finish(status.FromContextError(ctx.Err()).Err())
		})

		return logged, nil
	}
}

type loggingClientStream struct {
	grpcgo.ClientStream
	log            *logger.Logger
	method         string
	started        time.Time
	once           sync.Once
	stopContextLog func() bool
}

func (stream *loggingClientStream) SendMsg(message any) error {
	err := stream.ClientStream.SendMsg(message)
	if err != nil {
		stream.finish(err)
	}

	return err
}

func (stream *loggingClientStream) RecvMsg(message any) error {
	err := stream.ClientStream.RecvMsg(message)
	if err != nil {
		if err == io.EOF {
			stream.finish(nil)
		} else {
			stream.finish(err)
		}
	}

	return err
}

func (stream *loggingClientStream) finish(err error) {
	stream.once.Do(func() {
		if stream.stopContextLog != nil {
			stream.stopContextLog()
		}
		logClientStreamResult(stream.log, stream.Context(), stream.method, stream.started, err)
	})
}

func logClientStreamResult(
	log *logger.Logger,
	ctx context.Context,
	method string,
	started time.Time,
	err error,
) {
	log.InfoContext(
		ctx,
		"gRPC client stream завершён",
		logger.String("rpc_method", method),
		logger.String("grpc_code", status.Code(err).String()),
		logger.Duration("duration", time.Since(started)),
	)
}

func contextWithMaximumTimeout(
	ctx context.Context,
	timeout time.Duration,
) (context.Context, context.CancelFunc) {
	deadline, hasDeadline := ctx.Deadline()
	if hasDeadline && time.Until(deadline) <= timeout {
		return context.WithCancel(ctx)
	}

	return context.WithTimeout(ctx, timeout)
}
