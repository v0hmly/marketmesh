package workload

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestUndeployInspectsBeforeDeletingExactOwnedResources(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{label: "run-29"}
	manager := &Manager{
		config:   Config{RunID: "run-29", Timeout: time.Minute, Output: io.Discard},
		clusters: []Cluster{{DC: "dc-a", Zone: "dmz", Kubeconfig: "/tmp/a", Context: "dc-a-dmz"}},
		kubectl:  runner,
	}
	if err := manager.Undeploy(t.Context()); err != nil {
		t.Fatalf("Undeploy() error = %v", err)
	}

	firstDelete := -1
	lastDiagnostic := -1
	for index, call := range runner.calls {
		if slices.Contains(call, "delete") && firstDelete == -1 {
			firstDelete = index
		}
		if slices.Contains(call, "logs") {
			lastDiagnostic = index
		}
	}
	if firstDelete <= lastDiagnostic {
		t.Fatalf("first delete index = %d, last diagnostics index = %d", firstDelete, lastDiagnostic)
	}

	want := ownedResources("dmz")
	got := make([]string, 0, len(want))
	for _, call := range runner.calls {
		deleteIndex := slices.Index(call, "delete")
		if deleteIndex >= 0 {
			got = append(got, call[deleteIndex+1])
			if slices.Contains(call, "--all") || slices.Contains(call, "--selector=marketmesh.io/run-id=run-29") {
				t.Fatalf("unsafe delete call = %v", call)
			}
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("deleted resources = %v, want %v", got, want)
	}
}

func TestUndeployRefusesForeignOwner(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{label: "other-run"}
	manager := &Manager{
		config:   Config{RunID: "run-29", Timeout: time.Minute, Output: io.Discard},
		clusters: []Cluster{{DC: "dc-a", Zone: "internal", Kubeconfig: "/tmp/a", Context: "dc-a-internal"}},
		kubectl:  runner,
	}
	err := manager.Undeploy(t.Context())
	if err == nil || !strings.Contains(err.Error(), "refusing to delete foreign resource") {
		t.Fatalf("Undeploy() error = %v, want foreign owner rejection", err)
	}
	for _, call := range runner.calls {
		if slices.Contains(call, "delete") {
			t.Fatalf("foreign resource delete call = %v", call)
		}
	}
}

func TestLimitBufferRejectsUnboundedCommandOutput(t *testing.T) {
	t.Parallel()

	buffer := &limitBuffer{remaining: 4}
	written, err := buffer.Write([]byte("abcdefgh"))
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if written != 8 || buffer.String() != "abcd" || !buffer.truncated {
		t.Fatalf(
			"Write() = (%d, %q, %t), want (8, abcd, true)",
			written,
			buffer.String(),
			buffer.truncated,
		)
	}
}

func TestAssertNamespaceRejectsForeignNamespace(t *testing.T) {
	t.Parallel()

	runner := &namespaceRunner{content: []byte(`{
		"apiVersion":"v1",
		"kind":"Namespace",
		"metadata":{"name":"marketmesh-e2e-tunnel","labels":{"managed-by":"foreign"}}
	}`)}
	manager := &Manager{kubectl: runner}
	_, err := manager.assertNamespace(t.Context(), Cluster{
		DC: "dc-a", Zone: "dmz", Kubeconfig: "/tmp/a", Context: "dc-a-dmz",
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to use foreign namespace") {
		t.Fatalf("assertNamespace() error = %v, want foreign namespace rejection", err)
	}
}

func TestAssertResourceOwnerRejectsForeignExactName(t *testing.T) {
	t.Parallel()

	runner := &namespaceRunner{content: []byte(`{
		"apiVersion":"apps/v1",
		"kind":"Deployment",
		"metadata":{
			"name":"mm29-gateway-in",
			"namespace":"marketmesh-e2e-tunnel",
			"labels":{
				"app.kubernetes.io/managed-by":"marketmesh-e2e-tunnel",
				"marketmesh.io/task":"MM-29",
				"marketmesh.io/run-id":"other-run"
			}
		}
	}`)}
	manager := &Manager{config: Config{RunID: "run-29"}, kubectl: runner}
	err := manager.assertResourceOwner(t.Context(), Cluster{
		DC: "dc-a", Zone: "dmz", Kubeconfig: "/tmp/a", Context: "dc-a-dmz",
	}, "deployment/mm29-gateway-in")
	if err == nil || !strings.Contains(err.Error(), "refusing to replace foreign resource") {
		t.Fatalf("assertResourceOwner() error = %v, want foreign resource rejection", err)
	}
}

func TestValidateClusterIdentitiesRequiresFourPhysicalClusters(t *testing.T) {
	t.Parallel()

	manager := &Manager{
		clusters: []Cluster{
			{DC: "dc-a", Zone: "dmz"},
			{DC: "dc-a", Zone: "internal"},
			{DC: "dc-b", Zone: "dmz"},
			{DC: "dc-b", Zone: "internal"},
		},
		kubectl: &namespaceRunner{content: []byte("same-cluster-uid")},
	}
	err := manager.validateClusterIdentities(t.Context())
	if err == nil || !strings.Contains(err.Error(), "four distinct kubernetes clusters") {
		t.Fatalf("validateClusterIdentities() error = %v, want duplicate rejection", err)
	}
}

type fakeRunner struct {
	label string
	calls [][]string
}

type namespaceRunner struct {
	content []byte
}

func (runner *namespaceRunner) Run(
	context.Context,
	[]byte,
	...string,
) ([]byte, error) {
	return slices.Clone(runner.content), nil
}

func (runner *fakeRunner) Run(
	_ context.Context,
	_ []byte,
	arguments ...string,
) ([]byte, error) {
	runner.calls = append(runner.calls, slices.Clone(arguments))
	for _, argument := range arguments {
		if strings.HasPrefix(argument, "--output=jsonpath=") {
			return []byte(runner.label), nil
		}
	}
	if len(arguments) == 0 {
		return nil, errors.New("empty command")
	}

	return nil, nil
}
