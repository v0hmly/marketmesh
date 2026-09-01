// Package workload owns the bounded Kubernetes lifecycle for MM-29 resources.
package workload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/pki"
	"github.com/v0hmly/marketmesh/e2e/tunnel/manifests"
)

const (
	Namespace                   = "marketmesh-e2e-tunnel"
	GatewayInDeployment         = "mm29-gateway-in"
	GatewayOutDeployment        = "mm29-gateway-out"
	FakeInternalDeployment      = "mm29-fake-internal"
	GatewayInService            = "mm29-gateway-in"
	FakeInternalService         = "mm29-fake-internal"
	OwnerConfigMap              = "mm29-run-owner"
	GatewayInTLSSecret          = "mm29-gateway-in-tls"
	GatewayOutTunnelTLSSecret   = "mm29-gateway-out-tunnel-tls"
	GatewayOutInternalTLSSecret = "mm29-gateway-out-internal-tls"
	FakeInternalTLSSecret       = "mm29-fake-internal-tls"
	maxCommandOutputBytes       = 1024 * 1024
)

// Cluster is one explicit topology boundary. No ambient kubeconfig is used.
type Cluster struct {
	DC              string
	Zone            string
	Kubeconfig      string
	Context         string
	GatewayInTarget string
}

// Config contains every mutable input for one workload run.
type Config struct {
	RunID             string
	Version           string
	GatewayInImage    string
	GatewayOutImage   string
	FakeInternalImage string
	Timeout           time.Duration
	Clusters          []Cluster
	Output            io.Writer
}

// Manager applies and removes only the exact resources owned by one run.
type Manager struct {
	config   Config
	kubectl  commandRunner
	clusters []Cluster
}

type commandRunner interface {
	Run(ctx context.Context, stdin []byte, arguments ...string) ([]byte, error)
}

type kubectlRunner struct {
	path string
}

// New validates all explicit topology inputs and locates kubectl.
func New(config Config) (*Manager, error) {
	if config.Timeout <= 0 || config.Timeout > 30*time.Minute {
		return nil, errors.New("workload: timeout is outside bounds")
	}
	if config.Output == nil {
		config.Output = io.Discard
	}
	if len(config.Clusters) != 4 {
		return nil, errors.New("workload: exactly four clusters are required")
	}

	seen := make(map[string]struct{}, len(config.Clusters))
	seenTargets := make(map[string]struct{}, len(config.Clusters))
	clusters := make([]Cluster, len(config.Clusters))
	for index, cluster := range config.Clusters {
		if cluster.DC != "dc-a" && cluster.DC != "dc-b" {
			return nil, errors.New("workload: cluster dc must be dc-a or dc-b")
		}
		if cluster.Zone != "dmz" && cluster.Zone != "internal" {
			return nil, errors.New("workload: cluster zone must be dmz or internal")
		}
		key := cluster.DC + "/" + cluster.Zone
		if _, found := seen[key]; found {
			return nil, fmt.Errorf("workload: cluster %s is duplicated", key)
		}
		seen[key] = struct{}{}
		if err := validateContext(cluster.Context); err != nil {
			return nil, err
		}
		absolute, err := filepath.Abs(cluster.Kubeconfig)
		if err != nil {
			return nil, errors.New("workload: resolving kubeconfig path")
		}
		info, err := os.Stat(absolute)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("workload: kubeconfig for %s is not a regular file", key)
		}
		cluster.Kubeconfig = absolute
		targetKey := absolute + "\x00" + cluster.Context
		if _, found := seenTargets[targetKey]; found {
			return nil, errors.New("workload: kubernetes target is duplicated")
		}
		seenTargets[targetKey] = struct{}{}
		clusters[index] = cluster
	}

	kubectlPath, err := exec.LookPath("kubectl")
	if err != nil {
		return nil, errors.New("workload: kubectl is required")
	}

	return newManager(config, clusters, kubectlRunner{path: kubectlPath})
}

func newManager(config Config, clusters []Cluster, runner commandRunner) (*Manager, error) {
	if runner == nil {
		return nil, errors.New("workload: command runner is required")
	}
	if err := pki.ValidateRunID(config.RunID); err != nil {
		return nil, fmt.Errorf("workload: validating run id: %w", err)
	}
	if strings.TrimSpace(config.Version) == "" || len(config.Version) > 128 {
		return nil, errors.New("workload: version is outside bounds")
	}

	return &Manager{config: config, clusters: clusters, kubectl: runner}, nil
}

// Deploy creates fresh in-memory PKI, applies the four workload sets, and
// waits for readiness. Failure captures diagnostics before automatic cleanup.
func (manager *Manager) Deploy(ctx context.Context) (resultErr error) {
	if ctx == nil {
		return errors.New("workload: deploy context must not be nil")
	}
	ctx, cancel := context.WithTimeout(ctx, manager.config.Timeout)
	defer cancel()
	if err := manager.validateClusterIdentities(ctx); err != nil {
		return err
	}

	bundles := make(map[string]pki.Bundle, 2)
	defer clearBundles(bundles)
	for _, dc := range []string{"dc-a", "dc-b"} {
		bundle, err := pki.New(manager.config.RunID, dc, time.Now())
		if err != nil {
			return fmt.Errorf("workload: creating pki for %s: %w", dc, err)
		}
		bundles[dc] = bundle
	}
	rendered := make(map[string][]byte, len(manager.clusters))
	for _, cluster := range manager.clusters {
		content, err := manager.renderManifest(cluster, bundles[cluster.DC])
		if err != nil {
			return err
		}
		rendered[cluster.DC+"/"+cluster.Zone] = content
	}
	if err := manager.preflightClusters(ctx); err != nil {
		return err
	}

	applied := make([]Cluster, 0, len(manager.clusters))
	defer func() {
		if resultErr == nil || len(applied) == 0 {
			return
		}
		diagnosticCtx, cancelDiagnostics := context.WithTimeout(
			context.WithoutCancel(ctx),
			manager.config.Timeout,
		)
		diagnosticErr := manager.inspectClusters(diagnosticCtx, applied)
		cancelDiagnostics()
		cleanupCtx, cancelCleanup := context.WithTimeout(
			context.WithoutCancel(ctx),
			manager.config.Timeout,
		)
		cleanupErr := manager.cleanupClusters(cleanupCtx, applied)
		cancelCleanup()
		resultErr = errors.Join(resultErr, diagnosticErr, cleanupErr)
	}()

	for _, cluster := range manager.clusters {
		if err := manager.applyNamespace(ctx, cluster); err != nil {
			return err
		}
		if err := manager.applyOwner(ctx, cluster); err != nil {
			return err
		}
		applied = append(applied, cluster)
		if err := manager.applySecrets(ctx, cluster, bundles[cluster.DC]); err != nil {
			return err
		}
		if err := manager.apply(
			ctx,
			cluster,
			rendered[cluster.DC+"/"+cluster.Zone],
		); err != nil {
			return err
		}
	}

	for _, deployment := range []string{FakeInternalDeployment, GatewayOutDeployment} {
		for _, cluster := range manager.clustersForZone("internal") {
			if err := manager.waitDeployment(ctx, cluster, deployment); err != nil {
				return err
			}
		}
	}
	for _, cluster := range manager.clustersForZone("dmz") {
		if err := manager.waitDeployment(ctx, cluster, GatewayInDeployment); err != nil {
			return err
		}
	}

	_, _ = fmt.Fprintf(
		manager.config.Output,
		"workloads MM-29 готовы: run_id=%s\n",
		manager.config.RunID,
	)

	return nil
}

// Inspect captures bounded metadata, events, and safe application logs.
func (manager *Manager) Inspect(ctx context.Context) error {
	if ctx == nil {
		return errors.New("workload: inspect context must not be nil")
	}
	ctx, cancel := context.WithTimeout(ctx, manager.config.Timeout)
	defer cancel()

	return manager.inspectClusters(ctx, manager.clusters)
}

// Undeploy captures diagnostics, then deletes only exact run-owned resources.
func (manager *Manager) Undeploy(ctx context.Context) error {
	if ctx == nil {
		return errors.New("workload: undeploy context must not be nil")
	}
	diagnosticCtx, cancelDiagnostics := context.WithTimeout(ctx, manager.config.Timeout)
	diagnosticErr := manager.inspectClusters(diagnosticCtx, manager.clusters)
	cancelDiagnostics()
	cleanupCtx, cancelCleanup := context.WithTimeout(
		context.WithoutCancel(ctx),
		manager.config.Timeout,
	)
	cleanupErr := manager.cleanupClusters(cleanupCtx, manager.clusters)
	cancelCleanup()

	return errors.Join(diagnosticErr, cleanupErr)
}

func (manager *Manager) applyNamespace(ctx context.Context, cluster Cluster) error {
	object := objectManifest{
		APIVersion: "v1",
		Kind:       "Namespace",
		Metadata: objectMetadata{
			Name:   Namespace,
			Labels: baseLabels("", "", ""),
		},
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return fmt.Errorf("workload: encoding namespace: %w", err)
	}

	return manager.apply(ctx, cluster, encoded)
}

func (manager *Manager) assertNamespace(ctx context.Context, cluster Cluster) (bool, error) {
	output, err := manager.runKubectl(
		ctx,
		cluster,
		nil,
		"get",
		"namespace",
		Namespace,
		"--ignore-not-found=true",
		"--output=json",
	)
	if err != nil {
		return false, fmt.Errorf("workload: reading namespace owner in %s/%s", cluster.DC, cluster.Zone)
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return false, nil
	}
	var object objectManifest
	if err := json.Unmarshal(output, &object); err != nil {
		return false, fmt.Errorf("workload: decoding namespace owner in %s/%s", cluster.DC, cluster.Zone)
	}
	if object.Metadata.Name != Namespace ||
		object.Metadata.Labels["app.kubernetes.io/managed-by"] != "marketmesh-e2e-tunnel" ||
		object.Metadata.Labels["marketmesh.io/task"] != "MM-29" {
		return false, fmt.Errorf("workload: refusing to use foreign namespace in %s/%s", cluster.DC, cluster.Zone)
	}

	return true, nil
}

func (manager *Manager) preflightClusters(ctx context.Context) error {
	for _, cluster := range manager.clusters {
		namespaceExists, err := manager.assertNamespace(ctx, cluster)
		if err != nil {
			return err
		}
		if !namespaceExists {
			continue
		}
		for _, resource := range ownedResources(cluster.Zone) {
			if err := manager.assertResourceOwner(ctx, cluster, resource); err != nil {
				return err
			}
		}
	}

	return nil
}

func (manager *Manager) assertResourceOwner(
	ctx context.Context,
	cluster Cluster,
	resource string,
) error {
	output, err := manager.runKubectl(
		ctx,
		cluster,
		nil,
		"get",
		resource,
		"--namespace="+Namespace,
		"--ignore-not-found=true",
		"--output=json",
	)
	if err != nil {
		return fmt.Errorf("workload: reading resource owner for %s", resource)
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return nil
	}
	var object objectManifest
	if err := json.Unmarshal(output, &object); err != nil {
		return fmt.Errorf("workload: decoding resource owner for %s", resource)
	}
	parts := strings.SplitN(resource, "/", 2)
	if len(parts) != 2 || object.Metadata.Name != parts[1] ||
		object.Metadata.Namespace != Namespace ||
		object.Metadata.Labels["app.kubernetes.io/managed-by"] != "marketmesh-e2e-tunnel" ||
		object.Metadata.Labels["marketmesh.io/task"] != "MM-29" ||
		object.Metadata.Labels["marketmesh.io/run-id"] != manager.config.RunID {
		return fmt.Errorf("workload: refusing to replace foreign resource %s", resource)
	}

	return nil
}

func (manager *Manager) validateClusterIdentities(ctx context.Context) error {
	seen := make(map[string]struct{}, len(manager.clusters))
	for _, cluster := range manager.clusters {
		output, err := manager.runKubectl(
			ctx,
			cluster,
			nil,
			"get",
			"namespace",
			"kube-system",
			"--output=jsonpath={.metadata.uid}",
		)
		identity := strings.TrimSpace(string(output))
		if err != nil || identity == "" || len(identity) > 128 {
			return fmt.Errorf(
				"workload: cannot verify cluster identity for %s/%s",
				cluster.DC,
				cluster.Zone,
			)
		}
		for _, character := range []byte(identity) {
			if (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') && character != '-' {
				return fmt.Errorf(
					"workload: cluster identity for %s/%s is invalid",
					cluster.DC,
					cluster.Zone,
				)
			}
		}
		if _, found := seen[identity]; found {
			return errors.New("workload: four distinct kubernetes clusters are required")
		}
		seen[identity] = struct{}{}
	}

	return nil
}

func (manager *Manager) applyOwner(ctx context.Context, cluster Cluster) error {
	object := objectManifest{
		APIVersion: "v1",
		Kind:       "ConfigMap",
		Metadata: objectMetadata{
			Name:      OwnerConfigMap,
			Namespace: Namespace,
			Labels:    baseLabels(manager.config.RunID, cluster.DC, cluster.Zone),
		},
		Data: map[string]string{"run_id": manager.config.RunID},
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return fmt.Errorf("workload: encoding owner: %w", err)
	}

	return manager.apply(ctx, cluster, encoded)
}

func (manager *Manager) applySecrets(ctx context.Context, cluster Cluster, bundle pki.Bundle) error {
	secrets := []secretManifest{}
	if cluster.Zone == "dmz" {
		secrets = append(secrets, newTLSSecret(
			GatewayInTLSSecret,
			manager.config.RunID,
			cluster,
			bundle.GatewayIn,
			bundle.TunnelCAPEM,
		))
	} else {
		secrets = append(
			secrets,
			newTLSSecret(
				GatewayOutTunnelTLSSecret,
				manager.config.RunID,
				cluster,
				bundle.GatewayOutTunnel,
				bundle.TunnelCAPEM,
			),
			newTLSSecret(
				GatewayOutInternalTLSSecret,
				manager.config.RunID,
				cluster,
				bundle.GatewayOutInternal,
				bundle.InternalCAPEM,
			),
			newTLSSecret(
				FakeInternalTLSSecret,
				manager.config.RunID,
				cluster,
				bundle.FakeInternal,
				bundle.InternalCAPEM,
			),
		)
	}
	for _, secret := range secrets {
		encoded, err := json.Marshal(secret)
		if err != nil {
			return fmt.Errorf("workload: encoding tls secret: %w", err)
		}
		applyErr := manager.apply(ctx, cluster, encoded)
		clear(encoded)
		if applyErr != nil {
			return applyErr
		}
	}

	return nil
}

func (manager *Manager) renderManifest(cluster Cluster, bundle pki.Bundle) ([]byte, error) {
	parameters := manifests.Parameters{
		RunID: manager.config.RunID, DC: cluster.DC, Version: manager.config.Version,
		GatewayInImage:    manager.config.GatewayInImage,
		GatewayOutImage:   manager.config.GatewayOutImage,
		FakeInternalImage: manager.config.FakeInternalImage,
		GatewayInTarget:   cluster.GatewayInTarget,
		GatewayInURI:      bundle.GatewayInURI,
		GatewayOutURI:     bundle.GatewayOutURI,
		FakeInternalURI:   bundle.FakeInternalURI,
	}
	var (
		content []byte
		err     error
	)
	if cluster.Zone == "dmz" {
		content, err = manifests.RenderDMZ(parameters)
	} else {
		content, err = manifests.RenderInternal(parameters)
	}
	if err != nil {
		return nil, fmt.Errorf("workload: rendering %s/%s: %w", cluster.DC, cluster.Zone, err)
	}

	return content, nil
}

func (manager *Manager) apply(ctx context.Context, cluster Cluster, content []byte) error {
	_, err := manager.runKubectl(
		ctx,
		cluster,
		content,
		"apply",
		"--server-side=true",
		"--field-manager=marketmesh-e2e-tunnel",
		"-f",
		"-",
	)
	if err != nil {
		return fmt.Errorf("workload: applying resources to %s/%s", cluster.DC, cluster.Zone)
	}

	return nil
}

func (manager *Manager) waitDeployment(
	ctx context.Context,
	cluster Cluster,
	deployment string,
) error {
	_, err := manager.runKubectl(
		ctx,
		cluster,
		nil,
		"rollout",
		"status",
		"deployment/"+deployment,
		"--namespace="+Namespace,
		"--timeout="+manager.config.Timeout.String(),
	)
	if err != nil {
		return fmt.Errorf(
			"workload: waiting for deployment %s in %s/%s",
			deployment,
			cluster.DC,
			cluster.Zone,
		)
	}

	return nil
}

func (manager *Manager) inspectClusters(ctx context.Context, clusters []Cluster) error {
	var resultErr error
	for _, cluster := range clusters {
		_, _ = fmt.Fprintf(
			manager.config.Output,
			"diagnostics: %s/%s\n",
			cluster.DC,
			cluster.Zone,
		)
		selector := "marketmesh.io/run-id=" + manager.config.RunID
		commands := [][]string{
			{
				"get", "deployment,pod,service,pdb,networkpolicy,configmap",
				"--namespace=" + Namespace, "--selector=" + selector, "--output=wide",
			},
			{"get", "events", "--namespace=" + Namespace, "--sort-by=.lastTimestamp"},
			{
				"logs", "--namespace=" + Namespace, "--selector=" + selector,
				"--all-containers=true", "--prefix=true", "--tail=200",
			},
		}
		for _, command := range commands {
			output, err := manager.runKubectl(ctx, cluster, nil, command...)
			if len(output) > 0 {
				_, _ = manager.config.Output.Write(output)
				if output[len(output)-1] != '\n' {
					_, _ = io.WriteString(manager.config.Output, "\n")
				}
			}
			if err != nil {
				resultErr = errors.Join(
					resultErr,
					fmt.Errorf("workload: diagnostics for %s/%s", cluster.DC, cluster.Zone),
				)
			}
		}
	}

	return resultErr
}

func (manager *Manager) cleanupClusters(ctx context.Context, clusters []Cluster) error {
	var resultErr error
	for _, cluster := range clusters {
		for _, resource := range ownedResources(cluster.Zone) {
			parts := strings.SplitN(resource, "/", 2)
			if len(parts) != 2 {
				resultErr = errors.Join(resultErr, errors.New("workload: invalid owned resource"))
				continue
			}
			label, err := manager.getRunLabel(ctx, cluster, parts[0], parts[1])
			if err != nil {
				resultErr = errors.Join(resultErr, err)
				continue
			}
			if label == "" {
				continue
			}
			if label != manager.config.RunID {
				resultErr = errors.Join(
					resultErr,
					fmt.Errorf("workload: refusing to delete foreign resource %s", resource),
				)
				continue
			}
			if _, err := manager.runKubectl(
				ctx,
				cluster,
				nil,
				"delete",
				resource,
				"--namespace="+Namespace,
				"--wait=true",
				"--timeout="+manager.config.Timeout.String(),
			); err != nil {
				resultErr = errors.Join(
					resultErr,
					fmt.Errorf("workload: deleting %s in %s/%s", resource, cluster.DC, cluster.Zone),
				)
			}
		}
	}

	return resultErr
}

func (manager *Manager) getRunLabel(
	ctx context.Context,
	cluster Cluster,
	kind string,
	name string,
) (string, error) {
	output, err := manager.runKubectl(
		ctx,
		cluster,
		nil,
		"get",
		kind,
		name,
		"--namespace="+Namespace,
		"--ignore-not-found=true",
		"--output=jsonpath={.metadata.labels.marketmesh\\.io/run-id}",
	)
	if err != nil {
		return "", fmt.Errorf("workload: reading owner for %s/%s", kind, name)
	}

	return strings.TrimSpace(string(output)), nil
}

func (manager *Manager) runKubectl(
	ctx context.Context,
	cluster Cluster,
	stdin []byte,
	arguments ...string,
) ([]byte, error) {
	base := []string{"--kubeconfig=" + cluster.Kubeconfig, "--context=" + cluster.Context}
	return manager.kubectl.Run(ctx, stdin, append(base, arguments...)...)
}

func (manager *Manager) clustersForZone(zone string) []Cluster {
	clusters := make([]Cluster, 0, 2)
	for _, cluster := range manager.clusters {
		if cluster.Zone == zone {
			clusters = append(clusters, cluster)
		}
	}

	return clusters
}

func (runner kubectlRunner) Run(
	ctx context.Context,
	stdin []byte,
	arguments ...string,
) ([]byte, error) {
	command := exec.CommandContext(ctx, runner.path, arguments...)
	command.Stdin = bytes.NewReader(stdin)
	output := &limitBuffer{remaining: maxCommandOutputBytes}
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	if output.truncated {
		return output.Bytes(), errors.Join(err, errors.New("workload: kubectl output exceeded bounds"))
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

type objectManifest struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   objectMetadata    `json:"metadata"`
	Data       map[string]string `json:"data,omitempty"`
}

type objectMetadata struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace,omitempty"`
	Labels    map[string]string `json:"labels"`
}

type secretManifest struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   objectMetadata    `json:"metadata"`
	Type       string            `json:"type"`
	Data       map[string][]byte `json:"data"`
}

func newTLSSecret(
	name string,
	runID string,
	cluster Cluster,
	certificate pki.Certificate,
	caPEM []byte,
) secretManifest {
	return secretManifest{
		APIVersion: "v1",
		Kind:       "Secret",
		Metadata: objectMetadata{
			Name:      name,
			Namespace: Namespace,
			Labels:    baseLabels(runID, cluster.DC, cluster.Zone),
		},
		Type: "Opaque",
		Data: map[string][]byte{
			"ca.crt":  caPEM,
			"tls.crt": certificate.CertificatePEM,
			"tls.key": certificate.PrivateKeyPEM,
		},
	}
}

func baseLabels(runID string, dc string, zone string) map[string]string {
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "marketmesh-e2e-tunnel",
		"marketmesh.io/task":           "MM-29",
	}
	if runID != "" {
		labels["marketmesh.io/run-id"] = runID
	}
	if dc != "" {
		labels["marketmesh.io/dc"] = dc
	}
	if zone != "" {
		labels["marketmesh.io/zone"] = zone
	}

	return labels
}

func ownedResources(zone string) []string {
	if zone == "dmz" {
		return []string{
			"deployment/" + GatewayInDeployment,
			"service/" + GatewayInService,
			"poddisruptionbudget/" + GatewayInDeployment,
			"networkpolicy/" + GatewayInDeployment,
			"secret/" + GatewayInTLSSecret,
			"configmap/" + OwnerConfigMap,
		}
	}

	return []string{
		"deployment/" + GatewayOutDeployment,
		"deployment/" + FakeInternalDeployment,
		"service/" + FakeInternalService,
		"poddisruptionbudget/" + GatewayOutDeployment,
		"poddisruptionbudget/" + FakeInternalDeployment,
		"networkpolicy/" + GatewayOutDeployment,
		"networkpolicy/" + FakeInternalDeployment,
		"secret/" + GatewayOutTunnelTLSSecret,
		"secret/" + GatewayOutInternalTLSSecret,
		"secret/" + FakeInternalTLSSecret,
		"configmap/" + OwnerConfigMap,
	}
}

func validateContext(value string) error {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return errors.New("workload: kubernetes context is outside bounds")
	}
	for _, character := range []byte(value) {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '.' && character != '_' && character != '-' {
			return errors.New("workload: kubernetes context contains an unsafe character")
		}
	}

	return nil
}

func clearBundles(bundles map[string]pki.Bundle) {
	for dc, bundle := range bundles {
		clear(bundle.GatewayIn.PrivateKeyPEM)
		clear(bundle.GatewayOutTunnel.PrivateKeyPEM)
		clear(bundle.GatewayOutInternal.PrivateKeyPEM)
		clear(bundle.FakeInternal.PrivateKeyPEM)
		delete(bundles, dc)
	}
}
