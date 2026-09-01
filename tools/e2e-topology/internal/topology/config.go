package topology

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// TaskKey identifies the Taskboard task that owns all disposable resources.
	TaskKey = "MM-28"
	// KindVersion is the pinned kind CLI release.
	KindVersion = "v0.33.0"
	// KubernetesVersion is the pinned Kubernetes release used by the topology.
	KubernetesVersion = "v1.37.0"
	// NodeImage is the immutable kind node image used by every logical cluster.
	NodeImage = "kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5"
	// Namespace carries the logical cluster identity without installing workloads.
	Namespace = "marketmesh-system"
	// AllowedProbePort is the only cross-zone TCP port permitted by the test firewall.
	AllowedProbePort = 30443
	// DeniedProbePort is used to prove that arbitrary cross-zone traffic is blocked.
	DeniedProbePort = 30444

	commandTimeout     = 30 * time.Second
	createTimeout      = 5 * time.Minute
	readyTimeout       = 2 * time.Minute
	diagnosticsTimeout = 2 * time.Minute
)

var (
	instancePattern      = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,18}[a-z0-9])?$`)
	dockerContextPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
)

// Config contains validated paths and names for one disposable topology instance.
type Config struct {
	RepositoryRoot string
	Instance       string
	DockerContext  string
	StateDir       string
	BinDir         string
	DiagnosticsDir string
	KindPath       string
	KubectlPath    string
	ProbePath      string
}

// Cluster describes one logical Kubernetes cluster and its isolated network.
type Cluster struct {
	LogicalName   string
	Name          string
	NodeName      string
	NetworkName   string
	KubeContext   string
	Kubeconfig    string
	DC            string
	Zone          string
	DockerSubnet  string
	PodSubnet     string
	ServiceSubnet string
}

// NewConfig validates operator input and derives repository-local state paths.
func NewConfig(repositoryRoot, instance, dockerContext string) (Config, error) {
	if !filepath.IsAbs(repositoryRoot) {
		return Config{}, errors.New("topology: repository root must be absolute")
	}
	if !instancePattern.MatchString(instance) {
		return Config{}, errors.New("topology: instance must be 1-20 lowercase letters, digits, or hyphens")
	}
	if !dockerContextPattern.MatchString(dockerContext) {
		return Config{}, errors.New("topology: docker context contains unsupported characters")
	}

	stateDir := filepath.Join(repositoryRoot, ".cache", "mm28-topology", instance)
	binDir := filepath.Join(stateDir, "bin")

	return Config{
		RepositoryRoot: repositoryRoot,
		Instance:       instance,
		DockerContext:  dockerContext,
		StateDir:       stateDir,
		BinDir:         binDir,
		DiagnosticsDir: filepath.Join(stateDir, "diagnostics"),
		KindPath:       filepath.Join(binDir, "kind"),
		KubectlPath:    filepath.Join(binDir, "kubectl"),
		ProbePath:      filepath.Join(binDir, "mm28-tcpprobe"),
	}, nil
}

// Clusters returns the four logical clusters in deterministic order.
func (c Config) Clusters() []Cluster {
	specs := []struct {
		logicalName   string
		dc            string
		zone          string
		dockerSubnet  string
		podSubnet     string
		serviceSubnet string
	}{
		{
			logicalName:   "dc-a-dmz",
			dc:            "dc-a",
			zone:          "dmz",
			dockerSubnet:  "172.28.10.0/24",
			podSubnet:     "10.28.0.0/16",
			serviceSubnet: "10.128.0.0/16",
		},
		{
			logicalName:   "dc-a-internal",
			dc:            "dc-a",
			zone:          "internal",
			dockerSubnet:  "172.28.11.0/24",
			podSubnet:     "10.29.0.0/16",
			serviceSubnet: "10.129.0.0/16",
		},
		{
			logicalName:   "dc-b-dmz",
			dc:            "dc-b",
			zone:          "dmz",
			dockerSubnet:  "172.28.20.0/24",
			podSubnet:     "10.30.0.0/16",
			serviceSubnet: "10.130.0.0/16",
		},
		{
			logicalName:   "dc-b-internal",
			dc:            "dc-b",
			zone:          "internal",
			dockerSubnet:  "172.28.21.0/24",
			podSubnet:     "10.31.0.0/16",
			serviceSubnet: "10.131.0.0/16",
		},
	}

	clusters := make([]Cluster, 0, len(specs))
	for _, spec := range specs {
		name := c.Instance + "-" + spec.logicalName
		clusters = append(clusters, Cluster{
			LogicalName:   spec.logicalName,
			Name:          name,
			NodeName:      name + "-control-plane",
			NetworkName:   name,
			KubeContext:   "kind-" + name,
			Kubeconfig:    filepath.Join(c.StateDir, "kubeconfigs", spec.logicalName+".yaml"),
			DC:            spec.dc,
			Zone:          spec.zone,
			DockerSubnet:  spec.dockerSubnet,
			PodSubnet:     spec.podSubnet,
			ServiceSubnet: spec.serviceSubnet,
		})
	}

	return clusters
}

// Cluster returns the cluster assigned to a data center and security zone.
func (c Config) Cluster(dc, zone string) (Cluster, error) {
	for _, cluster := range c.Clusters() {
		if cluster.DC == dc && cluster.Zone == zone {
			return cluster, nil
		}
	}

	return Cluster{}, fmt.Errorf("topology: unknown cluster %s/%s", dc, zone)
}

func (c Config) kindEnvironment(network string) []string {
	values := []string{
		"DOCKER_CONTEXT=" + c.DockerContext,
		"KIND_EXPERIMENTAL_PROVIDER=docker",
	}
	if network != "" {
		values = append(values, "KIND_EXPERIMENTAL_DOCKER_NETWORK="+network)
	}
	return values
}

func (c Config) ownsResource(name string) bool {
	return strings.HasPrefix(name, c.Instance+"-") && len(name) > len(c.Instance)+1
}

func kindConfig(cluster Cluster) string {
	return fmt.Sprintf(`kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  ipFamily: ipv4
  podSubnet: %q
  serviceSubnet: %q
nodes:
  - role: control-plane
`, cluster.PodSubnet, cluster.ServiceSubnet)
}
