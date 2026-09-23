package rolling

import (
	"bytes"
	"io"
	"slices"
	"sync/atomic"
	"testing"
)

func TestKubernetesArchiveValidatesExactPodAndReplicaSetOwnership(t *testing.T) {
	t.Parallel()

	runtime := &kubernetesArchiveRuntime{runID: "run-34"}
	cluster := Cluster{DC: "dc-a", Zone: "internal"}
	podObject := validArchivePodObject(cluster)
	pod, err := runtime.validatePod(cluster, podObject)
	if err != nil {
		t.Fatalf("validatePod() error = %v", err)
	}
	if pod.OwnerName != "mm29-fake-internal-7f8d9" || pod.OwnerUID != "replicaset-uid" {
		t.Fatalf("archive Pod owner = %q/%q", pod.OwnerName, pod.OwnerUID)
	}
	target, found := targetFor(cluster.DC, ComponentFakeInternal)
	if !found {
		t.Fatal("fake internal target is missing")
	}
	deployment := validDeployment(target)
	replicaSet := validArchiveReplicaSet(cluster, deployment)
	if err := runtime.validateReplicaSet(cluster, pod, deployment, replicaSet); err != nil {
		t.Fatalf("validateReplicaSet() error = %v", err)
	}

	replicaSet.Metadata.OwnerReferences[0].UID = "replacement-deployment-uid"
	if err := runtime.validateReplicaSet(cluster, pod, deployment, replicaSet); err == nil {
		t.Fatal("validateReplicaSet() error = nil for replacement deployment")
	}
}

func TestKubernetesArchiveRejectsForeignPodIdentity(t *testing.T) {
	t.Parallel()

	runtime := &kubernetesArchiveRuntime{runID: "run-34"}
	cluster := Cluster{DC: "dc-a", Zone: "internal"}
	tests := []struct {
		name   string
		mutate func(*podObject)
	}{
		{
			name: "run id",
			mutate: func(pod *podObject) {
				pod.Metadata.Labels["marketmesh.io/run-id"] = "other-run"
			},
		},
		{
			name: "pod uid",
			mutate: func(pod *podObject) {
				pod.Metadata.UID = "UNSAFE_UID"
			},
		},
		{
			name: "replicaset controller",
			mutate: func(pod *podObject) {
				pod.Metadata.OwnerReferences[0].Controller = false
			},
		},
		{
			name: "ready mismatch",
			mutate: func(pod *podObject) {
				pod.Status.ContainerStatuses[0].Ready = false
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			pod := validArchivePodObject(cluster)
			test.mutate(&pod)
			if _, err := runtime.validatePod(cluster, pod); err == nil {
				t.Fatal("validatePod() error = nil")
			}
		})
	}
}

func TestPortForwardOutputAcceptsOnlyExactBoundedLoopbackReadyLine(t *testing.T) {
	t.Parallel()

	var canceled atomic.Bool
	output := newPortForwardOutput(func() { canceled.Store(true) })
	if _, err := output.Write([]byte("Forwarding from 127.0.0.1:54321 -> 9443\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	select {
	case port := <-output.ready:
		if port != 54321 {
			t.Fatalf("ready port = %d", port)
		}
	default:
		t.Fatal("exact port-forward ready line was not accepted")
	}
	if canceled.Load() {
		t.Fatal("exact ready line canceled the process")
	}

	invalid := newPortForwardOutput(func() { canceled.Store(true) })
	oversized := bytes.Repeat([]byte{'x'}, maximumPortForwardOutput+1)
	if _, err := invalid.Write(oversized); err != nil {
		t.Fatalf("oversized Write() error = %v", err)
	}
	if !canceled.Load() {
		t.Fatal("oversized port-forward output did not cancel the process")
	}
}

func TestPortForwardUsesNoShellOrAmbientEnvironment(t *testing.T) {
	t.Parallel()

	command := portForwardCommand(t.Context(), "/exact/kubectl", Cluster{
		Kubeconfig: "/tmp/mm34-kubeconfig",
		Context:    "kind-mm34-dc-a-internal",
	}, "mm29-fake-internal-abc-123", io.Discard)
	wantArguments := []string{
		"/exact/kubectl",
		"--kubeconfig=/tmp/mm34-kubeconfig",
		"--context=kind-mm34-dc-a-internal",
		"port-forward",
		"--namespace=" + Namespace,
		"--address=127.0.0.1",
		"pod/mm29-fake-internal-abc-123",
		":9443",
	}
	if command.Path != "/exact/kubectl" || !slices.Equal(command.Args, wantArguments) {
		t.Fatalf("port-forward command = %q %q", command.Path, command.Args)
	}
	if !slices.Equal(command.Env, []string{"KUBECONFIG="}) {
		t.Fatalf("port-forward environment = %q", command.Env)
	}
	if command.SysProcAttr == nil || !command.SysProcAttr.Setpgid || command.Cancel == nil {
		t.Fatal("port-forward process-group lifecycle is not configured")
	}
}

func validArchivePodObject(cluster Cluster) podObject {
	return podObject{
		APIVersion: "v1",
		Kind:       "Pod",
		Metadata: podMetadata{
			Name: "mm29-fake-internal-7f8d9-abcde", Namespace: Namespace,
			UID: "pod-uid",
			Labels: map[string]string{
				"app.kubernetes.io/name":       "fake-internal",
				"app.kubernetes.io/managed-by": "marketmesh-e2e-tunnel",
				"marketmesh.io/task":           "MM-29",
				"marketmesh.io/run-id":         "run-34",
				"marketmesh.io/dc":             cluster.DC,
				"marketmesh.io/zone":           "internal",
			},
			OwnerReferences: []ownerReference{{
				APIVersion: "apps/v1", Kind: "ReplicaSet",
				Name: "mm29-fake-internal-7f8d9", UID: "replicaset-uid", Controller: true,
			}},
		},
		Status: podStatus{
			Phase:             "Running",
			Conditions:        []podCondition{{Type: "Ready", Status: "True"}},
			ContainerStatuses: []containerStatus{{Name: "fake-internal", Ready: true}},
		},
	}
}

func validArchiveReplicaSet(
	cluster Cluster,
	deployment deploymentObject,
) replicaSetObject {
	return replicaSetObject{
		APIVersion: "apps/v1",
		Kind:       "ReplicaSet",
		Metadata: podMetadata{
			Name: "mm29-fake-internal-7f8d9", Namespace: Namespace,
			UID: "replicaset-uid",
			Labels: map[string]string{
				"app.kubernetes.io/name":       "fake-internal",
				"app.kubernetes.io/managed-by": "marketmesh-e2e-tunnel",
				"marketmesh.io/task":           "MM-29",
				"marketmesh.io/run-id":         "run-34",
				"marketmesh.io/dc":             cluster.DC,
				"marketmesh.io/zone":           "internal",
			},
			OwnerReferences: []ownerReference{{
				APIVersion: "apps/v1", Kind: "Deployment",
				Name: FakeInternalDeployment, UID: deployment.Metadata.UID, Controller: true,
			}},
		},
	}
}
