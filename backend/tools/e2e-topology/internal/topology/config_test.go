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
		name      string
		root      string
		instance  string
		wantError bool
	}{
		{
			name:     "valid",
			root:     "/workspace/marketmesh",
			instance: "mm44",
		},
		{
			name:      "relative root",
			root:      "marketmesh",
			instance:  "mm44",
			wantError: true,
		},
		{
			name:      "shell metacharacters in instance",
			root:      "/workspace/marketmesh",
			instance:  "mm44;orbctl-delete",
			wantError: true,
		},
		{
			name:      "path separator in instance",
			root:      "/workspace/marketmesh",
			instance:  "../mm44",
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config, err := NewConfig(test.root, test.instance)
			if test.wantError {
				if err == nil {
					t.Fatal("NewConfig() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewConfig() error = %v", err)
			}
			wantState := filepath.Join(test.root, ".cache", "e2e-topology", test.instance)
			if config.StateDir != wantState {
				t.Errorf("StateDir = %q, want %q", config.StateDir, wantState)
			}
		})
	}
}

func TestConfigClusters(t *testing.T) {
	t.Parallel()

	config, err := NewConfig("/workspace/marketmesh", "mm44")
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
	for _, cluster := range clusters {
		logicalNames = append(logicalNames, cluster.LogicalName)
		wantName := "mm44-" + cluster.LogicalName
		if cluster.Name != wantName || !config.ownsResource(cluster.Name) {
			t.Errorf("cluster name %q is not owned by instance, want %q", cluster.Name, wantName)
		}
		if cluster.NodeName != cluster.Name {
			t.Errorf("node name %q does not match machine name %q", cluster.NodeName, cluster.Name)
		}
		if cluster.KubeContext != cluster.Name {
			t.Errorf("kube context %q does not match machine name %q", cluster.KubeContext, cluster.Name)
		}
		if !strings.HasSuffix(cluster.Kubeconfig, filepath.Join("kubeconfigs", cluster.LogicalName+".yaml")) {
			t.Errorf("kubeconfig %q is not under the state kubeconfigs directory", cluster.Kubeconfig)
		}
		if _, exists := resourceNames[cluster.Name]; exists {
			t.Errorf("duplicate cluster resource name %q", cluster.Name)
		}
		resourceNames[cluster.Name] = struct{}{}
	}
	if !slices.Equal(logicalNames, wantLogicalNames) {
		t.Errorf("logical names = %v, want %v", logicalNames, wantLogicalNames)
	}
}
