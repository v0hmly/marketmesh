package topology

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNewConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		root          string
		instance      string
		dockerContext string
		wantError     bool
	}{
		{
			name:          "valid",
			root:          "/workspace/marketmesh",
			instance:      "mm28",
			dockerContext: "orbstack",
		},
		{
			name:          "relative root",
			root:          "marketmesh",
			instance:      "mm28",
			dockerContext: "orbstack",
			wantError:     true,
		},
		{
			name:          "shell metacharacters in instance",
			root:          "/workspace/marketmesh",
			instance:      "mm28;docker-rm",
			dockerContext: "orbstack",
			wantError:     true,
		},
		{
			name:          "path separator in instance",
			root:          "/workspace/marketmesh",
			instance:      "../mm28",
			dockerContext: "orbstack",
			wantError:     true,
		},
		{
			name:          "shell metacharacters in docker context",
			root:          "/workspace/marketmesh",
			instance:      "mm28",
			dockerContext: "orbstack;default",
			wantError:     true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config, err := NewConfig(test.root, test.instance, test.dockerContext)
			if test.wantError {
				if err == nil {
					t.Fatal("NewConfig() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewConfig() error = %v", err)
			}
			wantState := filepath.Join(test.root, ".cache", "mm28-topology", test.instance)
			if config.StateDir != wantState {
				t.Errorf("StateDir = %q, want %q", config.StateDir, wantState)
			}
		})
	}
}

func TestConfigClusters(t *testing.T) {
	t.Parallel()

	config, err := NewConfig("/workspace/marketmesh", "mm28", "orbstack")
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	clusters := config.Clusters()
	if len(clusters) != 4 {
		t.Fatalf("len(Clusters()) = %d, want 4", len(clusters))
	}

	wantLogicalNames := []string{"dc-a-dmz", "dc-a-internal", "dc-b-dmz", "dc-b-internal"}
	logicalNames := make([]string, 0, len(clusters))
	resourceNames := map[string]struct{}{}
	subnets := map[string]struct{}{}
	for _, cluster := range clusters {
		logicalNames = append(logicalNames, cluster.LogicalName)
		if !strings.HasPrefix(cluster.Name, "mm28-") || !config.ownsResource(cluster.Name) {
			t.Errorf("cluster name %q is not owned by instance", cluster.Name)
		}
		if _, exists := resourceNames[cluster.Name]; exists {
			t.Errorf("duplicate cluster resource name %q", cluster.Name)
		}
		resourceNames[cluster.Name] = struct{}{}
		for _, subnet := range []string{cluster.DockerSubnet, cluster.PodSubnet, cluster.ServiceSubnet} {
			if _, exists := subnets[subnet]; exists {
				t.Errorf("duplicate subnet %q", subnet)
			}
			subnets[subnet] = struct{}{}
		}
	}
	if !slices.Equal(logicalNames, wantLogicalNames) {
		t.Errorf("logical names = %v, want %v", logicalNames, wantLogicalNames)
	}
}

func TestKindConfigUsesDistinctSubnets(t *testing.T) {
	t.Parallel()

	config, err := NewConfig("/workspace/marketmesh", "mm28", "orbstack")
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	for _, cluster := range config.Clusters() {
		generated := kindConfig(cluster)
		for _, expected := range []string{cluster.PodSubnet, cluster.ServiceSubnet, "ipFamily: ipv4"} {
			if !strings.Contains(generated, expected) {
				t.Errorf("kindConfig(%s) does not contain %q", cluster.LogicalName, expected)
			}
		}
	}
}
