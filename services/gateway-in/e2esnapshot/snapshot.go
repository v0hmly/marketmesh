package e2esnapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// SchemaVersion identifies the only routing snapshot format accepted by
	// the MM-32 fault runner.
	SchemaVersion = "marketmesh.gateway-in.e2e.routing-snapshot/v1"
	// Path is the fixed endpoint registered only in explicitly enabled E2E pods.
	Path = "/_e2e/tunnel-routing-snapshot"

	// RouteUserGetMe is the fixed read route observed by the E2E probe.
	RouteUserGetMe Route = "ROUTE_ID_USER_GET_ME"
	// RouteUserUpdateMe is the fixed mutating route observed by the E2E probe.
	RouteUserUpdateMe Route = "ROUTE_ID_USER_UPDATE_ME"

	// TunnelStateHandshaking identifies a session that is not yet eligible.
	TunnelStateHandshaking TunnelState = "handshaking"
	// TunnelStateReady identifies an eligible session.
	TunnelStateReady TunnelState = "ready"
	// TunnelStateDraining identifies a session rejecting new requests.
	TunnelStateDraining TunnelState = "draining"
	// TunnelStateStale identifies a session whose activity deadline elapsed.
	TunnelStateStale TunnelState = "stale"
	// TunnelStateClosed identifies a terminated session retained in a snapshot.
	TunnelStateClosed TunnelState = "closed"

	maxDocumentBytes  = 64 * 1024
	maxJSONDepth      = 8
	maxJSONArray      = 128
	maxTunnels        = 32
	maxActiveRequests = 65_536
)

var (
	// ErrInvalidSnapshot identifies an unsafe or incomplete typed snapshot.
	ErrInvalidSnapshot = errors.New("e2esnapshot: invalid snapshot")
	// ErrInvalidDocument identifies malformed, ambiguous, or oversized JSON.
	ErrInvalidDocument = errors.New("e2esnapshot: invalid document")
)

// Route is a finite protobuf route name, not a caller-provided route ID.
type Route string

// TunnelState is a finite routing lifecycle state.
type TunnelState string

// Snapshot describes all configured E2E routes in one exact gateway-in pod.
type Snapshot struct {
	SchemaVersion     string          `json:"schema_version"`
	GatewayInInstance string          `json:"gateway_in_instance"`
	Routes            []RouteSnapshot `json:"routes"`
}

// RouteSnapshot describes one deterministic in-process registry snapshot.
type RouteSnapshot struct {
	Route            Route            `json:"route"`
	RouteAllowed     bool             `json:"route_allowed"`
	RegistryDraining bool             `json:"registry_draining"`
	Tunnels          []TunnelSnapshot `json:"tunnels"`
}

// TunnelSnapshot omits TunnelID deliberately. InstanceID is a 16-byte opaque
// value encoded as exactly 32 lower-hex characters.
type TunnelSnapshot struct {
	InstanceID     string      `json:"instance_id"`
	DataCenter     string      `json:"data_center"`
	State          TunnelState `json:"state"`
	ActiveRequests int         `json:"active_requests"`
}

// Decode reads one strict, bounded JSON document and validates all values.
func Decode(reader io.Reader) (Snapshot, error) {
	if reader == nil {
		return Snapshot{}, fmt.Errorf("%w: reader is required", ErrInvalidDocument)
	}

	data, err := io.ReadAll(io.LimitReader(reader, maxDocumentBytes+1))
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: read response", ErrInvalidDocument)
	}
	if len(data) == 0 || len(data) > maxDocumentBytes {
		return Snapshot{}, fmt.Errorf("%w: response size is outside bounds", ErrInvalidDocument)
	}
	if err := rejectAmbiguousJSON(data); err != nil {
		return Snapshot{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var snapshot Snapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("%w: decode response", ErrInvalidDocument)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Snapshot{}, err
	}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

// Encode validates and writes one JSON document. It never emits opaque values
// in an error message.
func Encode(writer io.Writer, snapshot Snapshot) error {
	if writer == nil {
		return fmt.Errorf("%w: writer is required", ErrInvalidDocument)
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	if err := json.NewEncoder(writer).Encode(snapshot); err != nil {
		return fmt.Errorf("%w: encode response", ErrInvalidDocument)
	}
	return nil
}

// Validate checks bounded fields and deterministic route/tunnel ordering.
func (snapshot Snapshot) Validate() error {
	if snapshot.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported schema version", ErrInvalidSnapshot)
	}
	if !isDNSSubdomain(snapshot.GatewayInInstance) {
		return fmt.Errorf("%w: gateway-in instance is invalid", ErrInvalidSnapshot)
	}
	wantedRoutes := [...]Route{RouteUserGetMe, RouteUserUpdateMe}
	if len(snapshot.Routes) != len(wantedRoutes) {
		return fmt.Errorf("%w: route count is invalid", ErrInvalidSnapshot)
	}
	for index, route := range snapshot.Routes {
		if route.Route != wantedRoutes[index] {
			return fmt.Errorf("%w: route order is invalid", ErrInvalidSnapshot)
		}
		if !route.RouteAllowed || route.RegistryDraining {
			return fmt.Errorf("%w: route is not available", ErrInvalidSnapshot)
		}
		if len(route.Tunnels) == 0 || len(route.Tunnels) > maxTunnels {
			return fmt.Errorf("%w: tunnel count is outside bounds", ErrInvalidSnapshot)
		}
		for tunnelIndex, tunnel := range route.Tunnels {
			if err := validateTunnel(tunnel); err != nil {
				return fmt.Errorf("%w: route %d tunnel %d", err, index, tunnelIndex)
			}
			if tunnelIndex > 0 && compareTunnels(route.Tunnels[tunnelIndex-1], tunnel) >= 0 {
				return fmt.Errorf("%w: tunnel order is invalid", ErrInvalidSnapshot)
			}
		}
	}
	return nil
}

func validateTunnel(tunnel TunnelSnapshot) error {
	if !isLowerHex(tunnel.InstanceID, 32) {
		return fmt.Errorf("%w: instance id is invalid", ErrInvalidSnapshot)
	}
	if tunnel.DataCenter != "dc-a" && tunnel.DataCenter != "dc-b" {
		return fmt.Errorf("%w: data center is invalid", ErrInvalidSnapshot)
	}
	switch tunnel.State {
	case TunnelStateHandshaking,
		TunnelStateReady,
		TunnelStateDraining,
		TunnelStateStale,
		TunnelStateClosed:
	default:
		return fmt.Errorf("%w: tunnel state is invalid", ErrInvalidSnapshot)
	}
	if tunnel.ActiveRequests < 0 || tunnel.ActiveRequests > maxActiveRequests {
		return fmt.Errorf("%w: active request count is outside bounds", ErrInvalidSnapshot)
	}
	return nil
}

func compareTunnels(left, right TunnelSnapshot) int {
	if compared := strings.Compare(left.DataCenter, right.DataCenter); compared != 0 {
		return compared
	}
	return strings.Compare(left.InstanceID, right.InstanceID)
}

func isLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range []byte(value) {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func isDNSSubdomain(value string) bool {
	if len(value) == 0 || len(value) > 253 {
		return false
	}
	for label := range strings.SplitSeq(value, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		for index, character := range []byte(label) {
			isAlphaNumeric := character >= 'a' && character <= 'z' ||
				character >= '0' && character <= '9'
			if !isAlphaNumeric && character != '-' {
				return false
			}
			if character == '-' && (index == 0 || index == len(label)-1) {
				return false
			}
		}
	}
	return true
}

func rejectAmbiguousJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := inspectJSONValue(decoder, 0); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	return ensureJSONEOF(decoder)
}

func inspectJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return errors.New("JSON nesting exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return errors.New("malformed JSON")
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}

	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			key, validKey := keyToken.(string)
			if keyErr != nil || !validKey {
				return errors.New("malformed object key")
			}
			if _, exists := keys[key]; exists {
				return errors.New("duplicate object field")
			}
			keys[key] = struct{}{}
			if err := inspectJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		return expectDelimiter(decoder, '}')
	case '[':
		count := 0
		for decoder.More() {
			count++
			if count > maxJSONArray {
				return errors.New("JSON array exceeds limit")
			}
			if err := inspectJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		return expectDelimiter(decoder, ']')
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

func expectDelimiter(decoder *json.Decoder, wanted json.Delim) error {
	token, err := decoder.Token()
	delimiter, valid := token.(json.Delim)
	if err != nil || !valid || delimiter != wanted {
		return errors.New("malformed JSON delimiter")
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing JSON content", ErrInvalidDocument)
	}
	return nil
}
