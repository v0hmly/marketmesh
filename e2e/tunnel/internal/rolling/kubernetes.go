package rolling

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	maximumCommandOutputBytes = 1024 * 1024
	maximumPatchBytes         = 64 * 1024
	defaultPollInterval       = 250 * time.Millisecond
)

// KubernetesConfig contains explicit and bounded Kubernetes inputs.
type KubernetesConfig struct {
	RunID        string
	Clusters     []Cluster
	PollInterval time.Duration
	Output       io.Writer
}

type kubernetes struct {
	config   KubernetesConfig
	clusters map[string]Cluster
	kubectl  commandRunner
}

type commandRunner interface {
	Run(ctx context.Context, stdin []byte, arguments ...string) ([]byte, error)
}

type kubectlRunner struct {
	path string
}

// NewKubernetes validates explicit kubeconfigs and locates kubectl.
func NewKubernetes(config KubernetesConfig) (Kubernetes, error) {
	if err := validateRunID(config.RunID); err != nil {
		return nil, err
	}
	if len(config.Clusters) != 4 {
		return nil, errors.New("rolling: exactly four clusters are required")
	}
	if config.PollInterval == 0 {
		config.PollInterval = defaultPollInterval
	}
	if config.PollInterval < 10*time.Millisecond || config.PollInterval > 5*time.Second {
		return nil, errors.New("rolling: poll interval is outside bounds")
	}
	if config.Output == nil {
		config.Output = io.Discard
	}
	clusters := make([]Cluster, len(config.Clusters))
	seen := make(map[string]struct{}, len(config.Clusters))
	seenTargets := make(map[string]struct{}, len(config.Clusters))
	for index, cluster := range config.Clusters {
		if err := validateClusterHandoff(cluster); err != nil {
			return nil, err
		}
		key := cluster.DC + "/" + cluster.Zone
		if _, found := seen[key]; found {
			return nil, fmt.Errorf("rolling: cluster %s is duplicated", key)
		}
		seen[key] = struct{}{}
		if err := validateContext(cluster.Context); err != nil {
			return nil, err
		}
		absolute, err := regularAbsolutePath(cluster.Kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("rolling: kubeconfig for %s is not a regular file", key)
		}
		cluster.Kubeconfig = absolute
		targetKey := absolute + "\x00" + cluster.Context
		if _, found := seenTargets[targetKey]; found {
			return nil, errors.New("rolling: kubernetes target is duplicated")
		}
		seenTargets[targetKey] = struct{}{}
		clusters[index] = cluster
	}
	path, err := exec.LookPath("kubectl")
	if err != nil {
		return nil, errors.New("rolling: kubectl is required")
	}

	return newKubernetes(config, clusters, kubectlRunner{path: path})
}

func regularAbsolutePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("rolling: path is not a regular file")
	}

	return absolute, nil
}

func newKubernetes(
	config KubernetesConfig,
	clusters []Cluster,
	runner commandRunner,
) (*kubernetes, error) {
	if isNilDependency(runner) {
		return nil, errors.New("rolling: command runner is required")
	}
	clusterByKey := make(map[string]Cluster, len(clusters))
	for _, cluster := range clusters {
		clusterByKey[cluster.DC+"/"+cluster.Zone] = cluster
	}
	if len(clusterByKey) != 4 {
		return nil, errors.New("rolling: four distinct cluster zones are required")
	}

	return &kubernetes{config: config, clusters: clusterByKey, kubectl: runner}, nil
}

func (kube *kubernetes) Prepare(ctx context.Context) error {
	if ctx == nil {
		return errors.New("rolling: prepare context must not be nil")
	}
	identities := make(map[string]struct{}, len(kube.clusters))
	for _, key := range []string{"dc-a/dmz", "dc-a/internal", "dc-b/dmz", "dc-b/internal"} {
		cluster, found := kube.clusters[key]
		if !found {
			return fmt.Errorf("rolling: cluster %s is missing", key)
		}
		identity, err := kube.clusterIdentity(ctx, cluster)
		if err != nil {
			return err
		}
		if _, found := identities[identity]; found {
			return errors.New("rolling: four distinct kubernetes clusters are required")
		}
		identities[identity] = struct{}{}
		if err := kube.verifyTopologyOwnership(ctx, cluster); err != nil {
			return err
		}
		if err := kube.verifyNamespaceAndOwner(ctx, cluster); err != nil {
			return err
		}
	}

	return nil
}

func (kube *kubernetes) clusterIdentity(ctx context.Context, cluster Cluster) (string, error) {
	output, err := kube.run(
		ctx,
		cluster,
		nil,
		"get",
		"namespace",
		"kube-system",
		"--output=jsonpath={.metadata.uid}",
	)
	identity := strings.TrimSpace(string(output))
	if err != nil || !isSafeUID(identity) {
		return "", fmt.Errorf("rolling: cannot verify cluster identity for %s/%s", cluster.DC, cluster.Zone)
	}

	return identity, nil
}

func validateClusterHandoff(cluster Cluster) error {
	if !topologyInstancePattern.MatchString(cluster.TopologyInstance) {
		return errors.New("rolling: cluster topology instance is outside bounds")
	}
	expectedLogicalName := cluster.DC + "-" + cluster.Zone
	expectedResourceName := cluster.TopologyInstance + "-" + expectedLogicalName
	if (cluster.DC != "dc-a" && cluster.DC != "dc-b") ||
		(cluster.Zone != "dmz" && cluster.Zone != "internal") ||
		cluster.LogicalName != expectedLogicalName || cluster.ResourceName != expectedResourceName ||
		cluster.Context != "kind-"+expectedResourceName ||
		net.ParseIP(cluster.ControlPlaneAddress).To4() == nil {
		return errors.New("rolling: cluster does not match the MM-28 topology handoff")
	}

	return nil
}

func (kube *kubernetes) verifyTopologyOwnership(ctx context.Context, cluster Cluster) error {
	output, err := kube.run(ctx, cluster, nil, "get", "namespace", topologyNamespace, "--output=json")
	if err != nil {
		return fmt.Errorf("rolling: reading topology namespace in %s/%s", cluster.DC, cluster.Zone)
	}
	var namespace metadataObject
	if err := json.Unmarshal(output, &namespace); err != nil {
		return fmt.Errorf("rolling: decoding topology namespace in %s/%s", cluster.DC, cluster.Zone)
	}
	labels := namespace.Metadata.Labels
	if namespace.Metadata.Name != topologyNamespace ||
		labels["marketmesh.dev/cluster"] != cluster.LogicalName ||
		labels["marketmesh.dev/dc"] != cluster.DC ||
		labels["marketmesh.dev/zone"] != cluster.Zone ||
		labels["marketmesh.dev/owner-task"] != topologyTaskKey ||
		labels["marketmesh.dev/topology-instance"] != cluster.TopologyInstance {
		return fmt.Errorf("rolling: refusing foreign topology in %s/%s", cluster.DC, cluster.Zone)
	}

	return nil
}

func (kube *kubernetes) Preflight(ctx context.Context, target Target) (Snapshot, error) {
	if ctx == nil {
		return Snapshot{}, errors.New("rolling: preflight context must not be nil")
	}
	cluster, found := kube.clusterFor(target)
	if !found {
		return Snapshot{}, errors.New("rolling: target cluster is missing")
	}
	clusterUID, err := kube.clusterIdentity(ctx, cluster)
	if err != nil {
		return Snapshot{}, err
	}
	if err := kube.verifyTopologyOwnership(ctx, cluster); err != nil {
		return Snapshot{}, err
	}
	if err := kube.verifyNamespaceAndOwner(ctx, cluster); err != nil {
		return Snapshot{}, err
	}
	deployment, err := kube.readDeployment(ctx, target)
	if err != nil {
		return Snapshot{}, err
	}
	if err := kube.validateDeployment(target, deployment); err != nil {
		return Snapshot{}, err
	}
	if err := validateReadyState(deployment, *deployment.Spec.Replicas); err != nil {
		return Snapshot{}, fmt.Errorf("rolling: deployment is not at full capacity: %w", err)
	}
	revision, err := deploymentRevision(deployment)
	if err != nil {
		return Snapshot{}, err
	}
	container, _ := findContainer(deployment, target.Container)

	return Snapshot{
		ClusterUID:     clusterUID,
		UID:            deployment.Metadata.UID,
		Revision:       revision,
		Generation:     deployment.Metadata.Generation,
		Desired:        *deployment.Spec.Replicas,
		Image:          container.Image,
		ConfigRevision: deployment.Spec.Template.Metadata.Annotations[configRevisionAnnotation],
	}, nil
}

func (kube *kubernetes) Update(
	ctx context.Context,
	target Target,
	change Change,
	snapshot Snapshot,
) error {
	if ctx == nil {
		return errors.New("rolling: update context must not be nil")
	}
	if err := validateChange(change); err != nil {
		return err
	}
	if err := kube.revalidateSnapshot(ctx, target, snapshot); err != nil {
		return err
	}
	annotations := map[string]string{rolloutRevisionAnnotation: change.Revision}
	container := map[string]any{"name": target.Container}
	switch change.Kind {
	case ChangeImage:
		if change.Image == snapshot.Image {
			return errors.New("rolling: image transition must change the digest")
		}
		if imageRepository(change.Image) != imageRepository(snapshot.Image) {
			return errors.New("rolling: image transition must keep the repository")
		}
		container["image"] = change.Image
	case ChangeConfig:
		if change.ConfigRevision == snapshot.ConfigRevision {
			return errors.New("rolling: config transition must change the revision")
		}
		annotations[configRevisionAnnotation] = change.ConfigRevision
	default:
		return errors.New("rolling: unknown change kind")
	}
	patch := podTemplatePatch(annotations, container)

	return kube.patch(ctx, target, patch)
}

func (kube *kubernetes) InjectReadinessFault(
	ctx context.Context,
	target Target,
	fault Fault,
	snapshot Snapshot,
) error {
	if ctx == nil {
		return errors.New("rolling: fault context must not be nil")
	}
	if err := validateRevision(fault.Revision); err != nil {
		return err
	}
	if err := kube.revalidateSnapshot(ctx, target, snapshot); err != nil {
		return err
	}
	environment := map[string]string{}
	switch target.Component {
	case ComponentGatewayIn:
		environment["EXPECTED_GATEWAY_OUT_URI"] = fmt.Sprintf(
			"spiffe://marketmesh.test/e2e/%s/%s/gateway-out-mm34-unready",
			kube.config.RunID,
			target.DC,
		)
	case ComponentGatewayOut:
		environment["EXPECTED_GATEWAY_IN_URI"] = fmt.Sprintf(
			"spiffe://marketmesh.test/e2e/%s/%s/gateway-in-mm34-unready",
			kube.config.RunID,
			target.DC,
		)
	case ComponentFakeInternal:
		environment["MAX_LEDGER_ENTRIES"] = "100001"
	default:
		return errors.New("rolling: target has no built-in readiness fault")
	}
	env := make([]map[string]string, 0, len(environment))
	for name, value := range environment {
		env = append(env, map[string]string{"name": name, "value": value})
	}
	patch := podTemplatePatch(
		map[string]string{
			configRevisionAnnotation:  fault.Revision,
			rolloutRevisionAnnotation: fault.Revision,
		},
		map[string]any{"name": target.Container, "env": env},
	)

	return kube.patch(ctx, target, patch)
}

func (kube *kubernetes) Wait(
	ctx context.Context,
	target Target,
	expectation Expectation,
) error {
	if ctx == nil {
		return errors.New("rolling: wait context must not be nil")
	}
	if expectation.UID == "" || expectation.Desired < minimumReplicas {
		return errors.New("rolling: invalid rollout expectation")
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return errors.Join(ErrReadinessNotReached, ctx.Err())
		case <-timer.C:
		}
		deployment, err := kube.readDeployment(ctx, target)
		if err != nil {
			return err
		}
		if err := kube.validateDeployment(target, deployment); err != nil {
			return err
		}
		if deployment.Metadata.UID != expectation.UID {
			return errors.New("rolling: deployment uid changed during rollout")
		}
		if *deployment.Spec.Replicas != expectation.Desired {
			return errors.New("rolling: desired replicas changed during rollout")
		}
		if err := validateCapacityInvariant(deployment, expectation.Desired); err != nil {
			return err
		}
		if progressDeadlineExceeded(deployment) {
			return ErrReadinessNotReached
		}
		container, _ := findContainer(deployment, target.Container)
		isExpectedTemplate := container.Image == expectation.Image &&
			deployment.Spec.Template.Metadata.Annotations[configRevisionAnnotation] ==
				expectation.ConfigRevision
		isObserved := deployment.Status.ObservedGeneration == deployment.Metadata.Generation
		isReady := deployment.Status.UpdatedReplicas == expectation.Desired &&
			deployment.Status.ReadyReplicas == expectation.Desired &&
			deployment.Status.AvailableReplicas == expectation.Desired &&
			deployment.Status.Replicas == expectation.Desired
		if isExpectedTemplate && isObserved && isReady {
			return nil
		}
		timer.Reset(kube.config.PollInterval)
	}
}

func (kube *kubernetes) Diagnostics(ctx context.Context, target Target) error {
	if ctx == nil {
		return errors.New("rolling: diagnostics context must not be nil")
	}
	if _, err := kube.readDeployment(ctx, target); err != nil {
		return err
	}
	cluster, _ := kube.clusterFor(target)
	selector := "marketmesh.io/run-id=" + kube.config.RunID +
		",app.kubernetes.io/name=" + string(target.Component)
	commands := [][]string{
		{
			"get", "deployment,replicaset,pod,endpointslice", "--namespace=" + Namespace,
			"--selector=" + selector, "--output=wide",
		},
		{
			"get", "events", "--namespace=" + Namespace,
			"--field-selector=involvedObject.name=" + target.Deployment,
			"--sort-by=.lastTimestamp",
		},
		{
			"logs", "--namespace=" + Namespace, "--selector=" + selector,
			"--all-containers=true", "--prefix=true", "--tail=200",
		},
	}
	var resultErr error
	_, _ = fmt.Fprintf(
		kube.config.Output,
		"diagnostics: %s/%s component=%s\n",
		target.DC,
		target.Zone,
		target.Component,
	)
	for _, command := range commands {
		output, err := kube.run(ctx, cluster, nil, command...)
		if len(output) > 0 {
			_, _ = kube.config.Output.Write(output)
			if output[len(output)-1] != '\n' {
				_, _ = io.WriteString(kube.config.Output, "\n")
			}
		}
		if err != nil {
			resultErr = errors.Join(resultErr, errors.New("rolling: collecting diagnostics"))
		}
	}

	return resultErr
}

func (kube *kubernetes) Rollback(
	ctx context.Context,
	target Target,
	revision string,
	snapshot Snapshot,
) error {
	if ctx == nil {
		return errors.New("rolling: rollback context must not be nil")
	}
	if snapshot.Revision <= 0 || snapshot.UID == "" {
		return errors.New("rolling: snapshot cannot identify an exact revision")
	}
	cluster, found := kube.clusterFor(target)
	if !found {
		return errors.New("rolling: target cluster is missing")
	}
	clusterUID, err := kube.clusterIdentity(ctx, cluster)
	if err != nil {
		return err
	}
	if clusterUID != snapshot.ClusterUID {
		return errors.New("rolling: refusing rollback after cluster identity changed")
	}
	if err := kube.verifyTopologyOwnership(ctx, cluster); err != nil {
		return err
	}
	if err := kube.verifyNamespaceAndOwner(ctx, cluster); err != nil {
		return err
	}
	deployment, err := kube.readDeployment(ctx, target)
	if err != nil {
		return err
	}
	if err := kube.validateDeployment(target, deployment); err != nil {
		return err
	}
	if deployment.Metadata.UID != snapshot.UID {
		return errors.New("rolling: refusing rollback after deployment uid changed")
	}
	container, _ := findContainer(deployment, target.Container)
	currentConfigRevision := deployment.Spec.Template.Metadata.Annotations[configRevisionAnnotation]
	if container.Image == snapshot.Image && currentConfigRevision == snapshot.ConfigRevision {
		return nil
	}
	if deployment.Spec.Template.Metadata.Annotations[rolloutRevisionAnnotation] != revision {
		return errors.New("rolling: refusing to roll back a foreign rollout")
	}
	_, err = kube.run(
		ctx,
		cluster,
		nil,
		"rollout",
		"undo",
		"deployment/"+target.Deployment,
		"--namespace="+Namespace,
		"--to-revision="+strconv.FormatInt(snapshot.Revision, 10),
	)
	if err != nil {
		return errors.New("rolling: restoring exact deployment revision")
	}

	return nil
}

func (kube *kubernetes) verifyNamespaceAndOwner(ctx context.Context, cluster Cluster) error {
	output, err := kube.run(ctx, cluster, nil, "get", "namespace", Namespace, "--output=json")
	if err != nil {
		return fmt.Errorf("rolling: reading namespace in %s/%s", cluster.DC, cluster.Zone)
	}
	var namespace metadataObject
	if err := json.Unmarshal(output, &namespace); err != nil {
		return fmt.Errorf("rolling: decoding namespace in %s/%s", cluster.DC, cluster.Zone)
	}
	if namespace.Metadata.Name != Namespace ||
		namespace.Metadata.Labels["app.kubernetes.io/managed-by"] != "marketmesh-e2e-tunnel" ||
		namespace.Metadata.Labels["marketmesh.io/task"] != "MM-29" {
		return fmt.Errorf("rolling: refusing foreign namespace in %s/%s", cluster.DC, cluster.Zone)
	}
	output, err = kube.run(
		ctx,
		cluster,
		nil,
		"get",
		"configmap",
		ownerConfigMap,
		"--namespace="+Namespace,
		"--output=json",
	)
	if err != nil {
		return fmt.Errorf("rolling: reading run owner in %s/%s", cluster.DC, cluster.Zone)
	}
	var owner metadataObject
	if err := json.Unmarshal(output, &owner); err != nil {
		return fmt.Errorf("rolling: decoding run owner in %s/%s", cluster.DC, cluster.Zone)
	}
	if owner.Metadata.Name != ownerConfigMap || owner.Metadata.Namespace != Namespace ||
		owner.Metadata.Labels["marketmesh.io/run-id"] != kube.config.RunID ||
		owner.Metadata.Labels["marketmesh.io/dc"] != cluster.DC ||
		owner.Metadata.Labels["marketmesh.io/zone"] != cluster.Zone ||
		owner.Data["run_id"] != kube.config.RunID {
		return fmt.Errorf("rolling: refusing foreign run owner in %s/%s", cluster.DC, cluster.Zone)
	}

	return nil
}

func (kube *kubernetes) revalidateSnapshot(
	ctx context.Context,
	target Target,
	snapshot Snapshot,
) error {
	current, err := kube.Preflight(ctx, target)
	if err != nil {
		return fmt.Errorf("rolling: revalidating target before mutation: %w", err)
	}
	if current != snapshot {
		return errors.New("rolling: deployment changed after preflight")
	}

	return nil
}

func (kube *kubernetes) readDeployment(
	ctx context.Context,
	target Target,
) (deploymentObject, error) {
	if err := validateTarget(target); err != nil {
		return deploymentObject{}, err
	}
	cluster, found := kube.clusterFor(target)
	if !found {
		return deploymentObject{}, errors.New("rolling: target cluster is missing")
	}
	output, err := kube.run(
		ctx,
		cluster,
		nil,
		"get",
		"deployment",
		target.Deployment,
		"--namespace="+Namespace,
		"--output=json",
	)
	if err != nil {
		return deploymentObject{}, fmt.Errorf("rolling: reading deployment %s", target.Deployment)
	}
	var deployment deploymentObject
	if err := json.Unmarshal(output, &deployment); err != nil {
		return deploymentObject{}, fmt.Errorf("rolling: decoding deployment %s", target.Deployment)
	}

	return deployment, nil
}

func (kube *kubernetes) validateDeployment(
	target Target,
	deployment deploymentObject,
) error {
	labels := deployment.Metadata.Labels
	isExpectedOwner := deployment.Metadata.Name == target.Deployment &&
		deployment.Metadata.Namespace == Namespace &&
		deployment.Metadata.UID != "" &&
		labels["app.kubernetes.io/managed-by"] == "marketmesh-e2e-tunnel" &&
		labels["marketmesh.io/task"] == "MM-29" &&
		labels["marketmesh.io/run-id"] == kube.config.RunID &&
		labels["marketmesh.io/dc"] == target.DC &&
		labels["marketmesh.io/zone"] == target.Zone
	if !isExpectedOwner {
		return errors.New("rolling: refusing foreign deployment")
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas < minimumReplicas {
		return errors.New("rolling: deployment has insufficient desired replicas")
	}
	if deployment.Spec.Strategy.Type != "RollingUpdate" ||
		!exactInt(deployment.Spec.Strategy.RollingUpdate.MaxUnavailable, 0) ||
		!exactInt(deployment.Spec.Strategy.RollingUpdate.MaxSurge, 1) {
		return errors.New("rolling: deployment must use RollingUpdate 0/1")
	}
	if deployment.Spec.ProgressDeadlineSeconds == nil ||
		*deployment.Spec.ProgressDeadlineSeconds <= 0 ||
		*deployment.Spec.ProgressDeadlineSeconds > 120 {
		return errors.New("rolling: progress deadline is outside bounds")
	}
	if deployment.Spec.Template.Spec.TerminationGracePeriodSeconds == nil ||
		*deployment.Spec.Template.Spec.TerminationGracePeriodSeconds < minimumTerminationGrace {
		return errors.New("rolling: termination grace period is insufficient")
	}
	container, found := findContainer(deployment, target.Container)
	if !found || !hasJSON(container.StartupProbe) || !hasJSON(container.ReadinessProbe) ||
		!hasJSON(container.Lifecycle.PreStop) {
		return errors.New("rolling: startup, readiness, and preStop are required")
	}
	if container.Image == "" || len(container.Image) > 512 || imageRepository(container.Image) == "" {
		return errors.New("rolling: current image reference is outside bounds")
	}
	shutdownTimeout, found := environmentValue(container.Env, "SHUTDOWN_TIMEOUT")
	if !found {
		return errors.New("rolling: shutdown timeout is required")
	}
	duration, err := time.ParseDuration(shutdownTimeout)
	if err != nil || duration <= 0 || duration > maximumShutdownTimeout ||
		time.Duration(*deployment.Spec.Template.Spec.TerminationGracePeriodSeconds)*time.Second <= duration {
		return errors.New("rolling: shutdown timeout is outside termination budget")
	}

	return nil
}

func validateReadyState(deployment deploymentObject, desired int32) error {
	if deployment.Status.ObservedGeneration != deployment.Metadata.Generation {
		return errors.New("generation is not observed")
	}
	if err := validateCapacityInvariant(deployment, desired); err != nil {
		return err
	}
	if deployment.Status.UpdatedReplicas != desired ||
		deployment.Status.ReadyReplicas != desired ||
		deployment.Status.AvailableReplicas != desired ||
		deployment.Status.Replicas != desired {
		return errors.New("replica status is not fully ready")
	}
	if progressDeadlineExceeded(deployment) {
		return errors.New("progress deadline was exceeded")
	}

	return nil
}

func validateCapacityInvariant(deployment deploymentObject, desired int32) error {
	if deployment.Status.UnavailableReplicas != 0 {
		return errors.New("rolling: unavailable replicas became non-zero")
	}
	if deployment.Status.ReadyReplicas < desired {
		return errors.New("rolling: ready replicas dropped below desired")
	}
	if deployment.Status.Replicas > desired+1 {
		return errors.New("rolling: total replicas exceeded maxSurge")
	}

	return nil
}

func progressDeadlineExceeded(deployment deploymentObject) bool {
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == "Progressing" && condition.Status == "False" &&
			condition.Reason == "ProgressDeadlineExceeded" {
			return true
		}
	}

	return false
}

func deploymentRevision(deployment deploymentObject) (int64, error) {
	value := deployment.Metadata.Annotations["deployment.kubernetes.io/revision"]
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil || revision <= 0 {
		return 0, errors.New("rolling: deployment revision is unavailable")
	}

	return revision, nil
}

func findContainer(deployment deploymentObject, name string) (containerObject, bool) {
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == name {
			return container, true
		}
	}

	return containerObject{}, false
}

func environmentValue(environment []environmentVariable, name string) (string, bool) {
	for _, variable := range environment {
		if variable.Name == name {
			return variable.Value, true
		}

	}
	return "", false
}

func exactInt(value json.RawMessage, expected int64) bool {
	var number int64
	if err := json.Unmarshal(value, &number); err == nil {
		return number == expected
	}
	var textValue string
	if err := json.Unmarshal(value, &textValue); err != nil {
		return false
	}
	parsed, err := strconv.ParseInt(textValue, 10, 64)

	return err == nil && parsed == expected
}

func imageRepository(reference string) string {
	if repository, _, found := strings.Cut(reference, "@"); found {
		return repository
	}
	lastSlash := strings.LastIndexByte(reference, '/')
	lastColon := strings.LastIndexByte(reference, ':')
	if lastColon > lastSlash {
		return reference[:lastColon]
	}

	return reference
}

func hasJSON(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) &&
		!bytes.Equal(trimmed, []byte("{}"))
}

func podTemplatePatch(annotations map[string]string, container map[string]any) map[string]any {
	return map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{"annotations": annotations},
				"spec":     map[string]any{"containers": []map[string]any{container}},
			},
		},
	}
}

func (kube *kubernetes) patch(
	ctx context.Context,
	target Target,
	patch map[string]any,
) (resultErr error) {
	encoded, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("rolling: encoding deployment patch: %w", err)
	}
	patchPath, cleanup, err := writePrivatePatchFile(encoded)
	if err != nil {
		return err
	}
	defer func() {
		if err := cleanup(); err != nil {
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("rolling: removing deployment patch: %w", err),
			)
		}
	}()
	cluster, _ := kube.clusterFor(target)
	_, err = kube.run(
		ctx,
		cluster,
		nil,
		"patch",
		"deployment",
		target.Deployment,
		"--namespace="+Namespace,
		"--type=strategic",
		"--patch-file="+patchPath,
	)
	if err != nil {
		return errors.New("rolling: patching deployment")
	}

	return nil
}

func writePrivatePatchFile(content []byte) (string, func() error, error) {
	if len(content) == 0 || len(content) > maximumPatchBytes {
		return "", nil, errors.New("rolling: deployment patch is outside bounds")
	}
	file, err := os.CreateTemp("", "marketmesh-mm34-patch-*.json")
	if err != nil {
		return "", nil, errors.New("rolling: creating deployment patch")
	}
	path := file.Name()
	closed := false
	cleanup := func() error {
		var closeErr error
		if !closed {
			closeErr = file.Close()
			closed = true
		}
		removeErr := os.Remove(path)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}

		return errors.Join(closeErr, removeErr)
	}
	fail := func(cause error) (string, func() error, error) {
		return "", nil, errors.Join(cause, cleanup())
	}
	if err := file.Chmod(0o600); err != nil {
		return fail(errors.New("rolling: securing deployment patch"))
	}
	written, err := file.Write(content)
	if err != nil {
		return fail(errors.New("rolling: writing deployment patch"))
	}
	if written != len(content) {
		return fail(io.ErrShortWrite)
	}
	if err := file.Close(); err != nil {
		closed = true
		return fail(errors.New("rolling: closing deployment patch"))
	}
	closed = true

	return path, cleanup, nil
}

func (kube *kubernetes) clusterFor(target Target) (Cluster, bool) {
	cluster, found := kube.clusters[target.DC+"/"+target.Zone]
	return cluster, found
}

func (kube *kubernetes) run(
	ctx context.Context,
	cluster Cluster,
	stdin []byte,
	arguments ...string,
) ([]byte, error) {
	base := []string{"--kubeconfig=" + cluster.Kubeconfig, "--context=" + cluster.Context}
	return kube.kubectl.Run(ctx, stdin, append(base, arguments...)...)
}

func (runner kubectlRunner) Run(
	ctx context.Context,
	stdin []byte,
	arguments ...string,
) ([]byte, error) {
	// #nosec G204 -- path comes from exec.LookPath and every caller supplies a
	// finite command plus validated kubeconfig, context, namespace and target.
	command := exec.CommandContext(ctx, runner.path, arguments...)
	command.Stdin = bytes.NewReader(stdin)
	output := &limitBuffer{remaining: maximumCommandOutputBytes}
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	if output.truncated {
		return output.Bytes(), errors.Join(err, errors.New("rolling: kubectl output exceeded bounds"))
	}

	return output.Bytes(), err
}

type limitBuffer struct {
	bytes.Buffer
	remaining int
	truncated bool
}

func (buffer *limitBuffer) Write(content []byte) (int, error) {
	original := len(content)
	if len(content) > buffer.remaining {
		content = content[:max(buffer.remaining, 0)]
		buffer.truncated = true
	}
	buffer.remaining -= len(content)
	_, _ = buffer.Buffer.Write(content)

	return original, nil
}

func validateContext(value string) error {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return errors.New("rolling: kubernetes context is outside bounds")
	}
	for _, character := range []byte(value) {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '.' && character != '_' && character != '-' {
			return errors.New("rolling: kubernetes context contains an unsafe character")
		}
	}

	return nil
}

func isSafeUID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range []byte(value) {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '-' {
			return false
		}
	}

	return true
}

type metadataObject struct {
	Metadata objectMetadata    `json:"metadata"`
	Data     map[string]string `json:"data"`
}

type deploymentObject struct {
	Metadata objectMetadata   `json:"metadata"`
	Spec     deploymentSpec   `json:"spec"`
	Status   deploymentStatus `json:"status"`
}

type objectMetadata struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	UID         string            `json:"uid"`
	Generation  int64             `json:"generation"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

type deploymentSpec struct {
	Replicas                *int32             `json:"replicas"`
	ProgressDeadlineSeconds *int32             `json:"progressDeadlineSeconds"`
	Strategy                deploymentStrategy `json:"strategy"`
	Template                podTemplate        `json:"template"`
}

type deploymentStrategy struct {
	Type          string                `json:"type"`
	RollingUpdate rollingUpdateStrategy `json:"rollingUpdate"`
}

type rollingUpdateStrategy struct {
	MaxUnavailable json.RawMessage `json:"maxUnavailable"`
	MaxSurge       json.RawMessage `json:"maxSurge"`
}

type podTemplate struct {
	Metadata objectMetadata `json:"metadata"`
	Spec     podSpec        `json:"spec"`
}

type podSpec struct {
	TerminationGracePeriodSeconds *int64            `json:"terminationGracePeriodSeconds"`
	Containers                    []containerObject `json:"containers"`
}

type containerObject struct {
	Name           string                `json:"name"`
	Image          string                `json:"image"`
	Env            []environmentVariable `json:"env"`
	StartupProbe   json.RawMessage       `json:"startupProbe"`
	ReadinessProbe json.RawMessage       `json:"readinessProbe"`
	Lifecycle      lifecycleObject       `json:"lifecycle"`
}

type environmentVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type lifecycleObject struct {
	PreStop json.RawMessage `json:"preStop"`
}

type deploymentStatus struct {
	ObservedGeneration  int64                 `json:"observedGeneration"`
	Replicas            int32                 `json:"replicas"`
	UpdatedReplicas     int32                 `json:"updatedReplicas"`
	ReadyReplicas       int32                 `json:"readyReplicas"`
	AvailableReplicas   int32                 `json:"availableReplicas"`
	UnavailableReplicas int32                 `json:"unavailableReplicas"`
	Conditions          []deploymentCondition `json:"conditions"`
}

type deploymentCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}
