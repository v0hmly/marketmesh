package rolling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNewKubernetesValidatesExplicitTargets(t *testing.T) {
	t.Parallel()
	temporary := t.TempDir()
	clusters := testClusters(temporary)
	for _, cluster := range clusters {
		if err := os.WriteFile(cluster.Kubeconfig, []byte("apiVersion: v1\n"), 0o600); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}
	}
	tests := []struct {
		name   string
		mutate func([]Cluster) []Cluster
	}{
		{name: "valid"},
		{
			name: "unsafe context",
			mutate: func(input []Cluster) []Cluster {
				input[0].Context = "$(unsafe)"
				return input
			},
		},
		{
			name: "duplicate target",
			mutate: func(input []Cluster) []Cluster {
				input[1].Kubeconfig = input[0].Kubeconfig
				input[1].Context = input[0].Context
				return input
			},
		},
		{
			name: "missing kubeconfig",
			mutate: func(input []Cluster) []Cluster {
				input[0].Kubeconfig = filepath.Join(temporary, "missing")
				return input
			},
		},
		{
			name: "missing cluster",
			mutate: func(input []Cluster) []Cluster {
				return input[:3]
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := slices.Clone(clusters)
			if test.mutate != nil {
				input = test.mutate(input)
			}
			_, err := NewKubernetes(KubernetesConfig{RunID: "run-34", Clusters: input})
			if test.name == "valid" && err != nil {
				t.Fatalf("NewKubernetes() error = %v", err)
			}
			if test.name != "valid" && err == nil {
				t.Fatal("NewKubernetes() error = nil, want unsafe target rejection")
			}
		})
	}
}

func TestNewKubernetesRejectsTypedNilRunner(t *testing.T) {
	t.Parallel()
	var runner *recordingCommandRunner
	clusters := []Cluster{
		{DC: "dc-a", Zone: "dmz"},
		{DC: "dc-a", Zone: "internal"},
		{DC: "dc-b", Zone: "dmz"},
		{DC: "dc-b", Zone: "internal"},
	}
	if _, err := newKubernetes(
		KubernetesConfig{RunID: "run-34"},
		clusters,
		runner,
	); err == nil {
		t.Fatal("newKubernetes() error = nil, want typed nil rejection")
	}
}

func TestKubernetesPrepareValidatesFourOwnedClusters(t *testing.T) {
	t.Parallel()
	runner := &recordingCommandRunner{}
	runner.run = func(_ []byte, arguments []string) ([]byte, error) {
		joined := strings.Join(arguments, " ")
		contextName := argumentValue(arguments, "--context=")
		switch {
		case strings.Contains(joined, "get namespace kube-system"):
			return []byte("uid-" + contextName), nil
		case strings.Contains(joined, "get namespace "+topologyNamespace):
			logicalName := strings.TrimPrefix(contextName, "kind-mm34topo-")
			dc, zone := logicalDCAndZone(logicalName)
			return mustJSON(t, metadataObject{Metadata: objectMetadata{
				Name: topologyNamespace,
				Labels: map[string]string{
					"marketmesh.dev/cluster":           logicalName,
					"marketmesh.dev/dc":                dc,
					"marketmesh.dev/zone":              zone,
					"marketmesh.dev/owner-task":        topologyTaskKey,
					"marketmesh.dev/topology-instance": "mm34topo",
				},
			}}), nil
		case strings.Contains(joined, "get namespace "+Namespace):
			return mustJSON(t, metadataObject{Metadata: objectMetadata{
				Name: Namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "marketmesh-e2e-tunnel",
					"marketmesh.io/task":           "MM-29",
				},
			}}), nil
		case strings.Contains(joined, "get configmap "+ownerConfigMap):
			logicalName := strings.TrimPrefix(contextName, "kind-mm34topo-")
			dc, zone := logicalDCAndZone(logicalName)
			return mustJSON(t, metadataObject{
				Metadata: objectMetadata{
					Name: ownerConfigMap, Namespace: Namespace,
					Labels: map[string]string{
						"marketmesh.io/run-id": "run-34",
						"marketmesh.io/dc":     dc,
						"marketmesh.io/zone":   zone,
					},
				},
				Data: map[string]string{"run_id": "run-34"},
			}), nil
		default:
			return nil, fmt.Errorf("unexpected command: %s", joined)
		}
	}
	kube := newTestKubernetes(t, runner)
	if err := kube.Prepare(t.Context()); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if len(runner.calls) != 16 {
		t.Fatalf("kubectl calls = %d, want 16", len(runner.calls))
	}
}

func TestKubernetesPreflight(t *testing.T) {
	t.Parallel()
	target, _ := targetFor("dc-a", ComponentGatewayIn)
	tests := []struct {
		name   string
		mutate func(*deploymentObject)
	}{
		{name: "valid"},
		{
			name: "foreign run",
			mutate: func(deployment *deploymentObject) {
				deployment.Metadata.Labels["marketmesh.io/run-id"] = "other-run"
			},
		},
		{
			name: "insufficient capacity",
			mutate: func(deployment *deploymentObject) {
				deployment.Status.ReadyReplicas = 1
			},
		},
		{
			name: "unsafe strategy",
			mutate: func(deployment *deploymentObject) {
				deployment.Spec.Strategy.RollingUpdate.MaxUnavailable = json.RawMessage("1")
			},
		},
		{
			name: "missing pre stop",
			mutate: func(deployment *deploymentObject) {
				deployment.Spec.Template.Spec.Containers[0].Lifecycle.PreStop = nil
			},
		},
		{
			name: "short termination budget",
			mutate: func(deployment *deploymentObject) {
				value := int64(20)
				deployment.Spec.Template.Spec.TerminationGracePeriodSeconds = &value
			},
		},
		{
			name: "unobserved generation",
			mutate: func(deployment *deploymentObject) {
				deployment.Status.ObservedGeneration--
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			deployment := validDeployment(target)
			if test.mutate != nil {
				test.mutate(&deployment)
			}
			runner := mutationCommandRunner(t, target, deployment)
			kube := newTestKubernetes(t, runner)
			snapshot, err := kube.Preflight(t.Context(), target)
			if test.name == "valid" {
				if err != nil {
					t.Fatalf("Preflight() error = %v", err)
				}
				if snapshot.UID != "deployment-uid" || snapshot.Revision != 7 || snapshot.Desired != 2 {
					t.Fatalf("Preflight() snapshot = %+v", snapshot)
				}
				return
			}
			if err == nil {
				t.Fatal("Preflight() error = nil, want rejection")
			}
		})
	}
}

func TestKubernetesUpdateUsesStrategicPatchAfterRevalidation(t *testing.T) {
	t.Parallel()
	target, _ := targetFor("dc-a", ComponentGatewayIn)
	deployment := validDeployment(target)
	runner := mutationCommandRunner(t, target, deployment)
	kube := newTestKubernetes(t, runner)
	snapshot := snapshotFor(deployment, target)
	image := "registry.test/marketmesh/gateway-in@sha256:" + strings.Repeat("c", 64)
	change := Change{Kind: ChangeImage, Revision: "gateway-in-image-v2", Image: image}
	if err := kube.Update(t.Context(), target, change, snapshot); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if len(runner.calls) != 6 {
		t.Fatalf("kubectl calls = %d, want boundary validation, revalidation, and patch", len(runner.calls))
	}
	patchCall := runner.calls[5]
	if !slices.Contains(patchCall.arguments, "--type=strategic") ||
		!slices.Contains(patchCall.arguments, "--patch-file=-") {
		t.Fatalf("patch arguments = %v", patchCall.arguments)
	}
	var patch map[string]any
	if err := json.Unmarshal(patchCall.stdin, &patch); err != nil {
		t.Fatalf("patch JSON error = %v", err)
	}
	if !strings.Contains(string(patchCall.stdin), image) ||
		!strings.Contains(string(patchCall.stdin), "gateway-in-image-v2") {
		t.Fatalf("patch = %s", patchCall.stdin)
	}
}

func TestKubernetesUpdateRejectsForeignImageRepository(t *testing.T) {
	t.Parallel()
	target, _ := targetFor("dc-a", ComponentGatewayIn)
	deployment := validDeployment(target)
	runner := mutationCommandRunner(t, target, deployment)
	kube := newTestKubernetes(t, runner)
	change := Change{
		Kind: ChangeImage, Revision: "gateway-in-image-v2",
		Image: "foreign.test/gateway-in@sha256:" + strings.Repeat("c", 64),
	}
	err := kube.Update(t.Context(), target, change, snapshotFor(deployment, target))
	if err == nil || !strings.Contains(err.Error(), "keep the repository") {
		t.Fatalf("Update() error = %v, want repository rejection", err)
	}
	for _, call := range runner.calls {
		if slices.Contains(call.arguments, "patch") {
			t.Fatalf("foreign repository reached mutation: %v", call.arguments)
		}
	}
}

func TestKubernetesUpdateRejectsChangedClusterIdentity(t *testing.T) {
	t.Parallel()
	target, _ := targetFor("dc-a", ComponentGatewayIn)
	deployment := validDeployment(target)
	runner := mutationCommandRunner(t, target, deployment)
	kube := newTestKubernetes(t, runner)
	snapshot := snapshotFor(deployment, target)
	snapshot.ClusterUID = "previous-cluster-uid"
	change := Change{
		Kind: ChangeImage, Revision: "gateway-in-image-v2",
		Image: "registry.test/marketmesh/gateway-in@sha256:" + strings.Repeat("c", 64),
	}
	err := kube.Update(t.Context(), target, change, snapshot)
	if err == nil || !strings.Contains(err.Error(), "changed after preflight") {
		t.Fatalf("Update() error = %v, want changed cluster rejection", err)
	}
	for _, call := range runner.calls {
		if slices.Contains(call.arguments, "patch") {
			t.Fatalf("changed cluster reached mutation: %v", call.arguments)
		}
	}
}

func TestKubernetesReadinessFaultsAreBuiltIn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		dc        string
		component Component
		expected  string
	}{
		{name: "gateway in", dc: "dc-a", component: ComponentGatewayIn, expected: "EXPECTED_GATEWAY_OUT_URI"},
		{name: "gateway out", dc: "dc-b", component: ComponentGatewayOut, expected: "EXPECTED_GATEWAY_IN_URI"},
		{name: "internal service", dc: "dc-a", component: ComponentFakeInternal, expected: "MAX_LEDGER_ENTRIES"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			target, _ := targetFor(test.dc, test.component)
			deployment := validDeployment(target)
			runner := mutationCommandRunner(t, target, deployment)
			kube := newTestKubernetes(t, runner)
			err := kube.InjectReadinessFault(
				t.Context(),
				target,
				Fault{Revision: "unready-v2"},
				snapshotFor(deployment, target),
			)
			if err != nil {
				t.Fatalf("InjectReadinessFault() error = %v", err)
			}
			patch := string(runner.calls[len(runner.calls)-1].stdin)
			if !strings.Contains(patch, test.expected) || !strings.Contains(patch, "unready-v2") {
				t.Fatalf("fault patch = %s", patch)
			}
			if strings.Contains(patch, "image") {
				t.Fatalf("fault patch changes image: %s", patch)
			}
		})
	}
}

func TestKubernetesWaitPreservesCapacityInvariant(t *testing.T) {
	t.Parallel()
	target, _ := targetFor("dc-a", ComponentGatewayIn)
	image := "registry.test/marketmesh/gateway-in@sha256:" + strings.Repeat("c", 64)
	surge := validDeployment(target)
	surge.Metadata.Generation = 8
	surge.Status.ObservedGeneration = 7
	surge.Status.Replicas = 3
	surge.Status.UpdatedReplicas = 1
	surge.Spec.Template.Spec.Containers[0].Image = image
	ready := surge
	ready.Status.ObservedGeneration = 8
	ready.Status.Replicas = 2
	ready.Status.UpdatedReplicas = 2
	runner := &recordingCommandRunner{outputs: [][]byte{mustJSON(t, surge), mustJSON(t, ready)}}
	kube := newTestKubernetes(t, runner)
	err := kube.Wait(t.Context(), target, Expectation{
		UID: "deployment-uid", Image: image, Desired: 2,
	})
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
}

func TestKubernetesWaitRejectsUnavailableReplica(t *testing.T) {
	t.Parallel()
	target, _ := targetFor("dc-a", ComponentGatewayIn)
	deployment := validDeployment(target)
	deployment.Status.UnavailableReplicas = 1
	runner := mutationCommandRunner(t, target, deployment)
	kube := newTestKubernetes(t, runner)
	err := kube.Wait(t.Context(), target, Expectation{
		UID: "deployment-uid", Image: deployment.Spec.Template.Spec.Containers[0].Image, Desired: 2,
	})
	if err == nil || errors.Is(err, ErrReadinessNotReached) {
		t.Fatalf("Wait() error = %v, want immediate capacity violation", err)
	}
}

func TestKubernetesRollbackUsesExactSavedRevision(t *testing.T) {
	t.Parallel()
	target, _ := targetFor("dc-a", ComponentGatewayIn)
	deployment := validDeployment(target)
	deployment.Metadata.Annotations["deployment.kubernetes.io/revision"] = "8"
	deployment.Spec.Template.Metadata.Annotations[rolloutRevisionAnnotation] = "gateway-in-image-v2"
	deployment.Spec.Template.Spec.Containers[0].Image =
		"registry.test/marketmesh/gateway-in@sha256:" + strings.Repeat("c", 64)
	runner := mutationCommandRunner(t, target, deployment)
	kube := newTestKubernetes(t, runner)
	snapshot := Snapshot{
		ClusterUID: "uid-kind-mm34topo-dc-a-dmz",
		UID:        "deployment-uid", Revision: 7, Generation: 7, Desired: 2,
		Image: "registry.test/marketmesh/gateway-in:mm29-commit",
	}
	if err := kube.Rollback(
		t.Context(),
		target,
		"gateway-in-image-v2",
		snapshot,
	); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if len(runner.calls) != 6 ||
		!slices.Contains(runner.calls[5].arguments, "--to-revision=7") {
		t.Fatalf("rollback calls = %+v", runner.calls)
	}
}

func TestKubernetesRollbackRefusesForeignRollout(t *testing.T) {
	t.Parallel()
	target, _ := targetFor("dc-a", ComponentGatewayIn)
	deployment := validDeployment(target)
	deployment.Spec.Template.Metadata.Annotations[rolloutRevisionAnnotation] = "other-revision"
	deployment.Spec.Template.Spec.Containers[0].Image =
		"registry.test/marketmesh/gateway-in@sha256:" + strings.Repeat("d", 64)
	runner := mutationCommandRunner(t, target, deployment)
	kube := newTestKubernetes(t, runner)
	err := kube.Rollback(t.Context(), target, "gateway-in-image-v2", Snapshot{
		ClusterUID: "uid-kind-mm34topo-dc-a-dmz",
		UID:        "deployment-uid", Revision: 7, Desired: 2,
		Image: "registry.test/marketmesh/gateway-in:mm29-commit",
	})
	if err == nil || !strings.Contains(err.Error(), "foreign rollout") {
		t.Fatalf("Rollback() error = %v, want foreign rollout rejection", err)
	}
	for _, call := range runner.calls {
		if slices.Contains(call.arguments, "undo") {
			t.Fatalf("foreign rollout reached undo: %v", call.arguments)
		}
	}
}

func newTestKubernetes(t *testing.T, runner commandRunner) *kubernetes {
	t.Helper()
	clusters := testClusters("/tmp")
	kube, err := newKubernetes(KubernetesConfig{
		RunID: "run-34", Clusters: clusters, PollInterval: time.Nanosecond,
	}, clusters, runner)
	if err != nil {
		t.Fatalf("newKubernetes() error = %v", err)
	}

	return kube
}

func testClusters(directory string) []Cluster {
	definitions := []struct {
		dc   string
		zone string
	}{
		{dc: "dc-a", zone: "dmz"},
		{dc: "dc-a", zone: "internal"},
		{dc: "dc-b", zone: "dmz"},
		{dc: "dc-b", zone: "internal"},
	}
	clusters := make([]Cluster, 0, len(definitions))
	for _, definition := range definitions {
		logicalName := definition.dc + "-" + definition.zone
		resourceName := "mm34topo-" + logicalName
		clusters = append(clusters, Cluster{
			LogicalName:      logicalName,
			ResourceName:     resourceName,
			TopologyInstance: "mm34topo",
			DC:               definition.dc,
			Zone:             definition.zone,
			Kubeconfig:       filepath.Join(directory, logicalName),
			Context:          "kind-" + resourceName,
		})
	}

	return clusters
}

func logicalDCAndZone(logicalName string) (string, string) {
	if strings.HasSuffix(logicalName, "-internal") {
		return strings.TrimSuffix(logicalName, "-internal"), "internal"
	}

	return strings.TrimSuffix(logicalName, "-dmz"), "dmz"
}

func validDeployment(target Target) deploymentObject {
	replicas := int32(2)
	progressDeadline := int32(120)
	terminationGrace := int64(30)
	return deploymentObject{
		Metadata: objectMetadata{
			Name: target.Deployment, Namespace: Namespace, UID: "deployment-uid", Generation: 7,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "marketmesh-e2e-tunnel",
				"marketmesh.io/task":           "MM-29",
				"marketmesh.io/run-id":         "run-34",
				"marketmesh.io/dc":             target.DC,
				"marketmesh.io/zone":           target.Zone,
			},
			Annotations: map[string]string{"deployment.kubernetes.io/revision": "7"},
		},
		Spec: deploymentSpec{
			Replicas: &replicas, ProgressDeadlineSeconds: &progressDeadline,
			Strategy: deploymentStrategy{
				Type: "RollingUpdate",
				RollingUpdate: rollingUpdateStrategy{
					MaxUnavailable: json.RawMessage("0"), MaxSurge: json.RawMessage("1"),
				},
			},
			Template: podTemplate{
				Metadata: objectMetadata{Annotations: map[string]string{}},
				Spec: podSpec{
					TerminationGracePeriodSeconds: &terminationGrace,
					Containers: []containerObject{{
						Name: target.Container, Image: "registry.test/marketmesh/" + target.Container + ":mm29-commit",
						Env:            []environmentVariable{{Name: "SHUTDOWN_TIMEOUT", Value: "20s"}},
						StartupProbe:   json.RawMessage(`{"httpGet":{"path":"/livez"}}`),
						ReadinessProbe: json.RawMessage(`{"httpGet":{"path":"/readyz"}}`),
						Lifecycle: lifecycleObject{
							PreStop: json.RawMessage(`{"exec":{"command":["/binary","prestop"]}}`),
						},
					}},
				},
			},
		},
		Status: deploymentStatus{
			ObservedGeneration: 7, Replicas: 2, UpdatedReplicas: 2,
			ReadyReplicas: 2, AvailableReplicas: 2,
			Conditions: []deploymentCondition{{Type: "Progressing", Status: "True"}},
		},
	}
}

func snapshotFor(deployment deploymentObject, target Target) Snapshot {
	container, _ := findContainer(deployment, target.Container)
	return Snapshot{
		ClusterUID: "uid-kind-mm34topo-" + target.DC + "-" + target.Zone,
		UID:        deployment.Metadata.UID, Revision: 7, Generation: deployment.Metadata.Generation,
		Desired: *deployment.Spec.Replicas, Image: container.Image,
		ConfigRevision: deployment.Spec.Template.Metadata.Annotations[configRevisionAnnotation],
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return encoded
}

func argumentValue(arguments []string, prefix string) string {
	for _, argument := range arguments {
		if value, found := strings.CutPrefix(argument, prefix); found {
			return value
		}
	}
	return ""
}

func mutationCommandRunner(
	t *testing.T,
	target Target,
	deployment deploymentObject,
) *recordingCommandRunner {
	t.Helper()
	runner := &recordingCommandRunner{}
	runner.run = func(_ []byte, arguments []string) ([]byte, error) {
		joined := strings.Join(arguments, " ")
		switch {
		case strings.Contains(joined, "get namespace kube-system"):
			return []byte("uid-kind-mm34topo-" + target.DC + "-" + target.Zone), nil
		case strings.Contains(joined, "get namespace "+topologyNamespace):
			return mustJSON(t, metadataObject{Metadata: objectMetadata{
				Name: topologyNamespace,
				Labels: map[string]string{
					"marketmesh.dev/cluster":           target.DC + "-" + target.Zone,
					"marketmesh.dev/dc":                target.DC,
					"marketmesh.dev/zone":              target.Zone,
					"marketmesh.dev/owner-task":        topologyTaskKey,
					"marketmesh.dev/topology-instance": "mm34topo",
				},
			}}), nil
		case strings.Contains(joined, "get namespace "+Namespace):
			return mustJSON(t, metadataObject{Metadata: objectMetadata{
				Name: Namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "marketmesh-e2e-tunnel",
					"marketmesh.io/task":           "MM-29",
				},
			}}), nil
		case strings.Contains(joined, "get configmap "+ownerConfigMap):
			return mustJSON(t, metadataObject{
				Metadata: objectMetadata{
					Name: ownerConfigMap, Namespace: Namespace,
					Labels: map[string]string{
						"marketmesh.io/run-id": "run-34",
						"marketmesh.io/dc":     target.DC,
						"marketmesh.io/zone":   target.Zone,
					},
				},
				Data: map[string]string{"run_id": "run-34"},
			}), nil
		case strings.Contains(joined, "get deployment "+target.Deployment):
			return mustJSON(t, deployment), nil
		case strings.Contains(joined, "patch deployment "+target.Deployment):
			return nil, nil
		case strings.Contains(joined, "rollout undo deployment/"+target.Deployment):
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected command: %s", joined)
		}
	}

	return runner
}

type commandCall struct {
	stdin     []byte
	arguments []string
}

type recordingCommandRunner struct {
	calls   []commandCall
	outputs [][]byte
	run     func(stdin []byte, arguments []string) ([]byte, error)
}

func (runner *recordingCommandRunner) Run(
	_ context.Context,
	stdin []byte,
	arguments ...string,
) ([]byte, error) {
	runner.calls = append(runner.calls, commandCall{
		stdin: slices.Clone(stdin), arguments: slices.Clone(arguments),
	})
	if runner.run != nil {
		return runner.run(stdin, arguments)
	}
	if len(runner.outputs) == 0 {
		return nil, nil
	}
	output := runner.outputs[0]
	runner.outputs = runner.outputs[1:]
	return output, nil
}
