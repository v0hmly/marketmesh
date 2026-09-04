package podchaos

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/pki"
	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/workload"
)

func TestKubernetesControllerPreflightAndDeleteExactPod(t *testing.T) {
	t.Parallel()

	fixture := newKubeFixture()
	controller := newTestKubernetesController(t, fixture)
	step := Step{DC: DCA, Component: ComponentGatewayIn, Role: RoleActive}

	state, err := controller.Preflight(boundedTestContext(t), fixture.runID, step)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if state.Pod.Name != fixture.pods[0].name ||
		len(state.Pods) != 2 ||
		state.HealthyPaths != 2 || state.HealthyPathsWithoutPod != 1 ||
		state.DesiredReplicas != 2 || state.ReadyReplicas != 2 ||
		state.AvailableReplicas != 2 {
		t.Fatalf("Preflight() state = %+v", state)
	}

	err = controller.Delete(boundedTestContext(t), DeleteRequest{
		RunID: fixture.runID, FaultID: "fault-01", Step: step,
		Pod: state.Pod, GracePeriod: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if len(fixture.deletions) != 1 || fixture.deletions[0].pod != state.Pod ||
		fixture.deletions[0].grace != 30*time.Second {
		t.Fatalf("deletions = %+v", fixture.deletions)
	}
	for _, call := range fixture.calls {
		if strings.Contains(call, "--all") || strings.Contains(call, "*") {
			t.Fatalf("unsafe kubectl call = %q", call)
		}
	}
}

func TestNewKubernetesControllerRejectsUnsafeTargetBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*KubernetesControllerConfig)
	}{
		{name: "missing target", mutate: func(config *KubernetesControllerConfig) {
			config.Targets = config.Targets[:3]
		}},
		{name: "duplicate logical target", mutate: func(config *KubernetesControllerConfig) {
			config.Targets[1] = config.Targets[0]
		}},
		{name: "duplicate physical target", mutate: func(config *KubernetesControllerConfig) {
			config.Targets[1].KubeconfigPath = config.Targets[0].KubeconfigPath
			config.Targets[1].ContextName = config.Targets[0].ContextName
		}},
		{name: "relative kubeconfig", mutate: func(config *KubernetesControllerConfig) {
			config.Targets[0].KubeconfigPath = "relative/kubeconfig"
		}},
		{name: "unsafe context", mutate: func(config *KubernetesControllerConfig) {
			config.Targets[0].ContextName = "--current"
		}},
		{name: "unbounded poll", mutate: func(config *KubernetesControllerConfig) {
			config.PollInterval = time.Millisecond
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newKubeFixture()
			config := kubernetesTestConfig(fixture)
			test.mutate(&config)
			if _, err := newKubernetesController(config, fixture, fixture); err == nil {
				t.Fatal("newKubernetesController() error = nil")
			}
		})
	}
}

func TestKubernetesControllerDeleteKeepsEligibleBaselineWhenActivityMoves(t *testing.T) {
	t.Parallel()

	fixture := newKubeFixture()
	controller := newTestKubernetesController(t, fixture)
	step := Step{DC: DCA, Component: ComponentGatewayIn, Role: RoleActive}
	state, err := controller.Preflight(boundedTestContext(t), fixture.runID, step)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	fixture.snapshots[fixture.pods[0].name], fixture.snapshots[fixture.pods[1].name] =
		fixture.snapshots[fixture.pods[1].name], fixture.snapshots[fixture.pods[0].name]
	fixture.snapshots[fixture.pods[0].name] = withGatewayName(
		fixture.snapshots[fixture.pods[0].name],
		fixture.pods[0].name,
	)
	fixture.snapshots[fixture.pods[1].name] = withGatewayName(
		fixture.snapshots[fixture.pods[1].name],
		fixture.pods[1].name,
	)

	err = controller.Delete(boundedTestContext(t), DeleteRequest{
		RunID: fixture.runID, FaultID: "fault-01", Step: step,
		Pod: state.Pod, GracePeriod: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if len(fixture.deletions) != 1 || fixture.deletions[0].pod != state.Pod {
		t.Fatalf("deletions = %+v", fixture.deletions)
	}
}

func TestKubernetesControllerSelectsDeterministicallyWhenActivityIsTied(t *testing.T) {
	t.Parallel()

	fixture := newKubeFixture()
	fixture.ambiguousRoutingReads = len(fixture.pods)
	controller := newTestKubernetesController(t, fixture)
	state, err := controller.Preflight(
		boundedTestContext(t),
		fixture.runID,
		Step{DC: DCA, Component: ComponentGatewayIn, Role: RoleActive},
	)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if state.Pod.Name != fixture.pods[0].name || fixture.routingReads != len(fixture.pods) {
		t.Fatalf("state = %+v, routing reads = %d", state, fixture.routingReads)
	}
}

func TestKubernetesControllerDeleteRejectsIneligibleBaselinePod(t *testing.T) {
	t.Parallel()

	fixture := newKubeFixture()
	controller := newTestKubernetesController(t, fixture)
	step := Step{DC: DCA, Component: ComponentGatewayIn, Role: RoleActive}
	state, err := controller.Preflight(boundedTestContext(t), fixture.runID, step)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	snapshot := fixture.snapshots[state.Pod.Name]
	for index := range snapshot.Routes {
		snapshot.Routes[index].RouteAllowed = false
	}
	fixture.snapshots[state.Pod.Name] = snapshot

	err = controller.Delete(boundedTestContext(t), DeleteRequest{
		RunID: fixture.runID, FaultID: "fault-01", Step: step,
		Pod: state.Pod, GracePeriod: 30 * time.Second,
	})
	if !errors.Is(err, ErrUnsafeState) {
		t.Fatalf("Delete() error = %v, want ErrUnsafeState", err)
	}
	if len(fixture.deletions) != 0 {
		t.Fatalf("deletions = %+v", fixture.deletions)
	}
}

func TestKubernetesControllerRetriesTransientKubernetesRead(t *testing.T) {
	t.Parallel()

	fixture := newKubeFixture()
	fixture.readFailures = 1
	controller := newTestKubernetesController(t, fixture)
	state, err := controller.Preflight(
		boundedTestContext(t),
		fixture.runID,
		Step{DC: DCA, Component: ComponentGatewayIn, Role: RoleActive},
	)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if state.Pod.Name != fixture.pods[0].name || fixture.readFailures != 0 {
		t.Fatalf("state = %+v, remaining read failures = %d", state, fixture.readFailures)
	}
}

func TestKubernetesControllerRejectsForeignOwnership(t *testing.T) {
	t.Parallel()

	fixture := newKubeFixture()
	fixture.ownerRunID = "mm32-foreign"
	controller := newTestKubernetesController(t, fixture)
	_, err := controller.Preflight(boundedTestContext(t), fixture.runID, Step{
		DC: DCA, Component: ComponentGatewayIn, Role: RoleActive,
	})
	if !errors.Is(err, ErrUnsafeState) {
		t.Fatalf("Preflight() error = %v, want ErrUnsafeState", err)
	}
	if len(fixture.deletions) != 0 {
		t.Fatalf("deletions = %+v", fixture.deletions)
	}
}

func TestKubernetesControllerRejectsNonMM32RunBeforeReadingClusters(t *testing.T) {
	t.Parallel()

	fixture := newKubeFixture()
	controller := newTestKubernetesController(t, fixture)
	_, err := controller.Preflight(boundedTestContext(t), "foreign-run", Step{
		DC: DCA, Component: ComponentGatewayIn, Role: RoleActive,
	})
	if !errors.Is(err, ErrUnsafeState) {
		t.Fatalf("Preflight() error = %v, want ErrUnsafeState", err)
	}
	if len(fixture.calls) != 0 || len(fixture.deletions) != 0 {
		t.Fatalf("calls = %v, deletions = %+v", fixture.calls, fixture.deletions)
	}
}

func TestKubernetesControllerRequiresExactMM29ReplicaCount(t *testing.T) {
	t.Parallel()

	fixture := newKubeFixture()
	fixture.replicas = 3
	controller := newTestKubernetesController(t, fixture)
	_, err := controller.Preflight(boundedTestContext(t), fixture.runID, Step{
		DC: DCA, Component: ComponentGatewayIn, Role: RoleActive,
	})
	if !errors.Is(err, ErrUnsafeState) {
		t.Fatalf("Preflight() error = %v, want ErrUnsafeState", err)
	}
	if len(fixture.deletions) != 0 {
		t.Fatalf("deletions = %+v", fixture.deletions)
	}
}

func TestKubernetesControllerRejectsDuplicateClusterIdentity(t *testing.T) {
	t.Parallel()

	fixture := newKubeFixture()
	fixture.clusterIdentity = "cluster-shared"
	controller := newTestKubernetesController(t, fixture)
	_, err := controller.Preflight(boundedTestContext(t), fixture.runID, Step{
		DC: DCA, Component: ComponentGatewayIn, Role: RoleActive,
	})
	if !errors.Is(err, ErrUnsafeState) {
		t.Fatalf("Preflight() error = %v, want ErrUnsafeState", err)
	}
	if len(fixture.deletions) != 0 {
		t.Fatalf("deletions = %+v", fixture.deletions)
	}
}

func TestKubernetesControllerRejectsUnvalidatedRecoveryBaselineBeforeReading(t *testing.T) {
	t.Parallel()

	fixture := newKubeFixture()
	controller := newTestKubernetesController(t, fixture)
	step := Step{DC: DCA, Component: ComponentGatewayIn, Role: RoleActive}
	baseline, err := controller.Preflight(boundedTestContext(t), fixture.runID, step)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	fixture.calls = nil
	baseline.Pods[0].OwnerRunID = "mm32-foreign"
	_, err = controller.WaitRecovered(boundedTestContext(t), RecoveryRequest{
		RunID: fixture.runID, FaultID: "fault-01", Step: step,
		OldPod: baseline.Pod, Baseline: baseline,
	})
	if !errors.Is(err, ErrUnsafeState) {
		t.Fatalf("WaitRecovered() error = %v, want ErrUnsafeState", err)
	}
	if len(fixture.calls) != 0 || len(fixture.deletions) != 0 {
		t.Fatalf("calls = %v, deletions = %+v", fixture.calls, fixture.deletions)
	}
}

func TestKubernetesControllerWaitsForOneNewUIDAndRoutingCapacity(t *testing.T) {
	t.Parallel()

	fixture := newKubeFixture()
	controller := newTestKubernetesController(t, fixture)
	step := Step{DC: DCA, Component: ComponentGatewayIn, Role: RoleActive}
	baseline, err := controller.Preflight(boundedTestContext(t), fixture.runID, step)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	fixture.pods = []kubePod{
		fixture.pods[1],
		{name: "mm29-gateway-in-new", uid: "pod-uid-new"},
	}
	fixture.snapshots = map[string]RoutingSnapshot{
		fixture.pods[0].name: routingSnapshotForGateway(
			fixture.pods[0].name,
			"dc-a",
			[]RoutingTunnelSnapshot{{
				InstanceID: "22222222222222222222222222222222",
				DataCenter: "dc-a", State: "ready",
			}},
		),
		fixture.pods[1].name: routingSnapshotForGateway(
			fixture.pods[1].name,
			"dc-a",
			[]RoutingTunnelSnapshot{{
				InstanceID: "33333333333333333333333333333333",
				DataCenter: "dc-a", State: "ready", ActiveRequests: 1,
			}},
		),
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	recovered, err := controller.WaitRecovered(ctx, RecoveryRequest{
		RunID: fixture.runID, FaultID: "fault-01", Step: step,
		OldPod: baseline.Pod, Baseline: baseline,
	})
	if err != nil {
		t.Fatalf("WaitRecovered() error = %v", err)
	}
	if recovered.Pod.Name != "mm29-gateway-in-new" ||
		recovered.Pod.UID != "pod-uid-new" || recovered.HealthyPaths != 2 {
		t.Fatalf("WaitRecovered() state = %+v", recovered)
	}
}

func TestKubernetesControllerReturnsOnlyExactOwnedLedgerPods(t *testing.T) {
	t.Parallel()

	routing := newKubeFixture()
	runner := &ledgerKubeFixture{runID: routing.runID}
	config := kubernetesTestConfig(routing)
	controller, err := newKubernetesController(config, runner, routing)
	if err != nil {
		t.Fatalf("newKubernetesController() error = %v", err)
	}
	pods, err := controller.LedgerPods(boundedTestContext(t), routing.runID)
	if err != nil {
		t.Fatalf("LedgerPods() error = %v", err)
	}
	if len(pods) != 4 {
		t.Fatalf("len(LedgerPods()) = %d, want 4", len(pods))
	}
	for _, ledgerPod := range pods {
		pod := ledgerPod.Pod
		if (ledgerPod.DataCenter != DCA && ledgerPod.DataCenter != DCB) ||
			pod.Deployment != workload.FakeInternalDeployment ||
			pod.OwnerRunID != routing.runID || pod.Namespace != workload.Namespace ||
			!strings.HasPrefix(pod.Name, "mm29-fake-internal-") {
			t.Errorf("LedgerPods() contains unsafe pod %+v", ledgerPod)
		}
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "--all") || strings.Contains(call, "*") ||
			strings.Contains(call, "secret") {
			t.Fatalf("unsafe ledger discovery call = %q", call)
		}
	}
}

func TestKubernetesControllerLoadsOnlyVerifiedInternalClientIdentity(t *testing.T) {
	t.Parallel()

	routing := newKubeFixture()
	bundle, err := pki.New(routing.runID, string(DCA), time.Now())
	if err != nil {
		t.Fatalf("pki.New() error = %v", err)
	}
	runner := &ledgerKubeFixture{
		runID: routing.runID,
		tlsData: map[string][]byte{
			"ca.crt":  bundle.InternalCAPEM,
			"tls.crt": bundle.GatewayOutInternal.CertificatePEM,
			"tls.key": bundle.GatewayOutInternal.PrivateKeyPEM,
		},
	}
	controller, err := newKubernetesController(kubernetesTestConfig(routing), runner, routing)
	if err != nil {
		t.Fatalf("newKubernetesController() error = %v", err)
	}
	configuration, err := controller.InternalClientTLSConfig(
		boundedTestContext(t),
		routing.runID,
		DCA,
	)
	if err != nil {
		t.Fatalf("InternalClientTLSConfig() error = %v", err)
	}
	if configuration.MinVersion != tls.VersionTLS13 ||
		configuration.ServerName != "mm29-fake-internal.marketmesh-e2e-tunnel.svc" ||
		len(configuration.Certificates) != 1 || configuration.VerifyConnection == nil {
		t.Fatalf("InternalClientTLSConfig() = %+v", configuration)
	}
	block, rest := pem.Decode(bundle.FakeInternal.CertificatePEM)
	if block == nil || len(rest) != 0 {
		t.Fatal("pem.Decode() did not return one server certificate")
	}
	serverLeaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("x509.ParseCertificate() error = %v", err)
	}
	if err := configuration.VerifyConnection(tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{serverLeaf}},
	}); err != nil {
		t.Fatalf("VerifyConnection() error = %v", err)
	}
	clientBlock, _ := pem.Decode(bundle.GatewayOutInternal.CertificatePEM)
	clientLeaf, err := x509.ParseCertificate(clientBlock.Bytes)
	if err != nil {
		t.Fatalf("x509.ParseCertificate(client) error = %v", err)
	}
	if err := configuration.VerifyConnection(tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{clientLeaf}},
	}); err == nil {
		t.Fatal("VerifyConnection() error = nil for wrong server identity")
	}
}

type kubeFixture struct {
	runID                 string
	ownerRunID            string
	clusterIdentity       string
	ambiguousRoutingReads int
	routingReads          int
	readFailures          int
	replicas              int32
	pods                  []kubePod
	snapshots             map[string]RoutingSnapshot
	calls                 []string
	deletions             []recordedDeletion
}

type kubePod struct {
	name string
	uid  string
}

type recordedDeletion struct {
	pod   PodRef
	grace time.Duration
}

type ledgerKubeFixture struct {
	runID   string
	calls   []string
	tlsData map[string][]byte
}

func (fixture *ledgerKubeFixture) Run(
	_ context.Context,
	target KubernetesTarget,
	arguments ...string,
) ([]byte, error) {
	fixture.calls = append(fixture.calls, strings.Join(arguments, " "))
	if len(arguments) < 2 || arguments[0] != "get" {
		return nil, errors.New("unexpected kubectl call")
	}
	labels := map[string]string{
		"app.kubernetes.io/name":       "fake-internal",
		"app.kubernetes.io/managed-by": "marketmesh-e2e-tunnel",
		"marketmesh.io/task":           "MM-29",
		"marketmesh.io/run-id":         fixture.runID,
		"marketmesh.io/dc":             string(target.DC),
		"marketmesh.io/zone":           target.Zone,
	}
	uidPrefix := string(target.DC) + "-" + target.Zone
	metadata := func(name, uid string, owners []map[string]any) map[string]any {
		return map[string]any{
			"name": name, "namespace": workload.Namespace, "uid": uid,
			"labels": labels, "ownerReferences": owners,
		}
	}
	pod := func(index int) map[string]any {
		name := fmt.Sprintf("mm29-fake-internal-%s-%d", target.DC, index)
		return map[string]any{
			"metadata": metadata(
				name,
				fmt.Sprintf("%s-pod-%d", uidPrefix, index),
				[]map[string]any{controllerReference(
					"ReplicaSet",
					"mm29-fake-internal-rs",
					uidPrefix+"-rs",
				)},
			),
			"status": map[string]any{
				"phase":      "Running",
				"conditions": []map[string]string{{"type": "Ready", "status": "True"}},
			},
		}
	}

	var document any
	switch arguments[1] {
	case "namespace":
		if arguments[2] == "kube-system" {
			document = map[string]any{"metadata": map[string]any{
				"name": "kube-system", "uid": "cluster-" + uidPrefix,
			}}
		} else {
			document = map[string]any{"metadata": map[string]any{
				"name": workload.Namespace, "uid": uidPrefix + "-namespace",
				"labels": map[string]string{
					"app.kubernetes.io/managed-by": "marketmesh-e2e-tunnel",
					"marketmesh.io/task":           "MM-29",
				},
			}}
		}
	case "configmap":
		document = map[string]any{
			"metadata": metadata(workload.OwnerConfigMap, uidPrefix+"-owner", nil),
			"data":     map[string]string{"run_id": fixture.runID},
		}
	case "deployment":
		deploymentMetadata := metadata(
			workload.FakeInternalDeployment,
			uidPrefix+"-deployment",
			nil,
		)
		deploymentMetadata["generation"] = 1
		document = map[string]any{
			"metadata": deploymentMetadata,
			"spec":     map[string]any{"replicas": 2},
			"status": map[string]any{
				"observedGeneration": 1, "replicas": 2, "updatedReplicas": 2,
				"readyReplicas": 2, "availableReplicas": 2, "unavailableReplicas": 0,
			},
		}
	case "pods":
		document = map[string]any{"items": []any{pod(0), pod(1)}}
	case "pod":
		index := 0
		if strings.HasSuffix(arguments[2], "-1") {
			index = 1
		}
		document = pod(index)
	case "replicaset":
		document = map[string]any{"metadata": metadata(
			"mm29-fake-internal-rs",
			uidPrefix+"-rs",
			[]map[string]any{controllerReference(
				"Deployment",
				workload.FakeInternalDeployment,
				uidPrefix+"-deployment",
			)},
		)}
	case "secret":
		document = map[string]any{
			"metadata": metadata(
				workload.GatewayOutInternalTLSSecret,
				uidPrefix+"-internal-client-tls",
				nil,
			),
			"type": "Opaque",
			"data": fixture.tlsData,
		}
	default:
		return nil, fmt.Errorf("unexpected resource %q", arguments[1])
	}
	return json.Marshal(document)
}

func newKubeFixture() *kubeFixture {
	pods := []kubePod{
		{name: "mm29-gateway-in-a", uid: "pod-uid-a"},
		{name: "mm29-gateway-in-b", uid: "pod-uid-b"},
	}
	return &kubeFixture{
		runID: "mm32-kube", ownerRunID: "mm32-kube", replicas: 2, pods: pods,
		snapshots: map[string]RoutingSnapshot{
			pods[0].name: routingSnapshotForGateway(pods[0].name, "dc-a", []RoutingTunnelSnapshot{{
				InstanceID: "11111111111111111111111111111111",
				DataCenter: "dc-a", State: "ready", ActiveRequests: 1,
			}}),
			pods[1].name: routingSnapshotForGateway(pods[1].name, "dc-a", []RoutingTunnelSnapshot{{
				InstanceID: "22222222222222222222222222222222",
				DataCenter: "dc-a", State: "ready",
			}}),
		},
		calls: []string{}, deletions: []recordedDeletion{},
	}
}

func newTestKubernetesController(t *testing.T, fixture *kubeFixture) *KubernetesController {
	t.Helper()
	controller, err := newKubernetesController(kubernetesTestConfig(fixture), fixture, fixture)
	if err != nil {
		t.Fatalf("newKubernetesController() error = %v", err)
	}
	return controller
}

func kubernetesTestConfig(fixture *kubeFixture) KubernetesControllerConfig {
	return KubernetesControllerConfig{
		Targets: []KubernetesTarget{
			{DC: DCA, Zone: ZoneDMZ, KubeconfigPath: "/tmp/mm32-a-dmz", ContextName: "kind-a-dmz"},
			{DC: DCA, Zone: ZoneInternal, KubeconfigPath: "/tmp/mm32-a-internal", ContextName: "kind-a-internal"},
			{DC: DCB, Zone: ZoneDMZ, KubeconfigPath: "/tmp/mm32-b-dmz", ContextName: "kind-b-dmz"},
			{DC: DCB, Zone: ZoneInternal, KubeconfigPath: "/tmp/mm32-b-internal", ContextName: "kind-b-internal"},
		},
		Routing: fixture, PollInterval: 10 * time.Millisecond,
	}
}

func (fixture *kubeFixture) Run(
	_ context.Context,
	target KubernetesTarget,
	arguments ...string,
) ([]byte, error) {
	fixture.calls = append(fixture.calls, strings.Join(arguments, " "))
	if fixture.readFailures > 0 {
		fixture.readFailures--
		return nil, errors.New("transient kubernetes read failure")
	}
	if len(arguments) < 2 || arguments[0] != "get" {
		return nil, errors.New("unexpected kubectl call")
	}
	labels := fixture.labels(target, "gateway-in")
	resource := arguments[1]
	var document any
	switch resource {
	case "namespace":
		name := arguments[2]
		if name == "kube-system" {
			identity := fixture.clusterIdentity
			if identity == "" {
				identity = "cluster-" + string(target.DC) + "-" + target.Zone
			}
			document = map[string]any{"metadata": map[string]any{
				"name": "kube-system",
				"uid":  identity,
			}}
			break
		}
		document = map[string]any{"metadata": map[string]any{
			"name": workload.Namespace, "uid": "namespace-uid",
			"labels": map[string]string{
				"app.kubernetes.io/managed-by": "marketmesh-e2e-tunnel",
				"marketmesh.io/task":           "MM-29",
			},
		}}
	case "configmap":
		document = map[string]any{
			"metadata": fixture.metadata(workload.OwnerConfigMap, "owner-uid", labels, nil),
			"data":     map[string]string{"run_id": fixture.ownerRunID},
		}
	case "deployment":
		metadata := fixture.metadata(workload.GatewayInDeployment, "deployment-uid", labels, nil)
		metadata["generation"] = 1
		document = map[string]any{
			"metadata": metadata,
			"spec":     map[string]any{"replicas": fixture.replicas},
			"status": map[string]any{
				"observedGeneration":  1,
				"replicas":            fixture.replicas,
				"updatedReplicas":     fixture.replicas,
				"readyReplicas":       fixture.replicas,
				"availableReplicas":   fixture.replicas,
				"unavailableReplicas": 0,
			},
		}
	case "pods":
		items := make([]any, 0, len(fixture.pods))
		for _, pod := range fixture.pods {
			items = append(items, fixture.podDocument(labels, pod))
		}
		document = map[string]any{"items": items}
	case "pod":
		if len(arguments) < 3 {
			return nil, errors.New("pod name is missing")
		}
		pod, found := fixture.findPod(arguments[2])
		if !found {
			return nil, errors.New("pod not found")
		}
		document = fixture.podDocument(labels, pod)
	case "replicaset":
		document = map[string]any{"metadata": fixture.metadata(
			"mm29-gateway-in-rs",
			"replicaset-uid",
			labels,
			[]map[string]any{controllerReference("Deployment", workload.GatewayInDeployment, "deployment-uid")},
		)}
	default:
		return nil, fmt.Errorf("unexpected resource %q", resource)
	}
	return json.Marshal(document)
}

func (fixture *kubeFixture) ReadRoutingSnapshot(
	_ context.Context,
	pod PodRef,
) (RoutingSnapshot, error) {
	snapshot, found := fixture.snapshots[pod.Name]
	if !found {
		return RoutingSnapshot{}, errors.New("snapshot not found")
	}
	fixture.routingReads++
	if fixture.routingReads <= fixture.ambiguousRoutingReads {
		snapshot = cloneRoutingSnapshot(snapshot)
		for routeIndex := range snapshot.Routes {
			for tunnelIndex := range snapshot.Routes[routeIndex].Tunnels {
				snapshot.Routes[routeIndex].Tunnels[tunnelIndex].ActiveRequests = 1
			}
		}
	}
	return snapshot, nil
}

func (fixture *kubeFixture) DeleteExactPod(
	_ context.Context,
	pod PodRef,
	grace time.Duration,
) error {
	fixture.deletions = append(fixture.deletions, recordedDeletion{pod: pod, grace: grace})
	return nil
}

func (fixture *kubeFixture) labels(
	target KubernetesTarget,
	component string,
) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       component,
		"app.kubernetes.io/managed-by": "marketmesh-e2e-tunnel",
		"marketmesh.io/task":           "MM-29",
		"marketmesh.io/run-id":         fixture.runID,
		"marketmesh.io/dc":             string(target.DC),
		"marketmesh.io/zone":           target.Zone,
	}
}

func (fixture *kubeFixture) metadata(
	name string,
	uid string,
	labels map[string]string,
	owners []map[string]any,
) map[string]any {
	return map[string]any{
		"name": name, "namespace": workload.Namespace, "uid": uid,
		"labels": labels, "ownerReferences": owners,
	}
}

func (fixture *kubeFixture) podDocument(
	labels map[string]string,
	pod kubePod,
) map[string]any {
	return map[string]any{
		"metadata": fixture.metadata(
			pod.name,
			pod.uid,
			labels,
			[]map[string]any{controllerReference("ReplicaSet", "mm29-gateway-in-rs", "replicaset-uid")},
		),
		"status": map[string]any{
			"phase":      "Running",
			"conditions": []map[string]string{{"type": "Ready", "status": "True"}},
		},
	}
}

func (fixture *kubeFixture) findPod(name string) (kubePod, bool) {
	for _, pod := range fixture.pods {
		if pod.name == name {
			return pod, true
		}
	}
	return kubePod{}, false
}

func controllerReference(kind string, name string, uid string) map[string]any {
	return map[string]any{
		"apiVersion": "apps/v1", "kind": kind, "name": name, "uid": uid,
		"controller": true,
	}
}

func withGatewayName(snapshot RoutingSnapshot, name string) RoutingSnapshot {
	snapshot.GatewayInInstance = name
	return snapshot
}

func cloneRoutingSnapshot(snapshot RoutingSnapshot) RoutingSnapshot {
	snapshot.Routes = append([]RoutingRouteSnapshot(nil), snapshot.Routes...)
	for index := range snapshot.Routes {
		snapshot.Routes[index].Tunnels = append(
			[]RoutingTunnelSnapshot(nil),
			snapshot.Routes[index].Tunnels...,
		)
	}
	return snapshot
}

func boundedTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)
	return ctx
}
