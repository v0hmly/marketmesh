package topology

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	ownerLabelKey      = "com.marketmesh.task"
	instanceLabelKey   = "com.marketmesh.topology"
	clusterLabelKey    = "io.x-k8s.kind.cluster"
	probeContainerPath = "/usr/local/bin/mm28-tcpprobe"
)

// Topology manages one strictly named, disposable four-cluster environment.
type Topology struct {
	config Config
	runner Runner
	logger *slog.Logger
	now    func() time.Time
}

type dockerNetwork struct {
	ID         string            `json:"Id"`
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver"`
	Scope      string            `json:"Scope"`
	Labels     map[string]string `json:"Labels"`
	IPAM       dockerIPAM        `json:"IPAM"`
	Containers map[string]struct {
		Name        string `json:"Name"`
		EndpointID  string `json:"EndpointID"`
		MacAddress  string `json:"MacAddress"`
		IPv4Address string `json:"IPv4Address"`
	} `json:"Containers"`
}

type dockerIPAM struct {
	Config []struct {
		Subnet string `json:"Subnet"`
	} `json:"Config"`
}

type dockerContainer struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Image  string `json:"Image"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		Paused     bool   `json:"Paused"`
		Restarting bool   `json:"Restarting"`
		Dead       bool   `json:"Dead"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
	} `json:"State"`
	NetworkSettings struct {
		SandboxID  string `json:"SandboxID"`
		SandboxKey string `json:"SandboxKey"`
		Networks   map[string]struct {
			NetworkID   string `json:"NetworkID"`
			EndpointID  string `json:"EndpointID"`
			Gateway     string `json:"Gateway"`
			IPAddress   string `json:"IPAddress"`
			IPPrefixLen int    `json:"IPPrefixLen"`
			MacAddress  string `json:"MacAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

type kubernetesObject struct {
	Metadata struct {
		Name   string            `json:"name"`
		UID    string            `json:"uid"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
}

type kubernetesObjectList struct {
	Items []kubernetesObject `json:"items"`
}

// New constructs a topology manager from validated configuration.
func New(config Config, runner Runner, logger *slog.Logger) *Topology {
	return &Topology{
		config: config,
		runner: runner,
		logger: logger,
		now:    time.Now,
	}
}

// Up creates or validates every owned resource and writes the public inventory.
func (t *Topology) Up(ctx context.Context) error {
	if err := t.up(ctx); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Minute)
		defer cancel()
		diagnosticsErr := t.Inspect(cleanupCtx)
		cleanupErr := t.cleanup(cleanupCtx)
		return errors.Join(err, diagnosticsErr, cleanupErr)
	}
	return nil
}

func (t *Topology) up(ctx context.Context) error {
	if err := t.ensureToolchain(); err != nil {
		return err
	}
	if err := t.dockerReady(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(t.config.StateDir, "kubeconfigs"), 0o750); err != nil {
		return fmt.Errorf("creating kubeconfig directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(t.config.StateDir, "configs"), 0o750); err != nil {
		return fmt.Errorf("creating kind config directory: %w", err)
	}

	for _, cluster := range t.config.Clusters() {
		if err := t.ensureNetwork(ctx, cluster); err != nil {
			return err
		}
		if err := t.ensureCluster(ctx, cluster); err != nil {
			return err
		}
	}

	for _, dc := range []string{"dc-a", "dc-b"} {
		if err := t.connectAndIsolateZones(ctx, dc); err != nil {
			return err
		}
	}
	for _, cluster := range t.config.Clusters() {
		if err := t.ensureIdentity(ctx, cluster); err != nil {
			return err
		}
	}
	if _, err := t.writeInventory(ctx); err != nil {
		return err
	}

	t.logger.InfoContext(ctx, "disposable topology is up", "instance", t.config.Instance)
	return nil
}

// Ready validates cluster health, identity, firewall state, and zone isolation.
func (t *Topology) Ready(ctx context.Context) error {
	if err := t.ensureToolchain(); err != nil {
		return err
	}
	if err := t.dockerReady(ctx); err != nil {
		return err
	}

	for _, cluster := range t.config.Clusters() {
		if err := t.validateNetwork(ctx, cluster); err != nil {
			return err
		}
		if _, err := t.validateContainer(ctx, cluster); err != nil {
			return err
		}
		if err := t.waitForCluster(ctx, cluster); err != nil {
			return err
		}
		if err := t.validateIdentity(ctx, cluster); err != nil {
			return err
		}
	}

	for _, dc := range []string{"dc-a", "dc-b"} {
		if err := t.validateFirewall(ctx, dc); err != nil {
			return err
		}
		if err := t.checkZoneIsolation(ctx, dc); err != nil {
			return err
		}
	}

	t.logger.InfoContext(ctx, "topology is ready and isolated", "instance", t.config.Instance)
	return nil
}

// Down captures diagnostics and removes only resources proven to belong to the instance.
func (t *Topology) Down(ctx context.Context) error {
	diagnosticsErr := t.Inspect(ctx)
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Minute)
	defer cancel()
	cleanupErr := t.cleanup(cleanupCtx)
	return errors.Join(diagnosticsErr, cleanupErr)
}

// Verify runs two complete up-ready-down cycles to prove repeatability and cleanup.
func (t *Topology) Verify(ctx context.Context) error {
	for run := 1; run <= 2; run++ {
		t.logger.InfoContext(ctx, "starting topology verification run", "run", run)
		if err := t.Up(ctx); err != nil {
			return fmt.Errorf("topology verification run %d up: %w", run, err)
		}
		if err := t.Ready(ctx); err != nil {
			cleanupErr := t.Down(context.WithoutCancel(ctx))
			return errors.Join(fmt.Errorf("topology verification run %d ready: %w", run, err), cleanupErr)
		}
		snapshot, err := t.ResolveTargets(ctx, TargetResolveRequest{
			ConsumerTask:  "MM-38",
			ConsumerRunID: fmt.Sprintf("mm38-verify-%d", run),
		})
		if err != nil {
			cleanupErr := t.Down(context.WithoutCancel(ctx))
			return errors.Join(fmt.Errorf("topology verification run %d resolve targets: %w", run, err), cleanupErr)
		}
		if _, err := t.ValidateTargets(ctx, snapshot, TargetValidateRequest{
			ExpectedState: ExpectedStateRunning,
		}); err != nil {
			cleanupErr := t.Down(context.WithoutCancel(ctx))
			return errors.Join(fmt.Errorf("topology verification run %d validate targets: %w", run, err), cleanupErr)
		}
		if err := t.Down(ctx); err != nil {
			return fmt.Errorf("topology verification run %d down: %w", run, err)
		}
	}
	return nil
}

// Versions returns the pinned and locally installed topology tool versions.
func (t *Topology) Versions(ctx context.Context) (map[string]string, error) {
	versions := map[string]string{
		"kind":       KindVersion,
		"kubernetes": KubernetesVersion,
		"node_image": NodeImage,
		"probe_port": strconv.Itoa(AllowedProbePort),
	}
	if err := t.ensureToolchain(); err != nil {
		versions["installed"] = "false"
		return versions, nil
	}

	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	result, err := t.runner.Run(commandCtx, Command{Program: t.config.KindPath, Args: []string{"version"}})
	if err != nil {
		return nil, err
	}
	versions["installed"] = "true"
	versions["kind_actual"] = strings.TrimSpace(result.Stdout)
	return versions, nil
}

func (t *Topology) ensureToolchain() error {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	kindAsset, ok := kindAssets[platform]
	if !ok {
		return fmt.Errorf("topology: unsupported platform %s", platform)
	}
	kubectlAsset, ok := kubectlAssets[platform]
	if !ok {
		return fmt.Errorf("topology: unsupported platform %s", platform)
	}

	checks := []struct {
		path string
		hash string
		name string
	}{
		{path: t.config.KindPath, hash: kindAsset.sha256, name: "kind"},
		{path: t.config.KubectlPath, hash: kubectlAsset.sha256, name: "kubectl"},
	}
	for _, check := range checks {
		matches, err := fileMatchesSHA256(check.path, check.hash)
		if err != nil {
			return fmt.Errorf("checking %s: %w", check.name, err)
		}
		if !matches {
			return fmt.Errorf("topology: pinned %s is missing or has an invalid checksum; run bootstrap", check.name)
		}
	}
	if info, err := os.Stat(t.config.ProbePath); err != nil || !info.Mode().IsRegular() {
		return errors.New("topology: tcp probe is missing; run bootstrap")
	}
	return nil
}

func (t *Topology) dockerReady(ctx context.Context) error {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	result, err := t.runner.Run(commandCtx, t.dockerCommand("info", "--format", "{{.Architecture}}"))
	if err != nil {
		return fmt.Errorf("checking docker context %q: %w", t.config.DockerContext, err)
	}
	engineArchitecture := normalizeDockerArchitecture(strings.TrimSpace(result.Stdout))
	if engineArchitecture != runtime.GOARCH {
		return fmt.Errorf(
			"topology: docker architecture %q does not match host architecture %q",
			engineArchitecture,
			runtime.GOARCH,
		)
	}
	return nil
}

func normalizeDockerArchitecture(value string) string {
	switch value {
	case "aarch64":
		return "arm64"
	case "x86_64":
		return "amd64"
	default:
		return value
	}
}

func (t *Topology) ensureNetwork(ctx context.Context, cluster Cluster) error {
	exists, err := t.networkExists(ctx, cluster.NetworkName)
	if err != nil {
		return err
	}
	if exists {
		return t.validateNetwork(ctx, cluster)
	}

	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	_, err = t.runner.Run(commandCtx, t.dockerCommand(
		"network",
		"create",
		"--driver",
		"bridge",
		"--subnet",
		cluster.DockerSubnet,
		"--label",
		ownerLabelKey+"="+TaskKey,
		"--label",
		instanceLabelKey+"="+t.config.Instance,
		cluster.NetworkName,
	))
	if err != nil {
		return fmt.Errorf("creating network %s: %w", cluster.NetworkName, err)
	}
	return t.validateNetwork(ctx, cluster)
}

func (t *Topology) networkExists(ctx context.Context, name string) (bool, error) {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	result, err := t.runner.Run(commandCtx, t.dockerCommand(
		"network",
		"ls",
		"--filter",
		"name=^"+name+"$",
		"--format",
		"{{.Name}}",
	))
	if err != nil {
		return false, fmt.Errorf("listing docker networks: %w", err)
	}
	names := strings.Fields(result.Stdout)
	if len(names) == 0 {
		return false, nil
	}
	if len(names) != 1 || names[0] != name {
		return false, fmt.Errorf("topology: unexpected docker network match for %s", name)
	}
	return true, nil
}

func (t *Topology) validateNetwork(ctx context.Context, cluster Cluster) error {
	network, err := t.inspectNetwork(ctx, cluster.NetworkName)
	if err != nil {
		return err
	}
	if network.Name != cluster.NetworkName {
		return fmt.Errorf("topology: network name mismatch for %s", cluster.NetworkName)
	}
	if network.Labels[ownerLabelKey] != TaskKey || network.Labels[instanceLabelKey] != t.config.Instance {
		return fmt.Errorf("topology: refusing unowned network %s", cluster.NetworkName)
	}
	if len(network.IPAM.Config) != 1 || network.IPAM.Config[0].Subnet != cluster.DockerSubnet {
		return fmt.Errorf("topology: network %s has unexpected subnet", cluster.NetworkName)
	}
	return nil
}

func (t *Topology) inspectNetwork(ctx context.Context, name string) (dockerNetwork, error) {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	result, err := t.runner.Run(commandCtx, t.dockerCommand("network", "inspect", name))
	if err != nil {
		return dockerNetwork{}, fmt.Errorf("inspecting network %s: %w", name, err)
	}
	var networks []dockerNetwork
	if err := json.Unmarshal([]byte(result.Stdout), &networks); err != nil || len(networks) != 1 {
		return dockerNetwork{}, fmt.Errorf("topology: invalid docker network inspection for %s", name)
	}
	if networks[0].Labels == nil {
		networks[0].Labels = map[string]string{}
	}
	if networks[0].Containers == nil {
		networks[0].Containers = map[string]struct {
			Name        string `json:"Name"`
			EndpointID  string `json:"EndpointID"`
			MacAddress  string `json:"MacAddress"`
			IPv4Address string `json:"IPv4Address"`
		}{}
	}
	return networks[0], nil
}

func (t *Topology) ensureCluster(ctx context.Context, cluster Cluster) error {
	clusters, err := t.kindClusters(ctx)
	if err != nil {
		return err
	}
	if slices.Contains(clusters, cluster.Name) {
		if _, err := t.validateContainer(ctx, cluster); err != nil {
			return err
		}
		return t.refreshKubeconfig(ctx, cluster)
	}
	if slices.Contains(clusters, cluster.LogicalName) {
		return fmt.Errorf("topology: refusing to reuse non-prefixed cluster %s", cluster.LogicalName)
	}

	configPath := filepath.Join(t.config.StateDir, "configs", cluster.LogicalName+".yaml")
	if err := writePrivateFile(configPath, []byte(kindConfig(cluster))); err != nil {
		return fmt.Errorf("writing kind config for %s: %w", cluster.Name, err)
	}
	if err := os.Remove(cluster.Kubeconfig); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing stale kubeconfig for %s: %w", cluster.Name, err)
	}

	commandCtx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	_, err = t.runner.Run(commandCtx, Command{
		Program: t.config.KindPath,
		Args: []string{
			"create",
			"cluster",
			"--name",
			cluster.Name,
			"--image",
			NodeImage,
			"--config",
			configPath,
			"--kubeconfig",
			cluster.Kubeconfig,
			"--wait",
			"240s",
			"--retain",
		},
		Env: t.config.kindEnvironment(cluster.NetworkName),
	})
	if err != nil {
		return fmt.Errorf("creating cluster %s: %w", cluster.Name, err)
	}
	if err := os.Chmod(cluster.Kubeconfig, 0o600); err != nil {
		return fmt.Errorf("setting kubeconfig permissions for %s: %w", cluster.Name, err)
	}
	if _, err = t.validateContainer(ctx, cluster); err != nil {
		return err
	}
	return t.refreshKubeconfig(ctx, cluster)
}

func (t *Topology) refreshKubeconfig(ctx context.Context, cluster Cluster) error {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	result, err := t.runner.Run(commandCtx, Command{
		Program: t.config.KindPath,
		Args:    []string{"get", "kubeconfig", "--name", cluster.Name},
		Env:     t.config.kindEnvironment(""),
	})
	if err != nil {
		return fmt.Errorf("getting kubeconfig for %s: %w", cluster.Name, err)
	}
	if strings.TrimSpace(result.Stdout) == "" {
		return fmt.Errorf("topology: kind returned an empty kubeconfig for %s", cluster.Name)
	}
	if err := writePrivateFile(cluster.Kubeconfig, []byte(result.Stdout)); err != nil {
		return fmt.Errorf("writing kubeconfig for %s: %w", cluster.Name, err)
	}
	return nil
}

func (t *Topology) kindClusters(ctx context.Context) ([]string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	result, err := t.runner.Run(commandCtx, Command{
		Program: t.config.KindPath,
		Args:    []string{"get", "clusters"},
		Env:     t.config.kindEnvironment(""),
	})
	if err != nil {
		return nil, fmt.Errorf("listing kind clusters: %w", err)
	}
	clusters := strings.Fields(result.Stdout)
	slices.Sort(clusters)
	return clusters, nil
}

func (t *Topology) validateContainer(ctx context.Context, cluster Cluster) (dockerContainer, error) {
	container, err := t.inspectContainer(ctx, cluster.NodeName)
	if err != nil {
		return dockerContainer{}, err
	}
	if strings.TrimPrefix(container.Name, "/") != cluster.NodeName {
		return dockerContainer{}, fmt.Errorf("topology: container name mismatch for %s", cluster.NodeName)
	}
	if container.Config.Labels[clusterLabelKey] != cluster.Name {
		return dockerContainer{}, fmt.Errorf("topology: refusing unowned container %s", cluster.NodeName)
	}
	if container.Config.Image != NodeImage {
		return dockerContainer{}, fmt.Errorf("topology: container %s uses an unexpected node image", cluster.NodeName)
	}
	if _, ok := container.NetworkSettings.Networks[cluster.NetworkName]; !ok {
		return dockerContainer{}, fmt.Errorf("topology: container %s is not attached to %s", cluster.NodeName, cluster.NetworkName)
	}
	return container, nil
}

func (t *Topology) inspectContainer(ctx context.Context, name string) (dockerContainer, error) {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	result, err := t.runner.Run(commandCtx, t.dockerCommand("container", "inspect", name))
	if err != nil {
		return dockerContainer{}, fmt.Errorf("inspecting container %s: %w", name, err)
	}
	var containers []dockerContainer
	if err := json.Unmarshal([]byte(result.Stdout), &containers); err != nil || len(containers) != 1 {
		return dockerContainer{}, fmt.Errorf("topology: invalid docker container inspection for %s", name)
	}
	if containers[0].Config.Labels == nil {
		containers[0].Config.Labels = map[string]string{}
	}
	return containers[0], nil
}

func (t *Topology) ensureIdentity(ctx context.Context, cluster Cluster) error {
	if err := t.waitForCluster(ctx, cluster); err != nil {
		return err
	}
	labels := []string{
		"marketmesh.dev/cluster=" + cluster.LogicalName,
		"marketmesh.dev/dc=" + cluster.DC,
		"marketmesh.dev/owner-task=" + TaskKey,
		"marketmesh.dev/topology-instance=" + t.config.Instance,
		"marketmesh.dev/zone=" + cluster.Zone,
	}
	args := []string{"label", "nodes", "--all"}
	args = append(args, labels...)
	args = append(args, "--overwrite")
	if _, err := t.runKubectl(ctx, readyTimeout, cluster, args...); err != nil {
		return fmt.Errorf("labelling nodes in %s: %w", cluster.Name, err)
	}

	if _, err := t.runKubectl(ctx, commandTimeout, cluster, "get", "namespace", Namespace, "-o", "name"); err != nil {
		if _, createErr := t.runKubectl(ctx, commandTimeout, cluster, "create", "namespace", Namespace); createErr != nil {
			return fmt.Errorf("creating namespace identity in %s: %w", cluster.Name, createErr)
		}
	}
	args = []string{"label", "namespace", Namespace}
	args = append(args, labels...)
	args = append(args, "--overwrite")
	if _, err := t.runKubectl(ctx, commandTimeout, cluster, args...); err != nil {
		return fmt.Errorf("labelling namespace identity in %s: %w", cluster.Name, err)
	}
	return nil
}

func (t *Topology) waitForCluster(ctx context.Context, cluster Cluster) error {
	_, err := t.runKubectl(
		ctx,
		readyTimeout,
		cluster,
		"wait",
		"--for=condition=Ready",
		"nodes",
		"--all",
		"--timeout=90s",
	)
	if err != nil {
		return fmt.Errorf("waiting for cluster %s: %w", cluster.Name, err)
	}
	return nil
}

func (t *Topology) validateIdentity(ctx context.Context, cluster Cluster) error {
	currentContext, err := t.runKubectl(ctx, commandTimeout, cluster, "config", "current-context")
	if err != nil {
		return fmt.Errorf("reading kube context for %s: %w", cluster.Name, err)
	}
	if strings.TrimSpace(currentContext.Stdout) != cluster.KubeContext {
		return fmt.Errorf("topology: kube context mismatch for %s", cluster.Name)
	}

	expectedLabels := []struct {
		key   string
		value string
	}{
		{key: "marketmesh.dev/cluster", value: cluster.LogicalName},
		{key: "marketmesh.dev/dc", value: cluster.DC},
		{key: "marketmesh.dev/owner-task", value: TaskKey},
		{key: "marketmesh.dev/topology-instance", value: t.config.Instance},
		{key: "marketmesh.dev/zone", value: cluster.Zone},
	}
	nodesResult, err := t.runKubectl(ctx, commandTimeout, cluster, "get", "nodes", "-o", "json")
	if err != nil {
		return fmt.Errorf("reading nodes in %s: %w", cluster.Name, err)
	}
	var nodes kubernetesObjectList
	if err := json.Unmarshal([]byte(nodesResult.Stdout), &nodes); err != nil || len(nodes.Items) != 1 {
		return fmt.Errorf("topology: invalid node identity document in %s", cluster.Name)
	}
	if err := validateKubernetesIdentity(nodes.Items[0], cluster.NodeName, expectedLabels); err != nil {
		return fmt.Errorf("validating node identity in %s: %w", cluster.Name, err)
	}

	namespaceResult, err := t.runKubectl(
		ctx,
		commandTimeout,
		cluster,
		"get",
		"namespace",
		Namespace,
		"-o",
		"json",
	)
	if err != nil {
		return fmt.Errorf("reading namespace identity in %s: %w", cluster.Name, err)
	}
	var namespace kubernetesObject
	if err := json.Unmarshal([]byte(namespaceResult.Stdout), &namespace); err != nil {
		return fmt.Errorf("topology: invalid namespace identity document in %s", cluster.Name)
	}
	if err := validateKubernetesIdentity(namespace, Namespace, expectedLabels); err != nil {
		return fmt.Errorf("validating namespace identity in %s: %w", cluster.Name, err)
	}
	return nil
}

func validateKubernetesIdentity(
	object kubernetesObject,
	expectedName string,
	expectedLabels []struct {
		key   string
		value string
	},
) error {
	if object.Metadata.Name != expectedName {
		return fmt.Errorf("topology: resource name %q does not match %q", object.Metadata.Name, expectedName)
	}
	for _, label := range expectedLabels {
		if object.Metadata.Labels[label.key] != label.value {
			return fmt.Errorf("topology: label %s does not match expected identity", label.key)
		}
	}
	return nil
}

func (t *Topology) connectAndIsolateZones(ctx context.Context, dc string) error {
	dmz, err := t.config.Cluster(dc, "dmz")
	if err != nil {
		return err
	}
	internal, err := t.config.Cluster(dc, "internal")
	if err != nil {
		return err
	}
	if _, err := t.validateContainer(ctx, dmz); err != nil {
		return err
	}
	internalContainer, err := t.validateContainer(ctx, internal)
	if err != nil {
		return err
	}
	if _, isConnected := internalContainer.NetworkSettings.Networks[dmz.NetworkName]; !isConnected {
		commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
		_, connectErr := t.runner.Run(commandCtx, t.dockerCommand(
			"network",
			"connect",
			dmz.NetworkName,
			internal.NodeName,
		))
		cancel()
		if connectErr != nil {
			return fmt.Errorf("connecting %s to %s: %w", internal.NodeName, dmz.NetworkName, connectErr)
		}
	}

	internalContainer, err = t.inspectContainer(ctx, internal.NodeName)
	if err != nil {
		return err
	}
	internalAttachment, ok := internalContainer.NetworkSettings.Networks[internal.NetworkName]
	if !ok || net.ParseIP(internalAttachment.IPAddress).To4() == nil || net.ParseIP(internalAttachment.Gateway).To4() == nil {
		return fmt.Errorf("topology: missing internal attachment for %s", internal.NodeName)
	}
	internalInterface, err := t.interfaceForIP(ctx, internal.NodeName, internalAttachment.IPAddress)
	if err != nil {
		return err
	}
	if err := t.configureDefaultRoute(
		ctx,
		internal.NodeName,
		internalAttachment.Gateway,
		internalInterface,
	); err != nil {
		return err
	}
	dmzAttachment, ok := internalContainer.NetworkSettings.Networks[dmz.NetworkName]
	if !ok || net.ParseIP(dmzAttachment.IPAddress).To4() == nil {
		return fmt.Errorf("topology: missing dmz attachment for %s", internal.NodeName)
	}
	interfaceName, err := t.interfaceForIP(ctx, internal.NodeName, dmzAttachment.IPAddress)
	if err != nil {
		return err
	}
	return t.configureFirewall(ctx, internal.NodeName, interfaceName)
}

func (t *Topology) configureDefaultRoute(
	ctx context.Context,
	nodeName string,
	gateway string,
	interfaceName string,
) error {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	_, err := t.runner.Run(commandCtx, t.dockerCommand(
		"exec",
		nodeName,
		"ip",
		"route",
		"replace",
		"default",
		"via",
		gateway,
		"dev",
		interfaceName,
	))
	if err != nil {
		return fmt.Errorf("restoring internal default route in %s: %w", nodeName, err)
	}
	return t.validateDefaultRoute(ctx, nodeName, gateway, interfaceName)
}

func (t *Topology) validateDefaultRoute(
	ctx context.Context,
	nodeName string,
	gateway string,
	interfaceName string,
) error {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	result, err := t.runner.Run(commandCtx, t.dockerCommand(
		"exec",
		nodeName,
		"ip",
		"route",
		"show",
		"default",
	))
	if err != nil {
		return fmt.Errorf("reading default route in %s: %w", nodeName, err)
	}
	if !defaultRouteMatches(result.Stdout, gateway, interfaceName) {
		return fmt.Errorf("topology: default route does not use the internal network in %s", nodeName)
	}
	return nil
}

func defaultRouteMatches(output string, gateway string, interfaceName string) bool {
	lines := strings.FieldsFunc(strings.TrimSpace(output), func(character rune) bool {
		return character == '\n' || character == '\r'
	})
	if len(lines) != 1 {
		return false
	}
	fields := strings.Fields(lines[0])
	return len(fields) >= 5 &&
		fields[0] == "default" &&
		fields[1] == "via" &&
		fields[2] == gateway &&
		fields[3] == "dev" &&
		fields[4] == interfaceName
}

func (t *Topology) interfaceForIP(ctx context.Context, nodeName, address string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	result, err := t.runner.Run(commandCtx, t.dockerCommand(
		"exec",
		nodeName,
		"ip",
		"-o",
		"-4",
		"address",
		"show",
	))
	if err != nil {
		return "", fmt.Errorf("listing interfaces in %s: %w", nodeName, err)
	}
	for line := range strings.SplitSeq(result.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		interfaceIP, _, parseErr := net.ParseCIDR(fields[3])
		if parseErr == nil && interfaceIP.String() == address {
			return strings.TrimSuffix(fields[1], ":"), nil
		}
	}
	return "", fmt.Errorf("topology: interface for dmz address was not found in %s", nodeName)
}

func (t *Topology) configureFirewall(ctx context.Context, nodeName, interfaceName string) error {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	_, err := t.runner.Run(commandCtx, t.dockerCommand(
		"exec",
		nodeName,
		"sh",
		"-ceu",
		firewallScript,
		"sh",
		interfaceName,
		strconv.Itoa(AllowedProbePort),
	))
	if err != nil {
		return fmt.Errorf("configuring zone firewall in %s: %w", nodeName, err)
	}
	return nil
}

func (t *Topology) validateFirewall(ctx context.Context, dc string) error {
	dmz, err := t.config.Cluster(dc, "dmz")
	if err != nil {
		return err
	}
	internal, err := t.config.Cluster(dc, "internal")
	if err != nil {
		return err
	}
	container, err := t.inspectContainer(ctx, internal.NodeName)
	if err != nil {
		return err
	}
	internalAttachment, ok := container.NetworkSettings.Networks[internal.NetworkName]
	if !ok || net.ParseIP(internalAttachment.IPAddress).To4() == nil || net.ParseIP(internalAttachment.Gateway).To4() == nil {
		return fmt.Errorf("topology: %s is not connected to %s", internal.NodeName, internal.NetworkName)
	}
	internalInterface, err := t.interfaceForIP(ctx, internal.NodeName, internalAttachment.IPAddress)
	if err != nil {
		return err
	}
	if err := t.validateDefaultRoute(
		ctx,
		internal.NodeName,
		internalAttachment.Gateway,
		internalInterface,
	); err != nil {
		return err
	}
	attachment, ok := container.NetworkSettings.Networks[dmz.NetworkName]
	if !ok {
		return fmt.Errorf("topology: %s is not connected to %s", internal.NodeName, dmz.NetworkName)
	}
	interfaceName, err := t.interfaceForIP(ctx, internal.NodeName, attachment.IPAddress)
	if err != nil {
		return err
	}
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	_, err = t.runner.Run(commandCtx, t.dockerCommand(
		"exec",
		internal.NodeName,
		"sh",
		"-ceu",
		firewallCheckScript,
		"sh",
		interfaceName,
		strconv.Itoa(AllowedProbePort),
	))
	if err != nil {
		return fmt.Errorf("validating zone firewall in %s: %w", internal.NodeName, err)
	}
	return nil
}

func (t *Topology) checkZoneIsolation(ctx context.Context, dc string) error {
	dmz, err := t.config.Cluster(dc, "dmz")
	if err != nil {
		return err
	}
	internal, err := t.config.Cluster(dc, "internal")
	if err != nil {
		return err
	}
	dmzContainer, err := t.inspectContainer(ctx, dmz.NodeName)
	if err != nil {
		return err
	}
	internalContainer, err := t.inspectContainer(ctx, internal.NodeName)
	if err != nil {
		return err
	}
	dmzIP := dmzContainer.NetworkSettings.Networks[dmz.NetworkName].IPAddress
	internalDMZIP := internalContainer.NetworkSettings.Networks[dmz.NetworkName].IPAddress
	if net.ParseIP(dmzIP).To4() == nil || net.ParseIP(internalDMZIP).To4() == nil {
		return fmt.Errorf("topology: invalid probe addresses for %s", dc)
	}

	nodes := []string{dmz.NodeName, internal.NodeName}
	for _, node := range nodes {
		if err := t.installProbe(ctx, node); err != nil {
			return err
		}
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), commandTimeout)
	defer cleanupCancel()
	defer t.stopProbes(cleanupCtx, nodes)

	servers := []struct {
		node string
		port int
	}{
		{node: dmz.NodeName, port: AllowedProbePort},
		{node: dmz.NodeName, port: DeniedProbePort},
		{node: internal.NodeName, port: AllowedProbePort},
	}
	for _, server := range servers {
		if err := t.startProbeServer(ctx, server.node, server.port); err != nil {
			return err
		}
	}

	if _, err := t.probeConnection(ctx, internal.NodeName, dmzIP, AllowedProbePort); err != nil {
		return fmt.Errorf("topology: allowed internal to dmz probe failed in %s: %w", dc, err)
	}
	result, err := t.probeConnection(ctx, internal.NodeName, dmzIP, DeniedProbePort)
	if err == nil {
		return fmt.Errorf("topology: internal to dmz non-tunnel port was reachable in %s", dc)
	}
	if !isRejectedProbe(result) {
		return fmt.Errorf("topology: internal to dmz negative probe failed unexpectedly in %s: %w", dc, err)
	}
	result, err = t.probeConnection(ctx, dmz.NodeName, internalDMZIP, AllowedProbePort)
	if err == nil {
		return fmt.Errorf("topology: forbidden dmz to internal probe was reachable in %s", dc)
	}
	if !isRejectedProbe(result) {
		return fmt.Errorf("topology: dmz to internal negative probe failed unexpectedly in %s: %w", dc, err)
	}

	t.logger.InfoContext(ctx, "zone isolation verified", "dc", dc, "allowed_port", AllowedProbePort)
	return nil
}

func (t *Topology) installProbe(ctx context.Context, nodeName string) error {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if _, err := t.runner.Run(commandCtx, t.dockerCommand(
		"cp",
		t.config.ProbePath,
		nodeName+":"+probeContainerPath,
	)); err != nil {
		return fmt.Errorf("copying tcp probe to %s: %w", nodeName, err)
	}
	commandCtx, cancel = context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if _, err := t.runner.Run(commandCtx, t.dockerCommand(
		"exec",
		nodeName,
		"chmod",
		"0750",
		probeContainerPath,
	)); err != nil {
		return fmt.Errorf("setting tcp probe permissions in %s: %w", nodeName, err)
	}
	return nil
}

func (t *Topology) startProbeServer(ctx context.Context, nodeName string, port int) error {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if _, err := t.runner.Run(commandCtx, t.dockerCommand(
		"exec",
		"--detach",
		nodeName,
		probeContainerPath,
		"serve",
		"--port",
		strconv.Itoa(port),
		"--lifetime",
		"20s",
	)); err != nil {
		return fmt.Errorf("starting tcp probe in %s: %w", nodeName, err)
	}

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	for {
		commandCtx, checkCancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := t.runner.Run(commandCtx, t.dockerCommand(
			"exec",
			nodeName,
			"test",
			"-f",
			fmt.Sprintf("/run/mm28-topology/probe-%d.ready", port),
		))
		checkCancel()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("topology: tcp probe did not become ready in %s", nodeName)
		case <-poll.C:
		}
	}
}

func (t *Topology) probeConnection(ctx context.Context, source, address string, port int) (Result, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return t.runner.Run(commandCtx, t.dockerCommand(
		"exec",
		source,
		probeContainerPath,
		"connect",
		"--address",
		net.JoinHostPort(address, strconv.Itoa(port)),
		"--timeout",
		"3s",
	))
}

func isRejectedProbe(result Result) bool {
	return strings.Contains(result.Stderr, "tcpprobe: connection failed")
}

func (t *Topology) stopProbes(ctx context.Context, nodes []string) {
	for _, node := range nodes {
		for _, port := range []int{AllowedProbePort, DeniedProbePort} {
			commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, _ = t.runner.Run(commandCtx, t.dockerCommand(
				"exec",
				node,
				probeContainerPath,
				"stop",
				"--port",
				strconv.Itoa(port),
			))
			cancel()
		}
		commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, _ = t.runner.Run(commandCtx, t.dockerCommand("exec", node, "rm", "-f", probeContainerPath))
		cancel()
	}
}

func (t *Topology) cleanup(ctx context.Context) error {
	if err := t.ensureToolchain(); err != nil {
		return err
	}
	clusters, err := t.kindClusters(ctx)
	if err != nil {
		return err
	}
	clusterSet := make(map[string]struct{}, len(clusters))
	for _, cluster := range clusters {
		clusterSet[cluster] = struct{}{}
	}

	var cleanupErrors []error
	for _, cluster := range t.config.Clusters() {
		if !t.config.ownsResource(cluster.Name) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("topology: refusing unexpected cluster name %s", cluster.Name))
			continue
		}
		if _, exists := clusterSet[cluster.Name]; exists {
			if _, validateErr := t.validateContainer(ctx, cluster); validateErr != nil {
				cleanupErrors = append(cleanupErrors, validateErr)
				continue
			}
			commandCtx, cancel := context.WithTimeout(ctx, createTimeout)
			_, deleteErr := t.runner.Run(commandCtx, Command{
				Program: t.config.KindPath,
				Args:    []string{"delete", "cluster", "--name", cluster.Name},
				Env: append(
					t.config.kindEnvironment(""),
					"KUBECONFIG="+cluster.Kubeconfig,
				),
			})
			cancel()
			if deleteErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("deleting cluster %s: %w", cluster.Name, deleteErr))
				continue
			}
		}
		if removeErr := os.Remove(cluster.Kubeconfig); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("removing kubeconfig for %s: %w", cluster.Name, removeErr))
		}
	}

	for _, cluster := range t.config.Clusters() {
		exists, existsErr := t.networkExists(ctx, cluster.NetworkName)
		if existsErr != nil {
			cleanupErrors = append(cleanupErrors, existsErr)
			continue
		}
		if !exists {
			continue
		}
		network, inspectErr := t.inspectNetwork(ctx, cluster.NetworkName)
		if inspectErr != nil {
			cleanupErrors = append(cleanupErrors, inspectErr)
			continue
		}
		if network.Labels[ownerLabelKey] != TaskKey || network.Labels[instanceLabelKey] != t.config.Instance {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("topology: refusing unowned network %s", cluster.NetworkName))
			continue
		}
		if len(network.Containers) != 0 {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("topology: network %s still has containers", cluster.NetworkName))
			continue
		}
		commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
		_, removeErr := t.runner.Run(commandCtx, t.dockerCommand("network", "rm", cluster.NetworkName))
		cancel()
		if removeErr != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("removing network %s: %w", cluster.NetworkName, removeErr))
		}
	}

	if len(cleanupErrors) == 0 {
		if removeErr := t.removeInventory(); removeErr != nil {
			cleanupErrors = append(cleanupErrors, removeErr)
		}
	}

	if len(cleanupErrors) == 0 {
		t.logger.InfoContext(ctx, "topology resources removed", "instance", t.config.Instance)
	}
	return errors.Join(cleanupErrors...)
}

func (t *Topology) removeInventory() error {
	inventoryPath := filepath.Join(t.config.StateDir, "inventory.json")
	if err := os.Remove(inventoryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing topology inventory: %w", err)
	}
	return nil
}

func (t *Topology) dockerCommand(args ...string) Command {
	commandArgs := []string{"--context", t.config.DockerContext}
	commandArgs = append(commandArgs, args...)
	return Command{Program: "docker", Args: commandArgs}
}

func (t *Topology) runKubectl(
	ctx context.Context,
	timeout time.Duration,
	cluster Cluster,
	args ...string,
) (Result, error) {
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	commandArgs := []string{
		"--kubeconfig",
		cluster.Kubeconfig,
		"--context",
		cluster.KubeContext,
	}
	commandArgs = append(commandArgs, args...)
	return t.runner.Run(commandCtx, Command{Program: t.config.KubectlPath, Args: commandArgs})
}

func writePrivateFile(path string, data []byte) (returnErr error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, fmt.Errorf("removing temporary file: %w", err))
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

const firewallScript = `
iface="$1"
port="$2"
for chain in MM28-IN MM28-OUT MM28-FWD; do
  iptables -w 5 -N "$chain" 2>/dev/null || true
  iptables -w 5 -F "$chain"
done
iptables -w 5 -C INPUT -i "$iface" -j MM28-IN 2>/dev/null || iptables -w 5 -I INPUT 1 -i "$iface" -j MM28-IN
iptables -w 5 -C OUTPUT -o "$iface" -j MM28-OUT 2>/dev/null || iptables -w 5 -I OUTPUT 1 -o "$iface" -j MM28-OUT
iptables -w 5 -C FORWARD -i "$iface" -j MM28-FWD 2>/dev/null || iptables -w 5 -I FORWARD 1 -i "$iface" -j MM28-FWD
iptables -w 5 -C FORWARD -o "$iface" -j MM28-FWD 2>/dev/null || iptables -w 5 -I FORWARD 1 -o "$iface" -j MM28-FWD
iptables -w 5 -A MM28-IN -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -w 5 -A MM28-IN -j REJECT --reject-with icmp-port-unreachable
iptables -w 5 -A MM28-OUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -w 5 -A MM28-OUT -p tcp --dport "$port" -j ACCEPT
iptables -w 5 -A MM28-OUT -j REJECT --reject-with icmp-port-unreachable
iptables -w 5 -A MM28-FWD -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -w 5 -A MM28-FWD -o "$iface" -p tcp --dport "$port" -j ACCEPT
iptables -w 5 -A MM28-FWD -j REJECT --reject-with icmp-port-unreachable
`

const firewallCheckScript = `
iface="$1"
port="$2"
iptables -w 5 -C INPUT -i "$iface" -j MM28-IN
iptables -w 5 -C OUTPUT -o "$iface" -j MM28-OUT
iptables -w 5 -C FORWARD -i "$iface" -j MM28-FWD
iptables -w 5 -C FORWARD -o "$iface" -j MM28-FWD
iptables -w 5 -C MM28-IN -j REJECT --reject-with icmp-port-unreachable
iptables -w 5 -C MM28-OUT -p tcp --dport "$port" -j ACCEPT
iptables -w 5 -C MM28-OUT -j REJECT --reject-with icmp-port-unreachable
iptables -w 5 -C MM28-FWD -o "$iface" -p tcp --dport "$port" -j ACCEPT
iptables -w 5 -C MM28-FWD -j REJECT --reject-with icmp-port-unreachable
`
