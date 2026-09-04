package podchaos

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/pki"
	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/workload"
)

const (
	ZoneDMZ                 = "dmz"
	ZoneInternal            = "internal"
	expectedPodReplicas     = 2
	maxKubectlOutputBytes   = 1024 * 1024
	maxTLSMaterialBytes     = 64 * 1024
	defaultKubePollInterval = time.Second
)

var errRecoveryPending = errors.New("podchaos: recovery pending")

// KubernetesTarget is one explicit cluster boundary. Ambient kubeconfig and
// current-context state are never consulted.
type KubernetesTarget struct {
	DC             DC
	Zone           string
	KubeconfigPath string
	ContextName    string
}

// KubernetesControllerConfig contains the four exact cluster targets and the
// loopback-only routing reader used for dynamic role resolution.
type KubernetesControllerConfig struct {
	Targets      []KubernetesTarget
	Routing      RoutingReader
	KubectlPath  string
	PollInterval time.Duration
}

// KubernetesController owns fail-closed inventory reads and one exact
// UID-preconditioned pod deletion at a time.
type KubernetesController struct {
	targets      map[string]KubernetesTarget
	routing      RoutingReader
	kubectl      kubeCommandRunner
	deleter      exactPodDeleter
	pollInterval time.Duration
}

// LedgerPod binds one exact fake-internal pod to its finite logical DC without
// exposing topology addresses to the MM-31 ledger contract.
type LedgerPod struct {
	DataCenter DC
	Pod        PodRef
}

type kubeCommandRunner interface {
	Run(context.Context, KubernetesTarget, ...string) ([]byte, error)
}

type exactPodDeleter interface {
	DeleteExactPod(context.Context, PodRef, time.Duration) error
}

type kubectlCommandRunner struct {
	path string
}

type workloadState struct {
	desired   int32
	ready     int32
	available int32
	pods      []PodRef
}

// NewKubernetesController validates four distinct explicit targets and creates
// the production kubectl adapters.
func NewKubernetesController(
	config KubernetesControllerConfig,
) (*KubernetesController, error) {
	if err := validateKubectlPath(config.KubectlPath); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfiguration, err)
	}
	for index := range config.Targets {
		kubeconfig := config.Targets[index].KubeconfigPath
		if !filepath.IsAbs(kubeconfig) || filepath.Clean(kubeconfig) != kubeconfig ||
			kubeconfig == string(filepath.Separator) {
			return nil, fmt.Errorf("%w: kubeconfig path is invalid", ErrInvalidConfiguration)
		}
		info, err := os.Stat(kubeconfig)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: kubeconfig is not a regular file", ErrInvalidConfiguration)
		}
	}
	runner := kubectlCommandRunner{path: config.KubectlPath}
	return newKubernetesController(
		config,
		runner,
		newProxyPodDeleter(config.KubectlPath),
	)
}

func newKubernetesController(
	config KubernetesControllerConfig,
	runner kubeCommandRunner,
	deleter exactPodDeleter,
) (*KubernetesController, error) {
	if config.Routing == nil || runner == nil || deleter == nil {
		return nil, fmt.Errorf("%w: kubernetes adapters are required", ErrInvalidConfiguration)
	}
	if config.PollInterval == 0 {
		config.PollInterval = defaultKubePollInterval
	}
	if config.PollInterval < 10*time.Millisecond || config.PollInterval > time.Minute {
		return nil, fmt.Errorf("%w: kubernetes poll interval is outside bounds", ErrInvalidConfiguration)
	}
	if len(config.Targets) != 4 {
		return nil, fmt.Errorf("%w: exactly four kubernetes targets are required", ErrInvalidConfiguration)
	}

	targets := make(map[string]KubernetesTarget, len(config.Targets))
	physical := make(map[string]struct{}, len(config.Targets))
	for _, target := range config.Targets {
		if target.DC != DCA && target.DC != DCB ||
			target.Zone != ZoneDMZ && target.Zone != ZoneInternal ||
			!filepath.IsAbs(target.KubeconfigPath) ||
			filepath.Clean(target.KubeconfigPath) != target.KubeconfigPath ||
			!isExactArgument(target.ContextName) {
			return nil, fmt.Errorf("%w: kubernetes target is invalid", ErrInvalidConfiguration)
		}
		key := targetKey(target.DC, target.Zone)
		if _, exists := targets[key]; exists {
			return nil, fmt.Errorf("%w: kubernetes target is duplicated", ErrInvalidConfiguration)
		}
		physicalKey := target.KubeconfigPath + "\x00" + target.ContextName
		if _, exists := physical[physicalKey]; exists {
			return nil, fmt.Errorf("%w: physical kubernetes target is duplicated", ErrInvalidConfiguration)
		}
		targets[key] = target
		physical[physicalKey] = struct{}{}
	}

	return &KubernetesController{
		targets: targets, routing: config.Routing, kubectl: runner,
		deleter: deleter, pollInterval: config.PollInterval,
	}, nil
}

// Preflight resolves the requested role from current routing state only after
// validating the complete Kubernetes ownership and rollout state.
func (controller *KubernetesController) Preflight(
	ctx context.Context,
	runID string,
	step Step,
) (State, error) {
	if !hasDeadline(ctx) || controller == nil || !validStep(step) || !isMM32RunID(runID) {
		return State{}, fmt.Errorf("%w: preflight input is invalid", ErrUnsafeState)
	}

	ticker := time.NewTicker(controller.pollInterval)
	defer ticker.Stop()
	for {
		state, err := controller.preflightOnce(ctx, runID, step)
		if err == nil {
			return state, nil
		}
		if !errors.Is(err, errRecoveryPending) {
			return State{}, err
		}
		select {
		case <-ctx.Done():
			return State{}, errors.Join(ErrUnsafeState, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (controller *KubernetesController) preflightOnce(
	ctx context.Context,
	runID string,
	step Step,
) (State, error) {
	if err := controller.validateClusterIdentities(ctx); err != nil {
		return State{}, err
	}
	targetWorkload, err := controller.readWorkload(ctx, runID, step.DC, step.Component)
	if err != nil {
		return State{}, err
	}
	gatewayWorkload := targetWorkload
	if step.Component != ComponentGatewayIn {
		gatewayWorkload, err = controller.readWorkload(ctx, runID, step.DC, ComponentGatewayIn)
		if err != nil {
			return State{}, err
		}
	}
	snapshots, err := controller.readRoutingSnapshots(ctx, gatewayWorkload.pods)
	if err != nil {
		return State{}, err
	}
	selected, healthy, retained, err := resolveRole(step, targetWorkload.pods, snapshots)
	if err != nil {
		return State{}, err
	}

	return State{
		Selected: step, Pod: selected, Pods: slices.Clone(targetWorkload.pods),
		DesiredReplicas: targetWorkload.desired,
		ReadyReplicas:   targetWorkload.ready, AvailableReplicas: targetWorkload.available,
		HealthyPaths: healthy, HealthyPathsWithoutPod: retained,
		IsPodReady: true, IsTunnelReady: true, IsRolling: false,
	}, nil
}

func (controller *KubernetesController) validateClusterIdentities(ctx context.Context) error {
	keys := make([]string, 0, len(controller.targets))
	for key := range controller.targets {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	identities := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		target := controller.targets[key]
		var namespace namespaceDocument
		if err := controller.getList(
			ctx,
			target,
			&namespace,
			"get",
			"namespace",
			"kube-system",
			"--output=json",
		); err != nil {
			return err
		}
		identity := namespace.Metadata.UID
		if !isExactArgument(identity) || namespace.Metadata.Name != "kube-system" {
			return fmt.Errorf("%w: cluster identity is invalid", ErrUnsafeState)
		}
		if _, exists := identities[identity]; exists {
			return fmt.Errorf("%w: kubernetes targets share a cluster", ErrUnsafeState)
		}
		identities[identity] = struct{}{}
	}
	return nil
}

// Delete revalidates the exact baseline pod, ownership and retained routing
// capacity immediately before sending a UID-preconditioned Kubernetes DELETE.
// The ephemeral active/standby role is not resolved a second time.
func (controller *KubernetesController) Delete(
	ctx context.Context,
	request DeleteRequest,
) error {
	if !hasDeadline(ctx) || controller == nil || !validStep(request.Step) ||
		!isMM32RunID(request.RunID) || !isDNSLabel(request.FaultID) ||
		request.GracePeriod <= 0 || request.GracePeriod%time.Second != 0 {
		return fmt.Errorf("%w: delete request is invalid", ErrUnsafeState)
	}
	if err := validatePodRef(request.RunID, request.Pod); err != nil {
		return err
	}
	if err := controller.recheckExactPod(
		ctx,
		request.RunID,
		request.Step,
		request.Pod,
	); err != nil {
		return err
	}
	return controller.deleter.DeleteExactPod(ctx, request.Pod, request.GracePeriod)
}

func (controller *KubernetesController) recheckExactPod(
	ctx context.Context,
	runID string,
	step Step,
	wanted PodRef,
) error {
	if err := controller.validateClusterIdentities(ctx); err != nil {
		return err
	}
	targetWorkload, err := controller.readWorkload(ctx, runID, step.DC, step.Component)
	if err != nil {
		return err
	}
	gatewayWorkload := targetWorkload
	if step.Component != ComponentGatewayIn {
		gatewayWorkload, err = controller.readWorkload(ctx, runID, step.DC, ComponentGatewayIn)
		if err != nil {
			return err
		}
	}
	snapshots, err := controller.readRoutingSnapshots(ctx, gatewayWorkload.pods)
	if err != nil {
		return err
	}
	candidates, err := eligibleCandidates(
		step.Component,
		step.DC,
		targetWorkload.pods,
		snapshots,
	)
	if err != nil {
		return errors.Join(ErrUnsafeState, fmt.Errorf("recheck routing capacity: %w", err))
	}
	if len(candidates) < 2 {
		return fmt.Errorf("%w: retained routing capacity is insufficient", ErrUnsafeState)
	}
	for _, candidate := range candidates {
		if candidate.pod == wanted {
			return nil
		}
	}
	return fmt.Errorf("%w: baseline pod is no longer eligible", ErrUnsafeState)
}

// WaitRecovered waits for exactly one new pod outside the baseline UID set,
// then requires full replica and routing capacity before returning it.
func (controller *KubernetesController) WaitRecovered(
	ctx context.Context,
	request RecoveryRequest,
) (State, error) {
	if !hasDeadline(ctx) || controller == nil || !validStep(request.Step) ||
		!isMM32RunID(request.RunID) || !isDNSLabel(request.FaultID) ||
		len(request.Baseline.Pods) != int(request.Baseline.DesiredReplicas) {
		return State{}, fmt.Errorf("%w: recovery request is invalid", ErrUnsafeState)
	}
	if err := validateBaseline(request.RunID, request.Step, request.Baseline); err != nil {
		return State{}, err
	}
	if request.OldPod != request.Baseline.Pod {
		return State{}, fmt.Errorf("%w: recovery old pod does not match baseline", ErrUnsafeState)
	}
	if err := controller.validateClusterIdentities(ctx); err != nil {
		return State{}, err
	}

	ticker := time.NewTicker(controller.pollInterval)
	defer ticker.Stop()
	for {
		state, err := controller.recoveredState(ctx, request)
		if err == nil {
			return state, nil
		}
		if !errors.Is(err, errRecoveryPending) {
			return State{}, err
		}
		select {
		case <-ctx.Done():
			return State{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (controller *KubernetesController) recoveredState(
	ctx context.Context,
	request RecoveryRequest,
) (State, error) {
	current, err := controller.readWorkload(ctx, request.RunID, request.Step.DC, request.Step.Component)
	if err != nil {
		if errors.Is(err, errRecoveryPending) {
			return State{}, err
		}
		return State{}, err
	}
	if current.desired != request.Baseline.DesiredReplicas {
		return State{}, fmt.Errorf("%w: deployment replica target changed", ErrUnsafeState)
	}
	baselineUIDs := make(map[string]struct{}, len(request.Baseline.Pods))
	for _, pod := range request.Baseline.Pods {
		baselineUIDs[pod.UID] = struct{}{}
	}
	var replacement PodRef
	newPods := 0
	oldFound := false
	for _, pod := range current.pods {
		if pod.UID == request.OldPod.UID {
			oldFound = true
		}
		if _, existed := baselineUIDs[pod.UID]; !existed {
			replacement = pod
			newPods++
		}
	}
	if oldFound || newPods != 1 {
		return State{}, errRecoveryPending
	}

	gatewayWorkload := current
	if request.Step.Component != ComponentGatewayIn {
		gatewayWorkload, err = controller.readWorkload(
			ctx,
			request.RunID,
			request.Step.DC,
			ComponentGatewayIn,
		)
		if err != nil {
			return State{}, err
		}
	}
	snapshots, err := controller.readRoutingSnapshots(ctx, gatewayWorkload.pods)
	if err != nil {
		return State{}, errRecoveryPending
	}
	candidates, err := eligibleCandidates(request.Step.Component, request.Step.DC, current.pods, snapshots)
	if err != nil {
		return State{}, errRecoveryPending
	}
	isReplacementEligible := false
	for _, candidate := range candidates {
		if candidate.pod == replacement {
			isReplacementEligible = true
			break
		}
	}
	if !isReplacementEligible || len(candidates) < request.Baseline.HealthyPaths {
		return State{}, errRecoveryPending
	}

	return State{
		Selected: request.Step, Pod: replacement, Pods: slices.Clone(current.pods),
		DesiredReplicas: current.desired, ReadyReplicas: current.ready,
		AvailableReplicas: current.available,
		HealthyPaths:      len(candidates), HealthyPathsWithoutPod: len(candidates) - 1,
		IsPodReady: true, IsTunnelReady: true, IsRolling: false,
	}, nil
}

func eligibleCandidates(
	component Component,
	dc DC,
	pods []PodRef,
	snapshots []RoutingSnapshot,
) ([]routingCandidate, error) {
	if component == ComponentGatewayIn {
		return gatewayInCandidates(dc, pods, snapshots)
	}
	return gatewayOutCandidates(dc, pods, snapshots)
}

func (controller *KubernetesController) readRoutingSnapshots(
	ctx context.Context,
	pods []PodRef,
) ([]RoutingSnapshot, error) {
	sorted := slices.Clone(pods)
	slices.SortFunc(sorted, comparePodRef)
	snapshots := make([]RoutingSnapshot, 0, len(sorted))
	for _, pod := range sorted {
		snapshot, err := controller.routing.ReadRoutingSnapshot(ctx, pod)
		if err != nil {
			return nil, fmt.Errorf("%w: reading routing snapshot", ErrUnsafeState)
		}
		if snapshot.GatewayInInstance != pod.Name {
			return nil, fmt.Errorf("%w: routing snapshot belongs to another pod", ErrUnsafeState)
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func (controller *KubernetesController) readWorkload(
	ctx context.Context,
	runID string,
	dc DC,
	component Component,
) (workloadState, error) {
	zone := ZoneInternal
	deployment := workload.GatewayOutDeployment
	if component == ComponentGatewayIn {
		zone = ZoneDMZ
		deployment = workload.GatewayInDeployment
	}
	return controller.readNamedWorkload(
		ctx,
		runID,
		dc,
		zone,
		deployment,
		string(component),
	)
}

// LedgerPods returns the exact, ready fake-internal replicas whose immutable
// ledgers must be read after probe shutdown. The same namespace, owner,
// Deployment, ReplicaSet and pod ownership chain used by destructive preflight
// is revalidated; only the two explicit internal clusters are consulted.
func (controller *KubernetesController) LedgerPods(
	ctx context.Context,
	runID string,
) ([]LedgerPod, error) {
	if !hasDeadline(ctx) || controller == nil || !isMM32RunID(runID) {
		return nil, fmt.Errorf("%w: ledger pod input is invalid", ErrUnsafeState)
	}
	if err := controller.validateClusterIdentities(ctx); err != nil {
		return nil, err
	}
	pods := make([]LedgerPod, 0, expectedPodReplicas*2)
	for _, dc := range []DC{DCA, DCB} {
		state, err := controller.readNamedWorkload(
			ctx,
			runID,
			dc,
			ZoneInternal,
			workload.FakeInternalDeployment,
			"fake-internal",
		)
		if err != nil {
			return nil, err
		}
		for _, pod := range state.pods {
			pods = append(pods, LedgerPod{DataCenter: dc, Pod: pod})
		}
	}
	slices.SortFunc(pods, func(left, right LedgerPod) int {
		if comparison := strings.Compare(
			left.Pod.ContextName,
			right.Pod.ContextName,
		); comparison != 0 {
			return comparison
		}
		return comparePodRef(left.Pod, right.Pod)
	})
	return pods, nil
}

// InternalClientTLSConfig loads the exact MM-29 gateway-out client identity
// for one internal cluster and verifies it before returning an in-memory TLS
// configuration. Secret bytes are cleared before return and are never logged
// or written to diagnostics.
func (controller *KubernetesController) InternalClientTLSConfig(
	ctx context.Context,
	runID string,
	dc DC,
) (*tls.Config, error) {
	if !hasDeadline(ctx) || controller == nil || !isMM32RunID(runID) ||
		(dc != DCA && dc != DCB) {
		return nil, fmt.Errorf("%w: internal TLS input is invalid", ErrUnsafeState)
	}
	if err := controller.validateClusterIdentities(ctx); err != nil {
		return nil, err
	}
	target, found := controller.targets[targetKey(dc, ZoneInternal)]
	if !found {
		return nil, fmt.Errorf("%w: internal kubernetes target is missing", ErrUnsafeState)
	}
	if err := controller.validateNamespaceAndOwner(ctx, target, runID); err != nil {
		return nil, err
	}
	var secret secretDocument
	if err := controller.getExact(
		ctx,
		target,
		&secret,
		"secret",
		workload.GatewayOutInternalTLSSecret,
	); err != nil {
		return nil, err
	}
	if secret.Metadata.Name != workload.GatewayOutInternalTLSSecret ||
		secret.Metadata.Namespace != workload.Namespace || secret.Metadata.UID == "" ||
		secret.Type != "Opaque" || !validBaseLabels(target, runID, secret.Metadata.Labels) ||
		len(secret.Data) != 3 {
		clearTLSSecret(secret.Data)
		return nil, fmt.Errorf("%w: internal TLS secret ownership is invalid", ErrUnsafeState)
	}
	defer clearTLSSecret(secret.Data)
	caPEM := secret.Data["ca.crt"]
	certificatePEM := secret.Data["tls.crt"]
	privateKeyPEM := secret.Data["tls.key"]
	materialBytes := len(caPEM) + len(certificatePEM) + len(privateKeyPEM)
	if len(caPEM) == 0 || len(certificatePEM) == 0 || len(privateKeyPEM) == 0 ||
		materialBytes > maxTLSMaterialBytes {
		return nil, fmt.Errorf("%w: internal TLS material is invalid", ErrUnsafeState)
	}

	certificate, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil || len(certificate.Certificate) != 1 {
		return nil, fmt.Errorf("%w: internal TLS key pair is invalid", ErrUnsafeState)
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("%w: internal TLS leaf is invalid", ErrUnsafeState)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("%w: internal TLS CA is invalid", ErrUnsafeState)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return nil, fmt.Errorf("%w: internal TLS client chain is invalid", ErrUnsafeState)
	}
	expectedClientURI := (&url.URL{
		Scheme: "spiffe",
		Host:   pki.TrustDomain,
		Path:   "/e2e/" + runID + "/" + string(dc) + "/gateway-out",
	}).String()
	if len(leaf.URIs) != 1 || leaf.URIs[0] == nil || leaf.URIs[0].String() != expectedClientURI {
		return nil, fmt.Errorf("%w: internal TLS client identity is invalid", ErrUnsafeState)
	}
	expectedServerURI := (&url.URL{
		Scheme: "spiffe",
		Host:   pki.TrustDomain,
		Path:   "/e2e/" + runID + "/" + string(dc) + "/fake-internal",
	}).String()

	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		ServerName:   pki.FakeInternalService + "." + pki.Namespace + ".svc",
		RootCAs:      roots,
		Certificates: []tls.Certificate{certificate},
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.VerifiedChains[0]) == 0 {
				return errors.New("podchaos: internal server chain is not verified")
			}
			identities := state.VerifiedChains[0][0].URIs
			if len(identities) != 1 || identities[0] == nil ||
				identities[0].String() != expectedServerURI {
				return errors.New("podchaos: internal server identity mismatch")
			}
			return nil
		},
	}, nil
}

func clearTLSSecret(data map[string][]byte) {
	for key, value := range data {
		clear(value)
		delete(data, key)
	}
}

func (controller *KubernetesController) readNamedWorkload(
	ctx context.Context,
	runID string,
	dc DC,
	zone string,
	deployment string,
	component string,
) (workloadState, error) {
	target, found := controller.targets[targetKey(dc, zone)]
	if !found {
		return workloadState{}, fmt.Errorf("%w: kubernetes target is missing", ErrUnsafeState)
	}
	if err := controller.validateNamespaceAndOwner(ctx, target, runID); err != nil {
		return workloadState{}, err
	}

	var deploymentObject deploymentDocument
	if err := controller.getExact(
		ctx,
		target,
		&deploymentObject,
		"deployment",
		deployment,
	); err != nil {
		return workloadState{}, err
	}
	if err := validateDeployment(target, runID, deployment, deploymentObject); err != nil {
		return workloadState{}, err
	}
	desired := *deploymentObject.Spec.Replicas
	if deploymentObject.Status.ObservedGeneration < deploymentObject.Metadata.Generation ||
		deploymentObject.Status.Replicas != desired ||
		deploymentObject.Status.UpdatedReplicas != desired ||
		deploymentObject.Status.ReadyReplicas != desired ||
		deploymentObject.Status.AvailableReplicas != desired ||
		deploymentObject.Status.UnavailableReplicas != 0 {
		return workloadState{}, errRecoveryPending
	}

	var podList podListDocument
	selector := "app.kubernetes.io/name=" + component +
		",marketmesh.io/run-id=" + runID
	if err := controller.getList(
		ctx,
		target,
		&podList,
		"pods",
		"--namespace="+workload.Namespace,
		"--selector="+selector,
		"--output=json",
	); err != nil {
		return workloadState{}, err
	}
	if len(podList.Items) != int(desired) {
		return workloadState{}, errRecoveryPending
	}

	pods := make([]PodRef, 0, len(podList.Items))
	replicaSets := map[string]replicaSetDocument{}
	for _, listed := range podList.Items {
		if !isDNSSubdomain(listed.Metadata.Name) {
			return workloadState{}, fmt.Errorf("%w: listed pod name is invalid", ErrUnsafeState)
		}
		var pod podDocument
		if err := controller.getExact(ctx, target, &pod, "pod", listed.Metadata.Name); err != nil {
			return workloadState{}, err
		}
		owner, err := controllerOwner(pod.Metadata.OwnerReferences, "ReplicaSet")
		if err != nil {
			return workloadState{}, err
		}
		replicaSet, cached := replicaSets[owner.Name]
		if !cached {
			if err := controller.getExact(ctx, target, &replicaSet, "replicaset", owner.Name); err != nil {
				return workloadState{}, err
			}
			if err := validateReplicaSet(
				target,
				runID,
				deploymentObject.Metadata.UID,
				component,
				replicaSet,
			); err != nil {
				return workloadState{}, err
			}
			replicaSets[owner.Name] = replicaSet
		}
		if owner.UID != replicaSet.Metadata.UID || pod.Metadata.Name != listed.Metadata.Name ||
			!validOwnedMetadata(target, runID, component, pod.Metadata) ||
			pod.Metadata.Namespace != workload.Namespace ||
			pod.Metadata.UID == "" || pod.Metadata.DeletionTimestamp != nil ||
			pod.Status.Phase != "Running" || !podReady(pod.Status.Conditions) {
			return workloadState{}, errRecoveryPending
		}
		pods = append(pods, PodRef{
			KubeconfigPath: target.KubeconfigPath,
			ContextName:    target.ContextName,
			Namespace:      workload.Namespace,
			Deployment:     deployment,
			Name:           pod.Metadata.Name,
			UID:            pod.Metadata.UID,
			OwnerRunID:     runID,
		})
	}
	slices.SortFunc(pods, comparePodRef)

	return workloadState{
		desired: desired, ready: deploymentObject.Status.ReadyReplicas,
		available: deploymentObject.Status.AvailableReplicas, pods: pods,
	}, nil
}

func (controller *KubernetesController) validateNamespaceAndOwner(
	ctx context.Context,
	target KubernetesTarget,
	runID string,
) error {
	var namespace namespaceDocument
	if err := controller.getExact(ctx, target, &namespace, "namespace", workload.Namespace); err != nil {
		return err
	}
	if namespace.Metadata.Name != workload.Namespace || namespace.Metadata.UID == "" ||
		namespace.Metadata.Labels["app.kubernetes.io/managed-by"] != "marketmesh-e2e-tunnel" ||
		namespace.Metadata.Labels["marketmesh.io/task"] != "MM-29" {
		return fmt.Errorf("%w: namespace ownership is invalid", ErrUnsafeState)
	}
	var owner configMapDocument
	if err := controller.getExact(ctx, target, &owner, "configmap", workload.OwnerConfigMap); err != nil {
		return err
	}
	if owner.Data["run_id"] != runID ||
		owner.Metadata.Name != workload.OwnerConfigMap ||
		owner.Metadata.Namespace != workload.Namespace || owner.Metadata.UID == "" ||
		!validBaseLabels(target, runID, owner.Metadata.Labels) {
		return fmt.Errorf("%w: owner configmap is invalid", ErrUnsafeState)
	}
	return nil
}

func (controller *KubernetesController) getExact(
	ctx context.Context,
	target KubernetesTarget,
	destination any,
	kind string,
	name string,
) error {
	if !isExactArgument(kind) || !isDNSSubdomain(name) {
		return fmt.Errorf("%w: exact kubernetes read is invalid", ErrUnsafeState)
	}
	return controller.getList(
		ctx,
		target,
		destination,
		"get",
		kind,
		name,
		"--namespace="+workload.Namespace,
		"--output=json",
	)
}

func (controller *KubernetesController) getList(
	ctx context.Context,
	target KubernetesTarget,
	destination any,
	arguments ...string,
) error {
	if len(arguments) == 0 || arguments[0] != "get" {
		arguments = append([]string{"get"}, arguments...)
	}
	output, err := controller.kubectl.Run(ctx, target, arguments...)
	if err != nil {
		return fmt.Errorf("%w: kubernetes read failed", errRecoveryPending)
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: kubernetes response is invalid", ErrUnsafeState)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("%w: kubernetes response has trailing content", ErrUnsafeState)
	}
	return nil
}

func (runner kubectlCommandRunner) Run(
	ctx context.Context,
	target KubernetesTarget,
	arguments ...string,
) ([]byte, error) {
	base := []string{
		"--kubeconfig=" + target.KubeconfigPath,
		"--context=" + target.ContextName,
	}
	// #nosec G204 -- kubectl is an explicit validated executable, no shell is
	// used, and targets plus all arguments are built from finite internal values
	// or exact allowlist-validated Kubernetes identifiers.
	command := exec.CommandContext(ctx, runner.path, append(base, arguments...)...)
	output := &boundedBuffer{remaining: maxKubectlOutputBytes}
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	if output.isTruncated {
		return output.Bytes(), errors.Join(err, errors.New("podchaos: kubectl output exceeded bounds"))
	}
	return output.Bytes(), err
}

type boundedBuffer struct {
	bytes.Buffer
	remaining   int
	isTruncated bool
}

func (buffer *boundedBuffer) Write(content []byte) (int, error) {
	original := len(content)
	if len(content) > buffer.remaining {
		content = content[:max(buffer.remaining, 0)]
		buffer.isTruncated = true
	}
	buffer.remaining -= len(content)
	_, _ = buffer.Buffer.Write(content)
	return original, nil
}

type objectMetadata struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	UID               string            `json:"uid"`
	Generation        int64             `json:"generation"`
	Labels            map[string]string `json:"labels"`
	OwnerReferences   []ownerReference  `json:"ownerReferences"`
	DeletionTimestamp *string           `json:"deletionTimestamp"`
}

type ownerReference struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	Controller *bool  `json:"controller"`
}

type namespaceDocument struct {
	Metadata objectMetadata `json:"metadata"`
}

type configMapDocument struct {
	Metadata objectMetadata    `json:"metadata"`
	Data     map[string]string `json:"data"`
}

type secretDocument struct {
	Metadata objectMetadata    `json:"metadata"`
	Type     string            `json:"type"`
	Data     map[string][]byte `json:"data"`
}

type deploymentDocument struct {
	Metadata objectMetadata `json:"metadata"`
	Spec     struct {
		Replicas *int32 `json:"replicas"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration  int64 `json:"observedGeneration"`
		Replicas            int32 `json:"replicas"`
		UpdatedReplicas     int32 `json:"updatedReplicas"`
		ReadyReplicas       int32 `json:"readyReplicas"`
		AvailableReplicas   int32 `json:"availableReplicas"`
		UnavailableReplicas int32 `json:"unavailableReplicas"`
	} `json:"status"`
}

type replicaSetDocument struct {
	Metadata objectMetadata `json:"metadata"`
}

type podDocument struct {
	Metadata objectMetadata `json:"metadata"`
	Status   struct {
		Phase      string         `json:"phase"`
		Conditions []podCondition `json:"conditions"`
	} `json:"status"`
}

type podListDocument struct {
	Items []podDocument `json:"items"`
}

type podCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

func validateDeployment(
	target KubernetesTarget,
	runID string,
	name string,
	deployment deploymentDocument,
) error {
	if deployment.Metadata.Name != name ||
		deployment.Metadata.Namespace != workload.Namespace ||
		deployment.Metadata.UID == "" || deployment.Metadata.Generation <= 0 ||
		deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != expectedPodReplicas ||
		!validOwnedMetadata(target, runID, strings.TrimPrefix(name, "mm29-"), deployment.Metadata) {
		return fmt.Errorf("%w: deployment ownership is invalid", ErrUnsafeState)
	}
	return nil
}

func validateReplicaSet(
	target KubernetesTarget,
	runID string,
	deploymentUID string,
	expectedComponent string,
	replicaSet replicaSetDocument,
) error {
	owner, err := controllerOwner(replicaSet.Metadata.OwnerReferences, "Deployment")
	if err != nil {
		return err
	}
	if owner.UID != deploymentUID ||
		replicaSet.Metadata.Namespace != workload.Namespace ||
		replicaSet.Metadata.UID == "" ||
		!isWorkloadComponent(expectedComponent) ||
		!validOwnedMetadata(target, runID, expectedComponent, replicaSet.Metadata) {
		return fmt.Errorf("%w: replicaset ownership is invalid", ErrUnsafeState)
	}
	return nil
}

func isWorkloadComponent(component string) bool {
	return component == string(ComponentGatewayIn) ||
		component == string(ComponentGatewayOut) || component == "fake-internal"
}

func controllerOwner(references []ownerReference, kind string) (ownerReference, error) {
	var result ownerReference
	count := 0
	for _, reference := range references {
		if reference.Controller != nil && *reference.Controller {
			result = reference
			count++
		}
	}
	if count != 1 || result.APIVersion != "apps/v1" ||
		result.Kind != kind || result.Name == "" || result.UID == "" {
		return ownerReference{}, fmt.Errorf("%w: kubernetes owner chain is invalid", ErrUnsafeState)
	}
	return result, nil
}

func validOwnedMetadata(
	target KubernetesTarget,
	runID string,
	component string,
	metadata objectMetadata,
) bool {
	return validBaseLabels(target, runID, metadata.Labels) &&
		metadata.Labels["app.kubernetes.io/name"] == component
}

func validBaseLabels(target KubernetesTarget, runID string, labels map[string]string) bool {
	return labels["app.kubernetes.io/managed-by"] == "marketmesh-e2e-tunnel" &&
		labels["marketmesh.io/task"] == "MM-29" &&
		labels["marketmesh.io/run-id"] == runID &&
		labels["marketmesh.io/dc"] == string(target.DC) &&
		labels["marketmesh.io/zone"] == target.Zone
}

func podReady(conditions []podCondition) bool {
	ready := 0
	for _, condition := range conditions {
		if condition.Type == "Ready" {
			ready++
			if condition.Status != "True" {
				return false
			}
		}
	}
	return ready == 1
}

func targetKey(dc DC, zone string) string {
	return string(dc) + "/" + zone
}
