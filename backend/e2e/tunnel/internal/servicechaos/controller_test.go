package servicechaos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/workload"
)

func TestRunRestoresEveryFaultAndPreservesGatewayOutPods(t *testing.T) {
	t.Parallel()

	runner := newFakeKubernetes("mm33-happy")
	manager := testManager(t, runner, "mm33-happy")
	observations := make([]Observation, 0, 28)
	observer := ObserverFunc(func(_ context.Context, observation Observation) error {
		observations = append(observations, observation)
		if !observation.RequireRead || !observation.RequireMutation {
			t.Fatalf("observation = %#v, read and mutation must be required", observation)
		}
		if !runner.ready(observation.EligibleDC) {
			t.Fatalf("eligible DC %s is not ready during %#v", observation.EligibleDC, observation)
		}
		if observation.Phase == PhaseActive && !runner.faultIsActive(observation) {
			t.Fatalf("fault is not active during %#v", observation)
		}
		if observation.Phase == PhaseRecovered && !runner.ready(observation.FaultedDC) {
			t.Fatalf("faulted DC %s did not recover during %#v", observation.FaultedDC, observation)
		}

		return nil
	})

	if err := manager.Run(t.Context(), observer); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(observations) != 28 {
		t.Fatalf("observation count = %d, want 28", len(observations))
	}
	for _, dc := range []string{"dc-a", "dc-b"} {
		state := runner.clusters[dc]
		if !runner.ready(dc) || state.replicas != desiredReplicas || !state.serviceExists {
			t.Fatalf("cluster %s was not restored: %#v", dc, state)
		}
		if !equalStringMap(state.selector, runner.expectedSelector()) {
			t.Fatalf("cluster %s selector = %v", dc, state.selector)
		}
		if len(state.gatewayOutPods) != desiredReplicas {
			t.Fatalf("cluster %s gateway-out Pods changed: %v", dc, state.gatewayOutPods)
		}
	}
	for _, call := range runner.calls {
		if len(call.arguments) < 3 || !strings.HasPrefix(call.arguments[0], "--kubeconfig=") ||
			!strings.HasPrefix(call.arguments[1], "--context=") {
			t.Fatalf("kubectl call does not use explicit target: %v", call.arguments)
		}
		if slices.Contains(call.arguments, "namespace/"+workload.Namespace) ||
			slices.Contains(call.arguments, "deployment/"+workload.GatewayOutDeployment) &&
				(slices.Contains(call.arguments, "delete") || slices.Contains(call.arguments, "scale")) {
			t.Fatalf("unsafe mutation = %v", call.arguments)
		}
	}
}

func TestObserverFailureCapturesDiagnosticsBeforeRestoring(t *testing.T) {
	t.Parallel()

	runner := newFakeKubernetes("mm33-observer-failure")
	manager := testManager(t, runner, "mm33-observer-failure")
	target := manager.clusters[0]
	other := manager.clusters[1]
	observer := ObserverFunc(func(_ context.Context, observation Observation) error {
		if observation.Phase == PhaseActive {
			return errors.New("probe rejected availability")
		}
		return nil
	})

	err := manager.runRestorableFault(
		t.Context(),
		target,
		other,
		FaultScaleToZero,
		observer,
		func(ctx context.Context, cluster Cluster) error { return manager.scale(ctx, cluster, 0) },
		func(ctx context.Context, cluster Cluster) error {
			return manager.waitDeploymentReplicas(ctx, cluster, 0)
		},
		func(ctx context.Context, cluster Cluster) error {
			return manager.scale(ctx, cluster, desiredReplicas)
		},
	)
	if err == nil || !strings.Contains(err.Error(), "probe rejected availability") {
		t.Fatalf("runRestorableFault() error = %v, want observer failure", err)
	}
	if !runner.ready(target.DC) {
		t.Fatalf("target was not restored: %#v", runner.clusters[target.DC])
	}
	activeIndex := runner.callIndex(func(call recordedCall) bool {
		return slices.Contains(call.arguments, "--replicas=0")
	})
	diagnosticIndex := runner.callIndex(func(call recordedCall) bool {
		return slices.Contains(call.arguments, "logs")
	})
	restoreIndex := runner.callIndex(func(call recordedCall) bool {
		return slices.Contains(call.arguments, "--replicas=2")
	})
	if activeIndex < 0 || diagnosticIndex <= activeIndex || restoreIndex <= diagnosticIndex {
		t.Fatalf(
			"call order active=%d diagnostics=%d restore=%d; calls=%v",
			activeIndex,
			diagnosticIndex,
			restoreIndex,
			runner.calls,
		)
	}
}

func TestPreflightRefusesForeignExactResourcesWithoutMutation(t *testing.T) {
	t.Parallel()

	runner := newFakeKubernetes("foreign-run")
	manager := testManager(t, runner, "mm33-owned")
	err := manager.Run(t.Context(), ObserverFunc(func(context.Context, Observation) error {
		return nil
	}))
	if err == nil || !strings.Contains(err.Error(), "refusing foreign or unexpected resource") {
		t.Fatalf("Run() error = %v, want foreign resource rejection", err)
	}
	for _, call := range runner.calls {
		if slices.Contains(call.arguments, "delete") || slices.Contains(call.arguments, "scale") ||
			slices.Contains(call.arguments, "patch") || slices.Contains(call.arguments, "apply") {
			t.Fatalf("mutation executed for foreign resource: %v", call.arguments)
		}
	}
}

func TestValidateConfigRejectsUnsafeTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config Config
		want   string
	}{
		{
			name:   "task prefix is required",
			config: Config{RunID: "other-run", Timeout: time.Minute},
			want:   "mm33-* prefix",
		},
		{
			name:   "timeout is bounded",
			config: Config{RunID: "mm33-timeout", Timeout: 31 * time.Minute},
			want:   "timeout is outside bounds",
		},
		{
			name: "exact cluster count",
			config: Config{
				RunID: "mm33-count", Timeout: time.Minute, Clusters: []Cluster{{DC: "dc-a"}},
			},
			want: "exactly two internal clusters",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateConfig(&test.config)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateConfig() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateConfigNormalizesTwoExplicitKubeconfigs(t *testing.T) {
	t.Parallel()

	first, err := os.CreateTemp(t.TempDir(), "dc-a-kubeconfig-")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	second, err := os.CreateTemp(t.TempDir(), "dc-b-kubeconfig-")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	config := Config{
		RunID:   "mm33-valid",
		Timeout: time.Minute,
		Clusters: []Cluster{
			{DC: "dc-a", Kubeconfig: first.Name(), Context: "mm33-dc-a-internal"},
			{DC: "dc-b", Kubeconfig: second.Name(), Context: "mm33-dc-b-internal"},
		},
	}
	if err := validateConfig(&config); err != nil {
		t.Fatalf("validateConfig() error = %v", err)
	}
	for _, cluster := range config.Clusters {
		if !strings.HasPrefix(cluster.Context, "mm33-") || !strings.HasPrefix(cluster.Kubeconfig, "/") {
			t.Fatalf("cluster was not kept explicit and absolute: %#v", cluster)
		}
	}
}

func TestLimitBufferRejectsUnboundedKubectlOutput(t *testing.T) {
	t.Parallel()

	buffer := &limitBuffer{remaining: 4}
	written, err := buffer.Write([]byte("abcdefgh"))
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if written != 8 || buffer.String() != "abcd" || !buffer.truncated {
		t.Fatalf("Write() = (%d, %q, %t), want (8, abcd, true)", written, buffer.String(), buffer.truncated)
	}
}

func testManager(t *testing.T, runner *fakeKubernetes, runID string) *Manager {
	t.Helper()

	clusters := []Cluster{
		{DC: "dc-a", Kubeconfig: "/tmp/mm33-dc-a", Context: "mm33-dc-a-internal"},
		{DC: "dc-b", Kubeconfig: "/tmp/mm33-dc-b", Context: "mm33-dc-b-internal"},
	}
	manager, err := newManager(Config{
		RunID: runID, Timeout: 10 * time.Second, Clusters: clusters,
	}, clusters, runner)
	if err != nil {
		t.Fatalf("newManager() error = %v", err)
	}
	manager.pollInterval = time.Nanosecond

	return manager
}

type fakeKubernetes struct {
	runID    string
	clusters map[string]*fakeCluster
	calls    []recordedCall
}

type fakeCluster struct {
	replicas       int32
	serviceExists  bool
	selector       map[string]string
	fakePods       map[string]string
	gatewayOutPods map[string]string
	nextPod        int
	deletedPods    int
}

type recordedCall struct {
	arguments []string
	stdin     []byte
}

func newFakeKubernetes(runID string) *fakeKubernetes {
	runner := &fakeKubernetes{runID: runID, clusters: make(map[string]*fakeCluster, 2)}
	for _, dc := range []string{"dc-a", "dc-b"} {
		runner.clusters[dc] = &fakeCluster{
			replicas: desiredReplicas, serviceExists: true,
			selector: runner.expectedSelector(),
			fakePods: map[string]string{
				"mm29-fake-internal-" + dc + "-0": "fake-" + dc + "-uid-0",
				"mm29-fake-internal-" + dc + "-1": "fake-" + dc + "-uid-1",
			},
			gatewayOutPods: map[string]string{
				"mm29-gateway-out-" + dc + "-0": "gateway-" + dc + "-uid-0",
				"mm29-gateway-out-" + dc + "-1": "gateway-" + dc + "-uid-1",
			},
		}
	}

	return runner
}

func (runner *fakeKubernetes) Run(
	_ context.Context,
	stdin []byte,
	arguments ...string,
) ([]byte, error) {
	runner.calls = append(runner.calls, recordedCall{
		arguments: slices.Clone(arguments), stdin: slices.Clone(stdin),
	})
	if len(arguments) < 3 {
		return nil, errors.New("fake kubectl: explicit target and command are required")
	}
	dc := strings.TrimPrefix(arguments[1], "--context=mm33-")
	dc = strings.TrimSuffix(dc, "-internal")
	state := runner.clusters[dc]
	if state == nil {
		return nil, errors.New("fake kubectl: unknown context")
	}
	command := arguments[2:]

	switch command[0] {
	case "get":
		return runner.get(dc, state, command)
	case "rollout":
		if state.replicas != desiredReplicas {
			return nil, errors.New("fake kubectl: deployment is not ready")
		}
		return nil, nil
	case "scale":
		for _, argument := range command {
			if strings.HasPrefix(argument, "--replicas=") {
				value, err := strconv.Atoi(strings.TrimPrefix(argument, "--replicas="))
				if err != nil {
					return nil, err
				}
				state.replicas = int32(value)
				return nil, nil
			}
		}
	case "patch":
		for _, argument := range command {
			if strings.HasPrefix(argument, "--patch=") {
				var operations []jsonPatchOperation
				if err := json.Unmarshal([]byte(strings.TrimPrefix(argument, "--patch=")), &operations); err != nil {
					return nil, err
				}
				state.selector = operations[0].Value
				return nil, nil
			}
		}
	case "delete":
		resource := command[1]
		if strings.HasPrefix(resource, "pod/") {
			name := strings.TrimPrefix(resource, "pod/")
			if _, found := state.fakePods[name]; !found {
				return nil, errors.New("fake kubectl: Pod not found")
			}
			delete(state.fakePods, name)
			state.nextPod++
			state.deletedPods++
			replacement := fmt.Sprintf("mm29-fake-internal-%s-replacement-%d", dc, state.nextPod)
			state.fakePods[replacement] = fmt.Sprintf("fake-%s-replacement-uid-%d", dc, state.nextPod)
			return nil, nil
		}
		if resource == "service/"+workload.FakeInternalService {
			state.serviceExists = false
			return nil, nil
		}
	case "apply":
		var service kubernetesObject
		if err := json.Unmarshal(stdin, &service); err != nil {
			return nil, err
		}
		selector, err := decodeSelector(service.Spec.Selector)
		if err != nil {
			return nil, err
		}
		state.serviceExists = true
		state.selector = selector
		return nil, nil
	case "logs":
		return []byte("safe diagnostic log\n"), nil
	}

	return nil, fmt.Errorf("fake kubectl: unsupported command %v", command)
}

func (runner *fakeKubernetes) get(
	dc string,
	state *fakeCluster,
	arguments []string,
) ([]byte, error) {
	resource := arguments[1]
	if resource == "namespace/kube-system" {
		return []byte("cluster-" + dc + "-uid"), nil
	}
	if strings.Contains(resource, ",") || resource == "events" {
		return []byte("safe diagnostics\n"), nil
	}
	if resource == "deployment/"+workload.FakeInternalDeployment {
		selector, _ := json.Marshal(map[string]any{"matchLabels": runner.expectedSelector()})
		return json.Marshal(kubernetesObject{
			APIVersion: "apps/v1", Kind: "Deployment",
			Metadata: runner.metadata(workload.FakeInternalDeployment, fakeInternalValue, dc, "deployment-"+dc),
			Spec:     objectSpec{Replicas: &state.replicas, Selector: selector},
			Status:   objectStatus{AvailableReplicas: state.replicas},
		})
	}
	if resource == "service/"+workload.FakeInternalService {
		if !state.serviceExists {
			if slices.Contains(arguments, "--ignore-not-found=true") {
				return nil, nil
			}
			return nil, errors.New("fake kubectl: Service not found")
		}
		if slices.Contains(arguments, "--output=name") {
			return []byte("service/" + workload.FakeInternalService), nil
		}
		selector, _ := json.Marshal(state.selector)
		return json.Marshal(kubernetesObject{
			APIVersion: "v1", Kind: "Service",
			Metadata: runner.metadata(workload.FakeInternalService, fakeInternalValue, dc, "service-"+dc),
			Spec: objectSpec{
				Selector: selector,
				Ports:    []servicePort{{Name: "grpc", Port: 9443, TargetPort: "grpc", Protocol: "TCP"}},
			},
		})
	}
	if resource == "pods" {
		app := fakeInternalValue
		pods := state.fakePods
		if slices.ContainsFunc(arguments, func(argument string) bool {
			return strings.Contains(argument, nameLabel+"="+gatewayOutValue)
		}) {
			app = gatewayOutValue
			pods = state.gatewayOutPods
		}
		list := kubernetesList{Items: make([]kubernetesObject, 0, len(pods))}
		for name, uid := range pods {
			list.Items = append(list.Items, kubernetesObject{
				Metadata: runner.metadata(name, app, dc, uid),
			})
		}
		return json.Marshal(list)
	}
	if resource == "endpoints/"+workload.FakeInternalService {
		endpoints := endpointsObject{}
		if runner.ready(dc) {
			endpoints.Subsets = []endpointSubset{{Addresses: []struct{}{{}}}}
		}
		return json.Marshal(endpoints)
	}

	return nil, fmt.Errorf("fake kubectl: unsupported get %v", arguments)
}

func (runner *fakeKubernetes) metadata(name string, app string, dc string, uid string) objectMetadata {
	return objectMetadata{
		Name: name, Namespace: workload.Namespace, UID: uid,
		Labels: map[string]string{
			managedByLabel: managedByValue,
			taskLabel:      workloadTaskValue,
			runIDLabel:     runner.runID,
			dcLabel:        dc,
			zoneLabel:      internalZoneValue,
			nameLabel:      app,
		},
	}
}

func (runner *fakeKubernetes) expectedSelector() map[string]string {
	return map[string]string{nameLabel: fakeInternalValue, runIDLabel: runner.runID}
}

func (runner *fakeKubernetes) ready(dc string) bool {
	state := runner.clusters[dc]
	return state != nil && state.replicas == desiredReplicas && state.serviceExists &&
		equalStringMap(state.selector, runner.expectedSelector())
}

func (runner *fakeKubernetes) faultIsActive(observation Observation) bool {
	state := runner.clusters[observation.FaultedDC]
	switch observation.Fault {
	case FaultDeletePods:
		return state.deletedPods > 0
	case FaultScaleToZero:
		return state.replicas == 0
	case FaultEmptySelector:
		return state.serviceExists && !equalStringMap(state.selector, runner.expectedSelector())
	case FaultRecreateService:
		return !state.serviceExists
	default:
		return false
	}
}

func (runner *fakeKubernetes) callIndex(match func(recordedCall) bool) int {
	for index, call := range runner.calls {
		if match(call) {
			return index
		}
	}
	return -1
}
