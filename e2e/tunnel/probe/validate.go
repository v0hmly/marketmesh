package probe

import (
	"errors"
	"fmt"
	"strings"
)

const (
	maxOpaqueIDLength  = 128
	maxDimensionLength = 64
)

var (
	ErrInvalidConfig     = errors.New("probe: invalid config")
	ErrDuplicateRequest  = errors.New("probe: duplicate request id")
	ErrJournalCapacity   = errors.New("probe: journal capacity exceeded")
	ErrEventCapacity     = errors.New("probe: event capacity exceeded")
	ErrRunnerUsed        = errors.New("probe: runner has already been used")
	ErrRunnerNotRunning  = errors.New("probe: runner is not running")
	ErrIncompleteRun     = errors.New("probe: run is incomplete")
	ErrStopTimeout       = errors.New("probe: stop timeout exceeded")
	ErrInvalidMarker     = errors.New("probe: invalid marker")
	ErrNonMonotonicClock = errors.New("probe: clock moved backwards")
	ErrSteadyNotReached  = errors.New("probe: steady state was not reached")
)

func validateRequestID(value string) bool {
	if len(value) != requestIDSize*2 {
		return false
	}
	for _, character := range value {
		isDigit := character >= '0' && character <= '9'
		isLowerHex := character >= 'a' && character <= 'f'
		if !isDigit && !isLowerHex {
			return false
		}
	}

	return true
}

func validateOpaqueID(value string) bool {
	if value == "" || len(value) > maxOpaqueIDLength {
		return false
	}

	for index, character := range value {
		isLowercase := character >= 'a' && character <= 'z'
		isUppercase := character >= 'A' && character <= 'Z'
		isDigit := character >= '0' && character <= '9'
		isSeparator := character == '-' ||
			character == '_' ||
			character == '.' ||
			character == ':'
		if !isLowercase && !isUppercase && !isDigit && !isSeparator {
			return false
		}
		if index == 0 && isSeparator {
			return false
		}
	}

	return true
}

func validateDimension(value string, allowEmpty bool) bool {
	return validateSafeValue(value, maxDimensionLength, allowEmpty)
}

func validateSafeValue(value string, maxLength int, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	if len(value) > maxLength {
		return false
	}

	for index, character := range value {
		isLowercase := character >= 'a' && character <= 'z'
		isUppercase := character >= 'A' && character <= 'Z'
		isDigit := character >= '0' && character <= '9'
		isSeparator := character == '-' || character == '_' || character == '.'
		if !isLowercase && !isUppercase && !isDigit && !isSeparator {
			return false
		}
		if index == 0 && isSeparator {
			return false
		}
	}

	return true
}

func validateClass(class TrafficClass) bool {
	return class == TrafficClassRead || class == TrafficClassMutating
}

func validateOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomeSuccess,
		OutcomeTimeout,
		OutcomeCanceled,
		OutcomeBackpressure,
		OutcomeUnavailable,
		OutcomeRejected,
		OutcomeInternalError,
		OutcomeInvalidMetadata:
		return true
	default:
		return false
	}
}

func validateMarker(marker Marker) error {
	if !validateOpaqueID(marker.FaultID) {
		return fmt.Errorf("%w: invalid fault id", ErrInvalidMarker)
	}
	if !validateDataCenter(marker.DataCenter, true) {
		return fmt.Errorf("%w: invalid data center", ErrInvalidMarker)
	}
	if !validateZone(marker.Zone, true) {
		return fmt.Errorf("%w: invalid zone", ErrInvalidMarker)
	}
	if !validateComponent(marker.Component, true) {
		return fmt.Errorf("%w: invalid component", ErrInvalidMarker)
	}
	if !validateRole(marker.Role, true) {
		return fmt.Errorf("%w: invalid role", ErrInvalidMarker)
	}
	if !validateDimension(marker.Revision, true) {
		return fmt.Errorf("%w: invalid revision", ErrInvalidMarker)
	}

	switch marker.Phase {
	case MarkerPhaseBefore,
		MarkerPhaseStarted,
		MarkerPhaseSteady,
		MarkerPhaseRecovering,
		MarkerPhaseRecovered,
		MarkerPhaseAfter:
	default:
		return fmt.Errorf("%w: invalid phase", ErrInvalidMarker)
	}

	switch marker.Result {
	case MarkerResultUnknown, MarkerResultSuccess, MarkerResultFailure:
	default:
		return fmt.Errorf("%w: invalid result", ErrInvalidMarker)
	}

	return nil
}

func validateDataCenter(dataCenter DataCenter, allowUnknown bool) bool {
	switch dataCenter {
	case DataCenterUnknown:
		return allowUnknown
	case DataCenterA, DataCenterB:
		return true
	default:
		return false
	}
}

func validateZone(zone Zone, allowUnknown bool) bool {
	switch zone {
	case ZoneUnknown:
		return allowUnknown
	case ZoneDMZ, ZoneInternal, ZoneExternal:
		return true
	default:
		return false
	}
}

func validateComponent(component Component, allowUnknown bool) bool {
	switch component {
	case ComponentUnknown:
		return allowUnknown
	case ComponentGatewayIn,
		ComponentGatewayOut,
		ComponentInternalService,
		ComponentKubernetesService,
		ComponentFrontDoor,
		ComponentNetwork,
		ComponentDataCenter:
		return true
	default:
		return false
	}
}

func validateRole(role Role, allowUnknown bool) bool {
	switch role {
	case RoleUnknown:
		return allowUnknown
	case RoleActive, RoleStandby, RoleReplica:
		return true
	default:
		return false
	}
}

func normalizedReasons(reasons []string) []string {
	unique := make(map[string]struct{}, len(reasons))
	result := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		reason = strings.TrimSpace(reason)
		if !validateDimension(reason, false) {
			reason = "invalid_incomplete_reason"
		}
		if _, exists := unique[reason]; exists {
			continue
		}
		unique[reason] = struct{}{}
		result = append(result, reason)
	}

	return result
}
