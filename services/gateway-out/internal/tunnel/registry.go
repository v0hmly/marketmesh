package tunnel

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// MessageFactory создаёт новый protobuf DTO фиксированного внутреннего RPC.
type MessageFactory func() proto.Message

// ClassClients содержит отдельный gRPC client для каждого класса трафика.
// Клиенты создаются composition root с отключёнными automatic retries.
type ClassClients struct {
	ControlAuth grpcgo.ClientConnInterface
	Regular     grpcgo.ClientConnInterface
	Realtime    grpcgo.ClientConnInterface
}

// RouteSpec — локальная allowlist-запись. Ни одно из этих полей не берётся из tunnel wire.
type RouteSpec struct {
	ID                    contractv1.RouteId
	TrafficClass          contractv1.TrafficClass
	Method                string
	NewRequest            MessageFactory
	NewResponse           MessageFactory
	MaxRequestBytes       uint32
	MaxResponseBytes      uint32
	MaxDeadline           time.Duration
	Mutating              bool
	RequireIdempotencyKey bool
}

type route struct {
	RouteSpec
	client grpcgo.ClientConnInterface
}

// Registry — неизменяемое отображение RouteId на заранее заданный client/method.
type Registry struct {
	routes  map[contractv1.RouteId]route
	ordered []contractv1.RouteId
	classes []contractv1.TrafficClass
}

// NewRegistry проверяет и копирует статический route registry.
func NewRegistry(clients ClassClients, specs ...RouteSpec) (*Registry, error) {
	if len(specs) == 0 || len(specs) > protocolv1.MaxAdvertisedRoutes {
		return nil, errors.New("gateway-out tunnel registry must contain a bounded route set")
	}

	registry := &Registry{
		routes:  make(map[contractv1.RouteId]route, len(specs)),
		ordered: make([]contractv1.RouteId, 0, len(specs)),
		classes: make([]contractv1.TrafficClass, 0, protocolv1.MaxTrafficClasses),
	}
	seenClasses := make(map[contractv1.TrafficClass]struct{}, protocolv1.MaxTrafficClasses)
	for _, spec := range specs {
		if err := validateRouteSpec(spec); err != nil {
			return nil, err
		}
		if _, duplicated := registry.routes[spec.ID]; duplicated {
			return nil, fmt.Errorf("gateway-out tunnel route %s is duplicated", spec.ID)
		}

		client := clientForClass(clients, spec.TrafficClass)
		if nilInterface(client) {
			return nil, fmt.Errorf("gateway-out tunnel client for class %s is required", spec.TrafficClass)
		}

		registry.routes[spec.ID] = route{RouteSpec: spec, client: client}
		registry.ordered = append(registry.ordered, spec.ID)
		if _, found := seenClasses[spec.TrafficClass]; !found {
			seenClasses[spec.TrafficClass] = struct{}{}
			registry.classes = append(registry.classes, spec.TrafficClass)
		}
	}

	slices.Sort(registry.ordered)
	slices.Sort(registry.classes)

	return registry, nil
}

func validateRouteSpec(spec RouteSpec) error {
	if expected := routeTrafficClass(spec.ID); expected == contractv1.TrafficClass_TRAFFIC_CLASS_UNSPECIFIED {
		return fmt.Errorf("gateway-out tunnel route %s is unknown", spec.ID)
	} else if spec.TrafficClass != expected {
		return fmt.Errorf("gateway-out tunnel route %s has an invalid traffic class", spec.ID)
	}
	if spec.TrafficClass == contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME {
		return fmt.Errorf("gateway-out tunnel route %s requires a dedicated streaming adapter", spec.ID)
	}
	if !validFullMethod(spec.Method) {
		return fmt.Errorf("gateway-out tunnel route %s has an invalid internal method", spec.ID)
	}
	if spec.NewRequest == nil || spec.NewResponse == nil {
		return fmt.Errorf("gateway-out tunnel route %s requires protobuf factories", spec.ID)
	}
	if nilInterface(spec.NewRequest()) || nilInterface(spec.NewResponse()) {
		return fmt.Errorf("gateway-out tunnel route %s factory returned nil", spec.ID)
	}
	if spec.MaxRequestBytes == 0 || spec.MaxRequestBytes > protocolv1.MaxMessageBytes {
		return fmt.Errorf("gateway-out tunnel route %s request limit is invalid", spec.ID)
	}
	if spec.MaxResponseBytes == 0 || spec.MaxResponseBytes > protocolv1.MaxMessageBytes {
		return fmt.Errorf("gateway-out tunnel route %s response limit is invalid", spec.ID)
	}
	if spec.MaxDeadline <= 0 {
		return fmt.Errorf("gateway-out tunnel route %s deadline must be positive", spec.ID)
	}
	if spec.RequireIdempotencyKey && !spec.Mutating {
		return fmt.Errorf("gateway-out tunnel route %s cannot require idempotency for a read", spec.ID)
	}

	return nil
}

func (registry *Registry) lookup(id contractv1.RouteId) (route, bool) {
	value, found := registry.routes[id]
	return value, found
}

func (registry *Registry) advertisedRoutes() []contractv1.RouteId {
	return slices.Clone(registry.ordered)
}

func (registry *Registry) advertisedClasses() []contractv1.TrafficClass {
	return slices.Clone(registry.classes)
}

func (registry *Registry) hasClass(class contractv1.TrafficClass) bool {
	return slices.Contains(registry.classes, class)
}

func (route route) invoke(ctx context.Context, requestBytes []byte) ([]byte, codes.Code) {
	if uint64(len(requestBytes)) > uint64(route.MaxRequestBytes) {
		return nil, codes.ResourceExhausted
	}

	request := route.NewRequest()
	if nilInterface(request) {
		return nil, codes.Internal
	}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(requestBytes, request); err != nil {
		return nil, codes.InvalidArgument
	}

	response := route.NewResponse()
	if nilInterface(response) {
		return nil, codes.Internal
	}
	err := route.client.Invoke(
		ctx,
		route.Method,
		request,
		response,
		grpcgo.WaitForReady(false),
		grpcgo.MaxCallRecvMsgSize(int(route.MaxResponseBytes)),
		grpcgo.MaxCallSendMsgSize(int(route.MaxRequestBytes)),
	)
	if err != nil {
		return nil, safeResultCode(err)
	}

	responseBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(response)
	if err != nil {
		return nil, codes.Internal
	}
	if uint64(len(responseBytes)) > uint64(route.MaxResponseBytes) {
		return nil, codes.ResourceExhausted
	}

	return responseBytes, codes.OK
}

func safeResultCode(err error) codes.Code {
	switch {
	case errors.Is(err, context.Canceled):
		return codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return codes.DeadlineExceeded
	}

	code := status.Code(err)
	switch code {
	case codes.Canceled,
		codes.InvalidArgument,
		codes.DeadlineExceeded,
		codes.NotFound,
		codes.AlreadyExists,
		codes.PermissionDenied,
		codes.ResourceExhausted,
		codes.Aborted,
		codes.Unavailable,
		codes.Unauthenticated:
		return code
	default:
		return codes.Internal
	}
}

func resultCode(code codes.Code) contractv1.ResultCode {
	switch code {
	case codes.OK:
		return contractv1.ResultCode_RESULT_CODE_OK
	case codes.InvalidArgument:
		return contractv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT
	case codes.Unauthenticated:
		return contractv1.ResultCode_RESULT_CODE_UNAUTHENTICATED
	case codes.PermissionDenied:
		return contractv1.ResultCode_RESULT_CODE_PERMISSION_DENIED
	case codes.NotFound:
		return contractv1.ResultCode_RESULT_CODE_NOT_FOUND
	case codes.AlreadyExists, codes.Aborted:
		return contractv1.ResultCode_RESULT_CODE_CONFLICT
	case codes.ResourceExhausted:
		return contractv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED
	case codes.DeadlineExceeded:
		return contractv1.ResultCode_RESULT_CODE_DEADLINE_EXCEEDED
	case codes.Canceled:
		return contractv1.ResultCode_RESULT_CODE_CANCELED
	case codes.Unavailable:
		return contractv1.ResultCode_RESULT_CODE_UNAVAILABLE
	default:
		return contractv1.ResultCode_RESULT_CODE_INTERNAL
	}
}

func clientForClass(
	clients ClassClients,
	class contractv1.TrafficClass,
) grpcgo.ClientConnInterface {
	switch class {
	case contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH:
		return clients.ControlAuth
	case contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR:
		return clients.Regular
	case contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME:
		return clients.Realtime
	default:
		return nil
	}
}

func routeTrafficClass(routeID contractv1.RouteId) contractv1.TrafficClass {
	switch routeID {
	case contractv1.RouteId_ROUTE_ID_AUTH_REGISTER_CREDENTIALS,
		contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
		contractv1.RouteId_ROUTE_ID_AUTH_REFRESH_SESSION,
		contractv1.RouteId_ROUTE_ID_AUTH_REVOKE_SESSION,
		contractv1.RouteId_ROUTE_ID_AUTH_SESSION_ASSERTION:
		return contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH
	case contractv1.RouteId_ROUTE_ID_USER_GET_ME,
		contractv1.RouteId_ROUTE_ID_USER_UPDATE_ME:
		return contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR
	case contractv1.RouteId_ROUTE_ID_REALTIME_CHAT,
		contractv1.RouteId_ROUTE_ID_REALTIME_NOTIFICATIONS:
		return contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME
	default:
		return contractv1.TrafficClass_TRAFFIC_CLASS_UNSPECIFIED
	}
}

func validFullMethod(method string) bool {
	method = strings.TrimSpace(method)
	if !strings.HasPrefix(method, "/") || strings.Count(method, "/") != 2 {
		return false
	}

	parts := strings.Split(method[1:], "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}

	kind := reflect.TypeOf(value).Kind()
	switch kind {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	default:
		return false
	}
}
