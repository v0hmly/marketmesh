package topology

import (
	"strings"
	"testing"
)

func TestDiagnosticFormatsExcludeSensitiveDockerFields(t *testing.T) {
	t.Parallel()

	combined := safeDockerInfoFormat + safeContainerInspectFormat
	for _, forbidden := range []string{"Config.Env", "HTTPProxy", "HTTPSProxy", "RegistryConfig", "Mounts"} {
		if strings.Contains(combined, forbidden) {
			t.Errorf("diagnostic format contains sensitive field %q", forbidden)
		}
	}
	for _, required := range []string{"ServerVersion", "Architecture", "Config.Image", "Config.Labels", "NetworkSettings.Networks"} {
		if !strings.Contains(combined, required) {
			t.Errorf("diagnostic format does not contain required field %q", required)
		}
	}
}
