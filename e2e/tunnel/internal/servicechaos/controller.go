// Package servicechaos owns the bounded MM-33 fault lifecycle for the fake
// internal Kubernetes Service. It never creates or deletes a cluster.
package servicechaos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/pki"
	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/workload"
)

const (
	managedByLabel = "app.kubernetes.io/managed-by"
	nameLabel      = "app.kubernetes.io/name"
	runIDLabel     = "marketmesh.io/run-id"
	taskLabel      = "marketmesh.io/task"
	dcLabel        = "marketmesh.io/dc"
	zoneLabel      = "marketmesh.io/zone"

	managedByValue    = "marketmesh-e2e-tunnel"
	fakeInternalValue = "fake-internal"
	gatewayOutValue   = "gateway-out"
	workloadTaskValue = "MM-29"
	internalZoneValue = "internal"
	mm33RunPrefix     = "mm33-"

	desiredReplicas       = 2
	maxCommandOutputBytes = 1024 * 1024
	defaultPollInterval   = 250 * time.Millisecond
)

// Fault is one finite MM-33 disruption. Arbitrary Kubernetes resources or
// patches are deliberately not accepted from callers.
type Fault string

const (
	FaultDeletePods      Fault = "delete-pods"
	FaultScaleToZero     Fault = "scale-to-zero"
	FaultEmptySelector   Fault = "empty-service-selector"
	FaultRecreateService Fault = "recreate-service"
)

// Phase locates an observation relative to a fault mutation.
type Phase string

const (
	PhaseBaseline  Phase = "baseline"
	PhaseActive    Phase = "active"
	PhaseRecovered Phase = "recovered"
)

// Observation tells a continuous probe what it must verify. Both read and
// mutating flows are required for every phase.
type Observation struct {
	Fault           Fault
	Phase           Phase
	FaultedDC       string
	EligibleDC      string
	RequireRead     bool
	RequireMutation bool
}

// Observer bridges the fault lifecycle to the MM-31 continuous probe. The
// controller blocks in Observe so the probe can collect a bounded sample.
type Observer interface {
	Observe(context.Context, Observation) error
}

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(context.Context, Observation) error

// Observe calls function.
func (function ObserverFunc) Observe(ctx context.Context, observation Observation) error {
	return function(ctx, observation)
}

// Cluster is one explicit disposable internal-cluster boundary.
type Cluster struct {
	DC         string
	Kubeconfig string
	Context    string
}

// Config contains every mutable MM-33 input. No ambient KUBECONFIG is read.
type Config struct {
	RunID    string
	Timeout  time.Duration
	Clusters []Cluster
	Output   io.Writer
}

// Manager mutates and restores only the exact MM-29 fake-internal resources.
type Manager struct {
	config       Config
	clusters     []Cluster
	kubectl      commandRunner
	pollInterval time.Duration
}

type commandRunner interface {
	Run(context.Context, []byte, ...string) ([]byte, error)
}

type kubectlRunner struct {
	path string
}

// New validates the two explicit internal clusters before returning a
// controller. A task-specific mm33-* run ID is mandatory.
func New(config Config) (*Manager, error) {
	if err := validateConfig(&config); err != nil {
		return nil, err
	}
	kubectlPath, err := exec.LookPath("kubectl")
	if err != nil {
		return nil, errors.New("service chaos: kubectl is required")
	}

	return newManager(config, config.Clusters, kubectlRunner{path: kubectlPath})
}

func newManager(config Config, clusters []Cluster, runner commandRunner) (*Manager, error) {
	if runner == nil {
		return nil, errors.New("service chaos: command runner is required")
	}
	if config.Output == nil {
		config.Output = io.Discard
	}

	return &Manager{
		config:       config,
		clusters:     slices.Clone(clusters),
		kubectl:      runner,
		pollInterval: defaultPollInterval,
	}, nil
}

// Run executes every finite fault in both DCs, one DC at a time. It validates
// that the other DC is ready before every mutation and that gateway-out Pod
// identities remain unchanged across the complete run.
func (manager *Manager) Run(ctx context.Context, observer Observer) error {
	if ctx == nil {
		return errors.New("service chaos: context must not be nil")
	}
	if observer == nil {
		return errors.New("service chaos: observer is required")
	}
	ctx, cancel := context.WithTimeout(ctx, manager.config.Timeout)
	defer cancel()

	if err := manager.preflight(ctx); err != nil {
		return err
	}
	gatewayOutPods, err := manager.gatewayOutPodIdentities(ctx)
	if err != nil {
		return err
	}

	for _, target := range manager.clusters {
		other := manager.otherCluster(target.DC)
		if err := manager.deletePodsSequentially(ctx, target, other, observer); err != nil {
			return err
		}
		for _, scenario := range []struct {
			fault   Fault
			apply   func(context.Context, Cluster) error
			active  func(context.Context, Cluster) error
			restore func(context.Context, Cluster) error
		}{
			{
				fault: FaultScaleToZero,
				apply: func(runCtx context.Context, cluster Cluster) error {
					return manager.scale(runCtx, cluster, 0)
				},
				active: func(runCtx context.Context, cluster Cluster) error {
					return manager.waitDeploymentReplicas(runCtx, cluster, 0)
				},
				restore: func(runCtx context.Context, cluster Cluster) error {
					return manager.scale(runCtx, cluster, desiredReplicas)
				},
			},
			{
				fault: FaultEmptySelector,
				apply: func(runCtx context.Context, cluster Cluster) error {
					return manager.patchServiceSelector(runCtx, cluster, map[string]string{
						"marketmesh.io/mm33-disabled": manager.config.RunID,
					})
				},
				active: manager.waitEndpointsEmpty,
				restore: func(runCtx context.Context, cluster Cluster) error {
					return manager.patchServiceSelector(runCtx, cluster, manager.expectedSelector())
				},
			},
			{
				fault:   FaultRecreateService,
				apply:   manager.deleteService,
				active:  manager.waitServiceAbsent,
				restore: manager.recreateService,
			},
		} {
			if err := manager.runRestorableFault(
				ctx,
				target,
				other,
				scenario.fault,
				observer,
				scenario.apply,
				scenario.active,
				scenario.restore,
			); err != nil {
				return err
			}
		}
	}

	currentGatewayOutPods, err := manager.gatewayOutPodIdentities(ctx)
	if err != nil {
		return err
	}
	if !equalIdentitySets(gatewayOutPods, currentGatewayOutPods) {
		return errors.New("service chaos: gateway-out Pod identities changed during MM-33")
	}

	_, _ = fmt.Fprintf(manager.config.Output, "MM-33 service chaos завершён: run_id=%s\n", manager.config.RunID)

	return nil
}

func (manager *Manager) deletePodsSequentially(
	ctx context.Context,
	target Cluster,
	other Cluster,
	observer Observer,
) error {
	if err := manager.prepareFault(ctx, target, other); err != nil {
		return err
	}
	pods, err := manager.pods(ctx, target, fakeInternalValue)
	if err != nil {
		return err
	}
	if len(pods) != desiredReplicas {
		return fmt.Errorf("service chaos: expected %d fake-internal Pods in %s", desiredReplicas, target.DC)
	}
	if err := manager.observe(ctx, observer, FaultDeletePods, PhaseBaseline, target, other); err != nil {
		return err
	}

	for _, pod := range pods {
		if err := manager.assertOwnedMetadata(pod.Metadata, pod.Metadata.Name, target); err != nil {
			return err
		}
		apply := func(runCtx context.Context, cluster Cluster) error {
			_, deleteErr := manager.runKubectl(
				runCtx,
				cluster,
				nil,
				"delete",
				"pod/"+pod.Metadata.Name,
				"--namespace="+workload.Namespace,
				"--wait=true",
				"--timeout="+manager.config.Timeout.String(),
			)
			if deleteErr != nil {
				return fmt.Errorf("service chaos: deleting Pod in %s", cluster.DC)
			}
			return nil
		}
		if err := manager.runRestorableFault(
			ctx,
			target,
			other,
			FaultDeletePods,
			observer,
			apply,
			func(context.Context, Cluster) error { return nil },
			func(context.Context, Cluster) error { return nil },
		); err != nil {
			return err
		}
	}

	return nil
}

func (manager *Manager) runRestorableFault(
	ctx context.Context,
	target Cluster,
	other Cluster,
	fault Fault,
	observer Observer,
	apply func(context.Context, Cluster) error,
	waitActive func(context.Context, Cluster) error,
	restore func(context.Context, Cluster) error,
) error {
	if err := manager.prepareFault(ctx, target, other); err != nil {
		return err
	}
	if fault != FaultDeletePods {
		if err := manager.observe(ctx, observer, fault, PhaseBaseline, target, other); err != nil {
			return err
		}
	}

	activeErr := apply(ctx, target)
	if activeErr == nil {
		activeErr = waitActive(ctx, target)
	}
	if activeErr == nil {
		activeErr = manager.observe(ctx, observer, fault, PhaseActive, target, other)
	}
	diagnosticCtx, cancelDiagnostics := context.WithTimeout(
		context.WithoutCancel(ctx),
		manager.config.Timeout,
	)
	diagnosticErr := errors.Join(
		manager.inspect(diagnosticCtx, target, fault),
		manager.inspect(diagnosticCtx, other, fault),
	)
	cancelDiagnostics()

	restoreCtx, cancelRestore := context.WithTimeout(context.WithoutCancel(ctx), manager.config.Timeout)
	restoreErr := restore(restoreCtx, target)
	if restoreErr == nil {
		restoreErr = manager.waitDeploymentReady(restoreCtx, target, workload.FakeInternalDeployment)
	}
	if restoreErr == nil {
		restoreErr = manager.waitEndpointsReady(restoreCtx, target)
	}
	if restoreErr == nil {
		restoreErr = manager.observe(restoreCtx, observer, fault, PhaseRecovered, target, other)
	}
	cancelRestore()

	return errors.Join(activeErr, diagnosticErr, restoreErr)
}

func (manager *Manager) observe(
	ctx context.Context,
	observer Observer,
	fault Fault,
	phase Phase,
	target Cluster,
	other Cluster,
) error {
	observation := Observation{
		Fault: fault, Phase: phase, FaultedDC: target.DC, EligibleDC: other.DC,
		RequireRead: true, RequireMutation: true,
	}
	if err := observer.Observe(ctx, observation); err != nil {
		return fmt.Errorf("service chaos: observing %s/%s in %s: %w", fault, phase, target.DC, err)
	}
	_, _ = fmt.Fprintf(
		manager.config.Output,
		"observation: fault=%s phase=%s faulted_dc=%s eligible_dc=%s\n",
		fault,
		phase,
		target.DC,
		other.DC,
	)

	return nil
}

func (manager *Manager) prepareFault(ctx context.Context, target Cluster, other Cluster) error {
	if err := manager.assertOwnedResources(ctx, target); err != nil {
		return err
	}
	if err := manager.assertOwnedResources(ctx, other); err != nil {
		return err
	}
	for _, cluster := range []Cluster{target, other} {
		if err := manager.waitDeploymentReady(ctx, cluster, workload.FakeInternalDeployment); err != nil {
			return err
		}
		if err := manager.waitDeploymentReady(ctx, cluster, workload.GatewayOutDeployment); err != nil {
			return err
		}
		if err := manager.waitEndpointsReady(ctx, cluster); err != nil {
			return err
		}
	}

	return nil
}

func (manager *Manager) preflight(ctx context.Context) error {
	seenUIDs := make(map[string]struct{}, len(manager.clusters))
	for _, cluster := range manager.clusters {
		output, err := manager.runKubectl(
			ctx,
			cluster,
			nil,
			"get",
			"namespace/kube-system",
			"--output=jsonpath={.metadata.uid}",
		)
		uid := strings.TrimSpace(string(output))
		if err != nil || !safeIdentity(uid) {
			return fmt.Errorf("service chaos: cannot verify cluster identity for %s", cluster.DC)
		}
		if _, found := seenUIDs[uid]; found {
			return errors.New("service chaos: two distinct internal clusters are required")
		}
		seenUIDs[uid] = struct{}{}
		if err := manager.assertOwnedResources(ctx, cluster); err != nil {
			return err
		}
	}

	return nil
}

func (manager *Manager) assertOwnedResources(ctx context.Context, cluster Cluster) error {
	deployment, err := manager.getObject(ctx, cluster, "deployment/"+workload.FakeInternalDeployment)
	if err != nil {
		return err
	}
	if err := manager.assertOwnedMetadata(deployment.Metadata, workload.FakeInternalDeployment, cluster); err != nil {
		return err
	}
	if deployment.Spec.Replicas != nil && *deployment.Spec.Replicas != desiredReplicas {
		return fmt.Errorf("service chaos: fake-internal replicas in %s are not restored", cluster.DC)
	}

	service, err := manager.getObject(ctx, cluster, "service/"+workload.FakeInternalService)
	if err != nil {
		return err
	}
	if err := manager.assertOwnedMetadata(service.Metadata, workload.FakeInternalService, cluster); err != nil {
		return err
	}
	selector, err := decodeSelector(service.Spec.Selector)
	if err != nil {
		return fmt.Errorf("service chaos: decoding fake-internal selector in %s", cluster.DC)
	}
	if !equalStringMap(selector, manager.expectedSelector()) {
		return fmt.Errorf("service chaos: fake-internal selector in %s is not restored", cluster.DC)
	}
	if len(service.Spec.Ports) != 1 || service.Spec.Ports[0].Name != "grpc" ||
		service.Spec.Ports[0].Port != 9443 || service.Spec.Ports[0].TargetPort != "grpc" ||
		service.Spec.Ports[0].Protocol != "TCP" {
		return fmt.Errorf("service chaos: fake-internal Service contract in %s is unexpected", cluster.DC)
	}

	return nil
}

func (manager *Manager) assertOwnedMetadata(metadata objectMetadata, name string, cluster Cluster) error {
	if metadata.Name != name || metadata.Namespace != workload.Namespace ||
		metadata.Labels[managedByLabel] != managedByValue ||
		metadata.Labels[taskLabel] != workloadTaskValue ||
		metadata.Labels[runIDLabel] != manager.config.RunID ||
		metadata.Labels[dcLabel] != cluster.DC ||
		metadata.Labels[zoneLabel] != internalZoneValue {
		return fmt.Errorf("service chaos: refusing foreign or unexpected resource %s in %s", name, cluster.DC)
	}

	return nil
}

func (manager *Manager) getObject(
	ctx context.Context,
	cluster Cluster,
	resource string,
) (kubernetesObject, error) {
	output, err := manager.runKubectl(
		ctx,
		cluster,
		nil,
		"get",
		resource,
		"--namespace="+workload.Namespace,
		"--output=json",
	)
	if err != nil {
		return kubernetesObject{}, fmt.Errorf("service chaos: reading %s in %s", resource, cluster.DC)
	}
	var object kubernetesObject
	if err := json.Unmarshal(output, &object); err != nil {
		return kubernetesObject{}, fmt.Errorf("service chaos: decoding %s in %s", resource, cluster.DC)
	}

	return object, nil
}

func (manager *Manager) pods(ctx context.Context, cluster Cluster, app string) ([]kubernetesObject, error) {
	output, err := manager.runKubectl(
		ctx,
		cluster,
		nil,
		"get",
		"pods",
		"--namespace="+workload.Namespace,
		"--selector="+nameLabel+"="+app+","+runIDLabel+"="+manager.config.RunID,
		"--output=json",
	)
	if err != nil {
		return nil, fmt.Errorf("service chaos: reading %s Pods in %s", app, cluster.DC)
	}
	var list kubernetesList
	if err := json.Unmarshal(output, &list); err != nil {
		return nil, fmt.Errorf("service chaos: decoding %s Pods in %s", app, cluster.DC)
	}
	sort.Slice(list.Items, func(left int, right int) bool {
		return list.Items[left].Metadata.Name < list.Items[right].Metadata.Name
	})

	return list.Items, nil
}

func (manager *Manager) gatewayOutPodIdentities(ctx context.Context) (map[string][]string, error) {
	identities := make(map[string][]string, len(manager.clusters))
	for _, cluster := range manager.clusters {
		pods, err := manager.pods(ctx, cluster, gatewayOutValue)
		if err != nil {
			return nil, err
		}
		if len(pods) != desiredReplicas {
			return nil, fmt.Errorf("service chaos: expected %d gateway-out Pods in %s", desiredReplicas, cluster.DC)
		}
		for _, pod := range pods {
			if err := manager.assertOwnedMetadata(pod.Metadata, pod.Metadata.Name, cluster); err != nil {
				return nil, err
			}
			if !safeIdentity(pod.Metadata.UID) {
				return nil, fmt.Errorf("service chaos: invalid gateway-out Pod identity in %s", cluster.DC)
			}
			identities[cluster.DC] = append(identities[cluster.DC], pod.Metadata.Name+"/"+pod.Metadata.UID)
		}
		sort.Strings(identities[cluster.DC])
	}

	return identities, nil
}

func (manager *Manager) otherCluster(dc string) Cluster {
	for _, cluster := range manager.clusters {
		if cluster.DC != dc {
			return cluster
		}
	}
	return Cluster{}
}

func equalIdentitySets(left map[string][]string, right map[string][]string) bool {
	if len(left) != len(right) {
		return false
	}
	for dc, identities := range left {
		if !slices.Equal(identities, right[dc]) {
			return false
		}
	}
	return true
}

func equalStringMap(left map[string]string, right map[string]string) bool {
	return len(left) == len(right) && mapsEqual(left, right)
}

func mapsEqual(left map[string]string, right map[string]string) bool {
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func validateConfig(config *Config) error {
	if err := pki.ValidateRunID(config.RunID); err != nil {
		return fmt.Errorf("service chaos: validating run id: %w", err)
	}
	if !strings.HasPrefix(config.RunID, mm33RunPrefix) || len(config.RunID) == len(mm33RunPrefix) {
		return errors.New("service chaos: run id must use the unique mm33-* prefix")
	}
	if config.Timeout <= 0 || config.Timeout > 30*time.Minute {
		return errors.New("service chaos: timeout is outside bounds")
	}
	if len(config.Clusters) != 2 {
		return errors.New("service chaos: exactly two internal clusters are required")
	}
	seenDC := make(map[string]struct{}, len(config.Clusters))
	seenTarget := make(map[string]struct{}, len(config.Clusters))
	for index := range config.Clusters {
		cluster := &config.Clusters[index]
		if cluster.DC != "dc-a" && cluster.DC != "dc-b" {
			return errors.New("service chaos: cluster dc must be dc-a or dc-b")
		}
		if _, found := seenDC[cluster.DC]; found {
			return errors.New("service chaos: internal cluster dc is duplicated")
		}
		seenDC[cluster.DC] = struct{}{}
		if err := validateContext(cluster.Context); err != nil {
			return err
		}
		absolute, err := filepath.Abs(cluster.Kubeconfig)
		if err != nil {
			return errors.New("service chaos: resolving kubeconfig path")
		}
		info, err := os.Stat(absolute)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("service chaos: kubeconfig for %s is not a regular file", cluster.DC)
		}
		cluster.Kubeconfig = absolute
		target := absolute + "\x00" + cluster.Context
		if _, found := seenTarget[target]; found {
			return errors.New("service chaos: kubernetes target is duplicated")
		}
		seenTarget[target] = struct{}{}
	}

	return nil
}

func validateContext(value string) error {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return errors.New("service chaos: kubernetes context is outside bounds")
	}
	for _, character := range []byte(value) {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '.' && character != '_' && character != '-' {
			return errors.New("service chaos: kubernetes context contains an unsafe character")
		}
	}

	return nil
}

func safeIdentity(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range []byte(value) {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '.' && character != '_' && character != '-' && character != '/' {
			return false
		}
	}
	return true
}

type kubernetesObject struct {
	APIVersion string         `json:"apiVersion,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	Metadata   objectMetadata `json:"metadata"`
	Spec       objectSpec     `json:"spec,omitempty"`
	Status     objectStatus   `json:"status,omitempty"`
}

type kubernetesList struct {
	Items []kubernetesObject `json:"items"`
}

type objectMetadata struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	UID       string            `json:"uid,omitempty"`
	Labels    map[string]string `json:"labels"`
}

type objectSpec struct {
	Replicas *int32          `json:"replicas,omitempty"`
	Selector json.RawMessage `json:"selector,omitempty"`
	Ports    []servicePort   `json:"ports,omitempty"`
}

type objectStatus struct {
	AvailableReplicas int32 `json:"availableReplicas,omitempty"`
}

type servicePort struct {
	Name       string `json:"name"`
	Port       int32  `json:"port"`
	TargetPort string `json:"targetPort"`
	Protocol   string `json:"protocol"`
}

func decodeSelector(content json.RawMessage) (map[string]string, error) {
	var selector map[string]string
	if err := json.Unmarshal(content, &selector); err != nil {
		return nil, err
	}

	return selector, nil
}
