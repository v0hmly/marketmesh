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
	TaskKey = "MM-44"
	// K3sVersion is the pinned k3s release installed inside every topology VM.
	K3sVersion = "v1.36.4+k3s1"
	// KubectlVersion is the pinned kubectl release used on the host.
	KubectlVersion = "v1.37.0"
	// RuntimeName identifies the runtime stack recorded in the public inventory.
	RuntimeName = "orbstack-vm+k3s"
	// Namespace carries the logical cluster identity without installing workloads.
	Namespace = "marketmesh-system"
	// AllowedProbePort is the only cross-zone TCP port permitted by the test firewall.
	AllowedProbePort = 30443
	// DeniedProbePort is used to prove that arbitrary cross-zone traffic is blocked.
	DeniedProbePort = 30444

	// VMDistro is the pinned OrbStack machine image for every logical cluster.
	VMDistro = "ubuntu:24.04"
	// VMCPUs is the per-machine vCPU limit.
	VMCPUs = "2"
	// VMMemory is the per-machine memory limit.
	VMMemory = "2G"
	// VMDisk is the per-machine disk size.
	VMDisk = "20G"

	// IPTablesVersion is the pinned apt package version of iptables (nft frontend)
	// installed into every topology VM; the base OrbStack ubuntu:24.04 image does
	// not ship a firewall toolchain.
	IPTablesVersion = "1.8.10-3ubuntu2"

	commandTimeout     = 30 * time.Second
	createTimeout      = 5 * time.Minute
	readyTimeout       = 2 * time.Minute
	diagnosticsTimeout = 2 * time.Minute
)

// k3sBinarySHA256 pins the linux k3s binary for each supported host architecture;
// OrbStack machines always share the host architecture.
var k3sBinarySHA256 = map[string]string{
	"arm64": "c920706346d5ad4e5cd3c7bf1bb09ce71ebe07fec829e513e40f1caf98aed8bb",
	"amd64": "835873f37245fc615f547a2fe2af9402a347875f13fa64a1f136de644955ea3f",
}

var (
	instancePattern    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,18}[a-z0-9])?$`)
	contextNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	vmUserPattern      = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

// Config contains validated paths and names for one disposable topology instance.
type Config struct {
	RepositoryRoot string
	Instance       string
	StateDir       string
	BinDir         string
	DiagnosticsDir string
	K3sPath        string
	KubectlPath    string
	ProbePath      string
}

// Cluster describes one logical Kubernetes cluster and its dedicated OrbStack VM.
type Cluster struct {
	LogicalName string
	Name        string
	NodeName    string
	KubeContext string
	Kubeconfig  string
	DC          string
	Zone        string
}

// NewConfig validates operator input and derives repository-local state paths.
func NewConfig(repositoryRoot, instance string) (Config, error) {
	if !filepath.IsAbs(repositoryRoot) {
		return Config{}, errors.New("topology: repository root must be absolute")
	}
	if !instancePattern.MatchString(instance) {
		return Config{}, errors.New("topology: instance must be 1-20 lowercase letters, digits, or hyphens")
	}

	stateDir := filepath.Join(repositoryRoot, ".cache", "e2e-topology", instance)
	binDir := filepath.Join(stateDir, "bin")

	return Config{
		RepositoryRoot: repositoryRoot,
		Instance:       instance,
		StateDir:       stateDir,
		BinDir:         binDir,
		DiagnosticsDir: filepath.Join(stateDir, "diagnostics"),
		K3sPath:        filepath.Join(binDir, "k3s"),
		KubectlPath:    filepath.Join(binDir, "kubectl"),
		ProbePath:      filepath.Join(binDir, "mm44-tcpprobe"),
	}, nil
}

// Clusters returns the four logical clusters in deterministic order.
func (c Config) Clusters() []Cluster {
	specs := []struct {
		logicalName string
		dc          string
		zone        string
	}{
		{logicalName: "dc-a-dmz", dc: "dc-a", zone: "dmz"},
		{logicalName: "dc-a-internal", dc: "dc-a", zone: "internal"},
		{logicalName: "dc-b-dmz", dc: "dc-b", zone: "dmz"},
		{logicalName: "dc-b-internal", dc: "dc-b", zone: "internal"},
	}

	clusters := make([]Cluster, 0, len(specs))
	for _, spec := range specs {
		name := c.Instance + "-" + spec.logicalName
		clusters = append(clusters, Cluster{
			LogicalName: spec.logicalName,
			Name:        name,
			NodeName:    name,
			KubeContext: name,
			Kubeconfig:  filepath.Join(c.StateDir, "kubeconfigs", spec.logicalName+".yaml"),
			DC:          spec.dc,
			Zone:        spec.zone,
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

func (c Config) ownsResource(name string) bool {
	return strings.HasPrefix(name, c.Instance+"-") && len(name) > len(c.Instance)+1
}
