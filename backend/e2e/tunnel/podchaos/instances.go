package podchaos

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const (
	instanceIDBytes = 16
	gatewayOutSlots = 2
)

func gatewayInInstanceID(podName string) (string, error) {
	if !isDNSSubdomain(podName) {
		return "", fmt.Errorf("%w: gateway-in pod name is invalid", ErrUnsafeState)
	}
	digest := sha256.Sum256([]byte(podName))
	return hex.EncodeToString(digest[:instanceIDBytes]), nil
}

func gatewayOutInstanceIDs(podName string) ([gatewayOutSlots]string, error) {
	if !isDNSSubdomain(podName) {
		return [gatewayOutSlots]string{}, fmt.Errorf(
			"%w: gateway-out pod name is invalid",
			ErrUnsafeState,
		)
	}

	var result [gatewayOutSlots]string
	for slot := range gatewayOutSlots {
		source := make([]byte, len(podName)+1)
		copy(source, podName)
		source[len(podName)] = byte(slot)
		digest := sha256.Sum256(source)
		result[slot] = hex.EncodeToString(digest[:instanceIDBytes])
	}
	return result, nil
}
