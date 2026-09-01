package tunnelv1

import (
	"bytes"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// MaxEncodedFrameBytes is the absolute protobuf wire-size ceiling for v1.
	MaxEncodedFrameBytes = 1 << 20
	// MaxDataBytes is the absolute payload ceiling for one Data frame.
	MaxDataBytes uint32 = 256 << 10
	// MaxMessageBytes is the absolute reassembled logical-message ceiling.
	MaxMessageBytes uint32 = 8 << 20
	// MaxInFlightRequests is the absolute logical concurrency ceiling per tunnel.
	MaxInFlightRequests uint32 = 4096
	// MaxMetadataEntries is the absolute metadata item count per frame.
	MaxMetadataEntries uint32 = 16
	// MaxMetadataValueBytes is the largest permitted metadata value.
	MaxMetadataValueBytes uint32 = 16 << 10
	// MaxCreditBytes is the largest permitted increment in one Credit frame.
	MaxCreditBytes uint32 = 1 << 20
	// RequestIDBytes is the exact opaque request identifier size.
	RequestIDBytes = 16
	// TunnelIDBytes is the exact opaque tunnel identifier size.
	TunnelIDBytes = 16
	// InstanceIDBytes is the exact opaque process instance identifier size.
	InstanceIDBytes = 16
	// SessionIDBytes is the exact opaque session identifier size.
	SessionIDBytes = 16
	// MaxIdempotencyKeyBytes is the largest opaque idempotency key.
	MaxIdempotencyKeyBytes = 128
	// MaxSupportedVersions is the largest Hello version list.
	MaxSupportedVersions = 8
	// MaxCapabilities is the number of optional capabilities defined by v1.
	MaxCapabilities = 3
	// MaxTrafficClasses is the number of non-zero traffic classes defined by v1.
	MaxTrafficClasses = 3
	// MaxAdvertisedRoutes is the largest Hello route list.
	MaxAdvertisedRoutes = 64
	// MaxContentTypeBytes is the content-type metadata limit.
	MaxContentTypeBytes = 64
	// MaxTraceparentBytes is the traceparent metadata limit.
	MaxTraceparentBytes = 128
	// MaxTracestateBytes is the tracestate metadata limit.
	MaxTracestateBytes = 512
	// MaxRetryAfter is the largest backoff hint transferable in a Result.
	MaxRetryAfter = 5 * time.Minute
)

var protobufContentType = []byte("application/protobuf")

// ValidateGatewayOutFrame validates one already decoded frame from gateway-out.
// It performs structural checks only and does not advance tunnel state.
func ValidateGatewayOutFrame(frame *contractv1.ConnectRequest) error {
	if frame == nil {
		return invalidFrame("frame", "is required")
	}
	if err := rejectUnknownFields(frame.ProtoReflect(), "frame"); err != nil {
		return err
	}

	switch payload := frame.GetPayload().(type) {
	case *contractv1.ConnectRequest_Hello:
		if err := validateGatewayOutHelloHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validateGatewayOutHello(payload.Hello)
	case *contractv1.ConnectRequest_Data:
		if err := validateRequestHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validateData(payload.Data)
	case *contractv1.ConnectRequest_HalfClose:
		if err := validateRequestHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validateHalfClose(payload.HalfClose)
	case *contractv1.ConnectRequest_Cancel:
		if err := validateRequestHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validateCancel(payload.Cancel)
	case *contractv1.ConnectRequest_Result:
		if err := validateRequestHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validateResult(payload.Result)
	case *contractv1.ConnectRequest_Credit:
		if err := validateRequestHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validateCredit(payload.Credit)
	case *contractv1.ConnectRequest_Ping:
		if err := validateTunnelHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validatePing(payload.Ping)
	case *contractv1.ConnectRequest_Pong:
		if err := validateTunnelHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validatePong(payload.Pong)
	case *contractv1.ConnectRequest_Drain:
		if err := validateTunnelHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validateDrain(payload.Drain)
	case *contractv1.ConnectRequest_RevokeSession:
		if err := validateSessionControlHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validateRevokeSession(payload.RevokeSession)
	default:
		return ErrUnknownFrameType
	}
}

// ValidateGatewayInFrame validates one already decoded frame from gateway-in.
// It performs structural checks only and does not advance tunnel state.
func ValidateGatewayInFrame(frame *contractv1.ConnectResponse) error {
	if frame == nil {
		return invalidFrame("frame", "is required")
	}
	if err := rejectUnknownFields(frame.ProtoReflect(), "frame"); err != nil {
		return err
	}

	switch payload := frame.GetPayload().(type) {
	case *contractv1.ConnectResponse_Hello:
		if err := validateGatewayInHelloHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validateGatewayInHello(payload.Hello)
	case *contractv1.ConnectResponse_Open:
		if err := validateRequestHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validateOpen(frame.GetHeader().GetTrafficClass(), payload.Open)
	case *contractv1.ConnectResponse_Data:
		if err := validateRequestHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validateData(payload.Data)
	case *contractv1.ConnectResponse_HalfClose:
		if err := validateRequestHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validateHalfClose(payload.HalfClose)
	case *contractv1.ConnectResponse_Cancel:
		if err := validateRequestHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validateCancel(payload.Cancel)
	case *contractv1.ConnectResponse_Credit:
		if err := validateRequestHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validateCredit(payload.Credit)
	case *contractv1.ConnectResponse_Ping:
		if err := validateTunnelHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validatePing(payload.Ping)
	case *contractv1.ConnectResponse_Pong:
		if err := validateTunnelHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validatePong(payload.Pong)
	case *contractv1.ConnectResponse_Drain:
		if err := validateTunnelHeader(frame.GetHeader()); err != nil {
			return err
		}
		return validateDrain(payload.Drain)
	default:
		return ErrUnknownFrameType
	}
}

func validateGatewayOutHelloHeader(header *contractv1.FrameHeader) error {
	if err := validateHelloHeader(header); err != nil {
		return err
	}
	if len(header.GetTunnelId()) != 0 {
		return invalidFrame("header.tunnel_id", "must be empty in gateway-out Hello")
	}

	return nil
}

func validateGatewayInHelloHeader(header *contractv1.FrameHeader) error {
	if err := validateHelloHeader(header); err != nil {
		return err
	}
	if len(header.GetTunnelId()) != TunnelIDBytes {
		return invalidFrame("header.tunnel_id", "must be 16 bytes in gateway-in Hello")
	}

	return nil
}

func validateHelloHeader(header *contractv1.FrameHeader) error {
	if header == nil {
		return invalidFrame("header", "is required")
	}
	if header.GetProtocolVersion() != 0 {
		return unsupportedVersion("header.protocol_version")
	}
	if len(header.GetRequestId()) != 0 {
		return invalidFrame("header.request_id", "must be empty for Hello")
	}
	if header.GetSequence() != 0 {
		return invalidFrame("header.sequence", "must be zero for Hello")
	}
	if header.GetTrafficClass() != contractv1.TrafficClass_TRAFFIC_CLASS_UNSPECIFIED {
		return invalidFrame("header.traffic_class", "must be unspecified for Hello")
	}

	return nil
}

func validateRequestHeader(header *contractv1.FrameHeader) error {
	if header == nil {
		return invalidFrame("header", "is required")
	}
	if header.GetProtocolVersion() != protocolVersion {
		return unsupportedVersion("header.protocol_version")
	}
	if len(header.GetTunnelId()) != TunnelIDBytes {
		return invalidFrame("header.tunnel_id", "must be 16 bytes")
	}
	if len(header.GetRequestId()) != RequestIDBytes {
		return invalidFrame("header.request_id", "must be 16 bytes")
	}
	if header.GetSequence() == 0 {
		return invalidFrame("header.sequence", "must be positive")
	}
	if !knownTrafficClass(header.GetTrafficClass()) ||
		header.GetTrafficClass() == contractv1.TrafficClass_TRAFFIC_CLASS_UNSPECIFIED {
		return invalidFrame("header.traffic_class", "is unknown or unspecified")
	}

	return nil
}

func validateTunnelHeader(header *contractv1.FrameHeader) error {
	if header == nil {
		return invalidFrame("header", "is required")
	}
	if header.GetProtocolVersion() != protocolVersion {
		return unsupportedVersion("header.protocol_version")
	}
	if len(header.GetTunnelId()) != TunnelIDBytes {
		return invalidFrame("header.tunnel_id", "must be 16 bytes")
	}
	if len(header.GetRequestId()) != 0 {
		return invalidFrame("header.request_id", "must be empty for tunnel control")
	}
	if header.GetSequence() != 0 {
		return invalidFrame("header.sequence", "must be zero for tunnel control")
	}
	if header.GetTrafficClass() != contractv1.TrafficClass_TRAFFIC_CLASS_UNSPECIFIED {
		return invalidFrame("header.traffic_class", "must be unspecified for tunnel control")
	}

	return nil
}

func validateSessionControlHeader(header *contractv1.FrameHeader) error {
	if header == nil {
		return invalidFrame("header", "is required")
	}
	if header.GetProtocolVersion() != protocolVersion {
		return unsupportedVersion("header.protocol_version")
	}
	if len(header.GetTunnelId()) != TunnelIDBytes {
		return invalidFrame("header.tunnel_id", "must be 16 bytes")
	}
	if len(header.GetRequestId()) != 0 {
		return invalidFrame("header.request_id", "must be empty for session control")
	}
	if header.GetSequence() != 0 {
		return invalidFrame("header.sequence", "must be zero for session control")
	}
	if header.GetTrafficClass() != contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH {
		return invalidFrame("header.traffic_class", "must be control/auth")
	}

	return nil
}

func validateGatewayOutHello(hello *contractv1.GatewayOutHello) error {
	if hello == nil {
		return invalidFrame("hello", "is required")
	}
	if len(hello.GetInstanceId()) != InstanceIDBytes {
		return invalidFrame("hello.instance_id", "must be 16 bytes")
	}
	if err := validateSupportedVersions(hello.GetSupportedProtocolVersions()); err != nil {
		return err
	}
	if err := validateCapabilities(hello.GetCapabilities()); err != nil {
		return err
	}
	if err := validateTrafficClasses(hello.GetTrafficClasses()); err != nil {
		return err
	}
	if err := validateRoutes(hello.GetRouteIds()); err != nil {
		return err
	}

	return validateLimits(hello.GetLimits())
}

func validateGatewayInHello(hello *contractv1.GatewayInHello) error {
	if hello == nil {
		return invalidFrame("hello", "is required")
	}
	if len(hello.GetInstanceId()) != InstanceIDBytes {
		return invalidFrame("hello.instance_id", "must be 16 bytes")
	}
	if hello.GetSelectedProtocolVersion() != protocolVersion {
		return unsupportedVersion("hello.selected_protocol_version")
	}
	if err := validateCapabilities(hello.GetCapabilities()); err != nil {
		return err
	}
	if err := validateTrafficClasses(hello.GetTrafficClasses()); err != nil {
		return err
	}
	if err := validateRoutes(hello.GetRouteIds()); err != nil {
		return err
	}

	return validateLimits(hello.GetLimits())
}

func validateSupportedVersions(versions []uint32) error {
	if len(versions) == 0 || len(versions) > MaxSupportedVersions {
		return invalidFrame("hello.supported_protocol_versions", "has invalid length")
	}

	seen := map[uint32]struct{}{}
	foundVersion := false
	for _, version := range versions {
		if _, exists := seen[version]; exists {
			return invalidFrame("hello.supported_protocol_versions", "contains a duplicate")
		}
		seen[version] = struct{}{}
		if version == protocolVersion {
			foundVersion = true
		}
	}
	if !foundVersion {
		return unsupportedVersion("hello.supported_protocol_versions")
	}

	return nil
}

func validateCapabilities(capabilities []contractv1.Capability) error {
	if len(capabilities) > MaxCapabilities {
		return invalidFrame("hello.capabilities", "has invalid length")
	}

	seen := map[contractv1.Capability]struct{}{}
	for _, capability := range capabilities {
		if !knownCapability(capability) || capability == contractv1.Capability_CAPABILITY_UNSPECIFIED {
			return invalidFrame("hello.capabilities", "contains an unknown value")
		}
		if _, exists := seen[capability]; exists {
			return invalidFrame("hello.capabilities", "contains a duplicate")
		}
		seen[capability] = struct{}{}
	}

	return nil
}

func validateTrafficClasses(classes []contractv1.TrafficClass) error {
	if len(classes) == 0 || len(classes) > MaxTrafficClasses {
		return invalidFrame("hello.traffic_classes", "has invalid length")
	}

	seen := map[contractv1.TrafficClass]struct{}{}
	for _, class := range classes {
		if !knownTrafficClass(class) || class == contractv1.TrafficClass_TRAFFIC_CLASS_UNSPECIFIED {
			return invalidFrame("hello.traffic_classes", "contains an unknown value")
		}
		if _, exists := seen[class]; exists {
			return invalidFrame("hello.traffic_classes", "contains a duplicate")
		}
		seen[class] = struct{}{}
	}

	return nil
}

func validateRoutes(routes []contractv1.RouteId) error {
	if len(routes) == 0 || len(routes) > MaxAdvertisedRoutes {
		return invalidFrame("hello.route_ids", "has invalid length")
	}

	seen := map[contractv1.RouteId]struct{}{}
	for _, route := range routes {
		if !knownRoute(route) || route == contractv1.RouteId_ROUTE_ID_UNSPECIFIED {
			return invalidFrame("hello.route_ids", "contains an unknown value")
		}
		if _, exists := seen[route]; exists {
			return invalidFrame("hello.route_ids", "contains a duplicate")
		}
		seen[route] = struct{}{}
	}

	return nil
}

func validateLimits(limits *contractv1.Limits) error {
	if limits == nil {
		return invalidFrame("hello.limits", "is required")
	}
	if limits.GetMaxFrameBytes() == 0 || limits.GetMaxFrameBytes() > MaxEncodedFrameBytes {
		return invalidFrame("hello.limits.max_frame_bytes", "is outside v1 bounds")
	}
	if limits.GetMaxDataBytes() == 0 || limits.GetMaxDataBytes() > MaxDataBytes {
		return invalidFrame("hello.limits.max_data_bytes", "is outside v1 bounds")
	}
	if limits.GetMaxFrameBytes() <= limits.GetMaxDataBytes() {
		return invalidFrame("hello.limits.max_frame_bytes", "must exceed max_data_bytes")
	}
	if limits.GetMaxMessageBytes() < limits.GetMaxDataBytes() ||
		limits.GetMaxMessageBytes() > MaxMessageBytes {
		return invalidFrame("hello.limits.max_message_bytes", "is outside v1 bounds")
	}
	if limits.GetMaxInFlightRequests() == 0 ||
		limits.GetMaxInFlightRequests() > MaxInFlightRequests {
		return invalidFrame("hello.limits.max_in_flight_requests", "is outside v1 bounds")
	}
	if limits.GetMaxMetadataEntries() == 0 ||
		limits.GetMaxMetadataEntries() > MaxMetadataEntries {
		return invalidFrame("hello.limits.max_metadata_entries", "is outside v1 bounds")
	}
	if limits.GetMaxMetadataValueBytes() == 0 ||
		limits.GetMaxMetadataValueBytes() > MaxMetadataValueBytes {
		return invalidFrame("hello.limits.max_metadata_value_bytes", "is outside v1 bounds")
	}
	if limits.GetMaxCreditBytes() == 0 || limits.GetMaxCreditBytes() > MaxCreditBytes {
		return invalidFrame("hello.limits.max_credit_bytes", "is outside v1 bounds")
	}

	return nil
}

func validateOpen(class contractv1.TrafficClass, open *contractv1.Open) error {
	if open == nil {
		return invalidFrame("open", "is required")
	}
	if !knownRoute(open.GetRouteId()) || open.GetRouteId() == contractv1.RouteId_ROUTE_ID_UNSPECIFIED {
		return invalidFrame("open.route_id", "is unknown or unspecified")
	}
	if class != routeTrafficClass(open.GetRouteId()) {
		return invalidFrame("header.traffic_class", "does not match route policy")
	}
	if err := validateTimestamp("open.deadline", open.GetDeadline()); err != nil {
		return err
	}
	if len(open.GetIdempotencyKey()) > MaxIdempotencyKeyBytes {
		return invalidFrame("open.idempotency_key", "exceeds v1 limit")
	}

	return validateMetadata("open.metadata", open.GetMetadata(), true)
}

func validateData(data *contractv1.Data) error {
	if data == nil {
		return invalidFrame("data", "is required")
	}
	if len(data.GetPayload()) == 0 || len(data.GetPayload()) > int(MaxDataBytes) {
		return invalidFrame("data.payload", "is empty or exceeds v1 limit")
	}

	return nil
}

func validateHalfClose(halfClose *contractv1.HalfClose) error {
	if halfClose == nil {
		return invalidFrame("half_close", "is required")
	}

	return nil
}

func validateCancel(cancel *contractv1.Cancel) error {
	if cancel == nil {
		return invalidFrame("cancel", "is required")
	}
	if !knownCancelReason(cancel.GetReason()) ||
		cancel.GetReason() == contractv1.CancelReason_CANCEL_REASON_UNSPECIFIED {
		return invalidFrame("cancel.reason", "is unknown or unspecified")
	}

	return nil
}

func validateResult(result *contractv1.Result) error {
	if result == nil {
		return invalidFrame("result", "is required")
	}
	if !knownResultCode(result.GetCode()) || result.GetCode() == contractv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return invalidFrame("result.code", "is unknown or unspecified")
	}
	if result.GetCode() == contractv1.ResultCode_RESULT_CODE_OK && result.GetRetryAfter() != nil {
		return invalidFrame("result.retry_after", "must be absent for success")
	}
	if result.GetRetryAfter() != nil {
		if err := validateDuration("result.retry_after", result.GetRetryAfter()); err != nil {
			return err
		}
	}

	return validateMetadata("result.metadata", result.GetMetadata(), false)
}

func validateCredit(credit *contractv1.Credit) error {
	if credit == nil {
		return invalidFrame("credit", "is required")
	}
	if credit.GetBytes() == 0 || credit.GetBytes() > MaxCreditBytes {
		return invalidFrame("credit.bytes", "is outside v1 bounds")
	}

	return nil
}

func validatePing(ping *contractv1.Ping) error {
	if ping == nil || ping.GetNonce() == 0 {
		return invalidFrame("ping.nonce", "must be positive")
	}

	return nil
}

func validatePong(pong *contractv1.Pong) error {
	if pong == nil || pong.GetNonce() == 0 {
		return invalidFrame("pong.nonce", "must be positive")
	}

	return nil
}

func validateDrain(drain *contractv1.Drain) error {
	if drain == nil {
		return invalidFrame("drain", "is required")
	}
	if err := validateTimestamp("drain.deadline", drain.GetDeadline()); err != nil {
		return err
	}
	if !knownDrainReason(drain.GetReason()) ||
		drain.GetReason() == contractv1.DrainReason_DRAIN_REASON_UNSPECIFIED {
		return invalidFrame("drain.reason", "is unknown or unspecified")
	}

	return nil
}

func validateRevokeSession(revoke *contractv1.RevokeSession) error {
	if revoke == nil || len(revoke.GetSessionId()) != SessionIDBytes {
		return invalidFrame("revoke_session.session_id", "must be 16 bytes")
	}

	return nil
}

func validateMetadata(path string, entries []*contractv1.Metadata, allowSessionAssertion bool) error {
	if len(entries) > int(MaxMetadataEntries) {
		return invalidFrame(path, "has too many entries")
	}

	seen := map[contractv1.MetadataKey]struct{}{}
	for _, entry := range entries {
		if entry == nil {
			return invalidFrame(path, "contains a nil entry")
		}
		key := entry.GetKey()
		if !knownMetadataKey(key) || key == contractv1.MetadataKey_METADATA_KEY_UNSPECIFIED {
			return invalidFrame(path, "contains an unknown key")
		}
		if _, exists := seen[key]; exists {
			return invalidFrame(path, "contains a duplicate key")
		}
		seen[key] = struct{}{}
		if err := validateMetadataValue(key, entry.GetValue(), allowSessionAssertion); err != nil {
			return err
		}
	}

	return nil
}

func validateMetadataValue(
	key contractv1.MetadataKey,
	value []byte,
	allowSessionAssertion bool,
) error {
	if len(value) == 0 || len(value) > int(MaxMetadataValueBytes) {
		return invalidFrame("metadata.value", "is empty or exceeds v1 limit")
	}

	switch key {
	case contractv1.MetadataKey_METADATA_KEY_CONTENT_TYPE:
		if len(value) > MaxContentTypeBytes || !bytes.Equal(value, protobufContentType) {
			return invalidFrame("metadata.value", "has unsupported content type")
		}
	case contractv1.MetadataKey_METADATA_KEY_TRACEPARENT:
		if len(value) > MaxTraceparentBytes || !validTraceparent(value) {
			return invalidFrame("metadata.value", "has invalid traceparent format")
		}
	case contractv1.MetadataKey_METADATA_KEY_TRACESTATE:
		if len(value) > MaxTracestateBytes || !validTracestate(value) {
			return invalidFrame("metadata.value", "has invalid tracestate format")
		}
	case contractv1.MetadataKey_METADATA_KEY_SESSION_ASSERTION:
		if !allowSessionAssertion {
			return invalidFrame("metadata.key", "is forbidden in Result")
		}
	default:
		return invalidFrame("metadata.key", "is unknown")
	}

	return nil
}

func validTraceparent(value []byte) bool {
	if len(value) != 55 || !bytes.Equal(value[:2], []byte("00")) {
		return false
	}
	if value[2] != '-' || value[35] != '-' || value[52] != '-' {
		return false
	}
	if !lowerHex(value[3:35]) || allZero(value[3:35]) {
		return false
	}
	if !lowerHex(value[36:52]) || allZero(value[36:52]) {
		return false
	}

	return lowerHex(value[53:55])
}

func validTracestate(value []byte) bool {
	if value[0] == ',' || value[len(value)-1] == ',' {
		return false
	}

	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}

	members := bytes.Split(value, []byte{','})
	if len(members) > 32 {
		return false
	}
	seen := map[string]struct{}{}
	for _, member := range members {
		member = bytes.Trim(member, " ")
		key, memberValue, found := bytes.Cut(member, []byte{'='})
		if !found || !validTracestateKey(key) || !validTracestateValue(memberValue) {
			return false
		}
		if _, exists := seen[string(key)]; exists {
			return false
		}
		seen[string(key)] = struct{}{}
	}

	return true
}

func validTracestateKey(value []byte) bool {
	if len(value) == 0 || len(value) > 256 || !lowerAlphaNumeric(value[0]) {
		return false
	}

	atCount := 0
	for _, character := range value {
		isAllowed := lowerAlphaNumeric(character) || character == '_' || character == '-' ||
			character == '*' || character == '/'
		if character == '@' {
			atCount++
			isAllowed = true
		}
		if !isAllowed || atCount > 1 {
			return false
		}
	}

	return value[0] != '@' && value[len(value)-1] != '@'
}

func validTracestateValue(value []byte) bool {
	if len(value) == 0 || len(value) > 256 || value[len(value)-1] == ' ' {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e || character == ',' || character == '=' {
			return false
		}
	}

	return true
}

func lowerAlphaNumeric(value byte) bool {
	isLowerLetter := value >= 'a' && value <= 'z'
	isNumber := value >= '0' && value <= '9'
	return isLowerLetter || isNumber
}

func lowerHex(value []byte) bool {
	for _, character := range value {
		isNumber := character >= '0' && character <= '9'
		isLowerLetter := character >= 'a' && character <= 'f'
		if !isNumber && !isLowerLetter {
			return false
		}
	}

	return true
}

func allZero(value []byte) bool {
	for _, character := range value {
		if character != '0' {
			return false
		}
	}

	return true
}

func validateTimestamp(field string, timestamp *timestamppb.Timestamp) error {
	if timestamp == nil || timestamp.CheckValid() != nil {
		return invalidFrame(field, "is required and must be a valid UTC timestamp")
	}

	return nil
}

func validateDuration(field string, duration *durationpb.Duration) error {
	if duration.CheckValid() != nil {
		return invalidFrame(field, "must be valid")
	}
	value := duration.AsDuration()
	if value <= 0 || value > MaxRetryAfter {
		return invalidFrame(field, "is outside v1 bounds")
	}

	return nil
}

func routeTrafficClass(route contractv1.RouteId) contractv1.TrafficClass {
	switch route {
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

func knownTrafficClass(value contractv1.TrafficClass) bool {
	switch value {
	case contractv1.TrafficClass_TRAFFIC_CLASS_UNSPECIFIED,
		contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
		contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
		contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME:
		return true
	default:
		return false
	}
}

func knownCapability(value contractv1.Capability) bool {
	switch value {
	case contractv1.Capability_CAPABILITY_UNSPECIFIED,
		contractv1.Capability_CAPABILITY_DRAIN,
		contractv1.Capability_CAPABILITY_SESSION_REVOCATION,
		contractv1.Capability_CAPABILITY_REALTIME:
		return true
	default:
		return false
	}
}

func knownRoute(value contractv1.RouteId) bool {
	switch value {
	case contractv1.RouteId_ROUTE_ID_UNSPECIFIED,
		contractv1.RouteId_ROUTE_ID_AUTH_REGISTER_CREDENTIALS,
		contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
		contractv1.RouteId_ROUTE_ID_AUTH_REFRESH_SESSION,
		contractv1.RouteId_ROUTE_ID_AUTH_REVOKE_SESSION,
		contractv1.RouteId_ROUTE_ID_AUTH_SESSION_ASSERTION,
		contractv1.RouteId_ROUTE_ID_USER_GET_ME,
		contractv1.RouteId_ROUTE_ID_USER_UPDATE_ME,
		contractv1.RouteId_ROUTE_ID_REALTIME_CHAT,
		contractv1.RouteId_ROUTE_ID_REALTIME_NOTIFICATIONS:
		return true
	default:
		return false
	}
}

func knownMetadataKey(value contractv1.MetadataKey) bool {
	switch value {
	case contractv1.MetadataKey_METADATA_KEY_UNSPECIFIED,
		contractv1.MetadataKey_METADATA_KEY_CONTENT_TYPE,
		contractv1.MetadataKey_METADATA_KEY_TRACEPARENT,
		contractv1.MetadataKey_METADATA_KEY_TRACESTATE,
		contractv1.MetadataKey_METADATA_KEY_SESSION_ASSERTION:
		return true
	default:
		return false
	}
}

func knownCancelReason(value contractv1.CancelReason) bool {
	switch value {
	case contractv1.CancelReason_CANCEL_REASON_UNSPECIFIED,
		contractv1.CancelReason_CANCEL_REASON_CALLER,
		contractv1.CancelReason_CANCEL_REASON_DEADLINE,
		contractv1.CancelReason_CANCEL_REASON_DRAIN,
		contractv1.CancelReason_CANCEL_REASON_POLICY:
		return true
	default:
		return false
	}
}

func knownResultCode(value contractv1.ResultCode) bool {
	switch value {
	case contractv1.ResultCode_RESULT_CODE_UNSPECIFIED,
		contractv1.ResultCode_RESULT_CODE_OK,
		contractv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
		contractv1.ResultCode_RESULT_CODE_UNAUTHENTICATED,
		contractv1.ResultCode_RESULT_CODE_PERMISSION_DENIED,
		contractv1.ResultCode_RESULT_CODE_NOT_FOUND,
		contractv1.ResultCode_RESULT_CODE_CONFLICT,
		contractv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED,
		contractv1.ResultCode_RESULT_CODE_DEADLINE_EXCEEDED,
		contractv1.ResultCode_RESULT_CODE_CANCELED,
		contractv1.ResultCode_RESULT_CODE_UNAVAILABLE,
		contractv1.ResultCode_RESULT_CODE_INTERNAL:
		return true
	default:
		return false
	}
}

func knownDrainReason(value contractv1.DrainReason) bool {
	switch value {
	case contractv1.DrainReason_DRAIN_REASON_UNSPECIFIED,
		contractv1.DrainReason_DRAIN_REASON_MAINTENANCE,
		contractv1.DrainReason_DRAIN_REASON_SHUTDOWN,
		contractv1.DrainReason_DRAIN_REASON_OVERLOAD:
		return true
	default:
		return false
	}
}
