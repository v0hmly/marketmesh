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
	"strconv"
	"strings"
	"time"
)

const (
	probeVMPath        = "/usr/local/bin/mm44-tcpprobe"
	probeVMRuntimeDir  = "/run/mm44-topology"
	k3sKubeconfigPath  = "/etc/rancher/k3s/k3s.yaml"
	k3sServicePath     = "/etc/systemd/system/k3s.service"
	dmzChainName       = "MM44-DMZ-IN"
	internalChainName  = "MM44-INT-IN"
	machinePollDelay   = 500 * time.Millisecond
	probeServeLifetime = "20s"
)

// Topology manages one strictly named, disposable four-cluster environment.
type Topology struct {
	config Config
	runner Runner
	logger *slog.Logger
	now    func() time.Time
}

// orbMachine is the flat runtime view of one OrbStack machine, assembled from
// the real orbctl JSON schemas by parseMachineDocument and parseMachineList.
type orbMachine struct {
	ID    string
	Name  string
	State string
	IPv4  string
	Image struct {
		Distro  string
		Version string
		Arch    string
	}
	Config struct {
		DefaultUsername string
	}
}

// orbMachineRecord mirrors one flat record of `orbctl list -f json`.
type orbMachineRecord struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
	Image struct {
		Distro  string `json:"distro"`
		Version string `json:"version"`
		Arch    string `json:"arch"`
	} `json:"image"`
	Config struct {
		DefaultUsername string `json:"default_username"`
	} `json:"config"`
}

// orbMachineInfo mirrors the object returned by `orbctl info -f json`.
type orbMachineInfo struct {
	Record orbMachineRecord `json:"record"`
	IPv4   string           `json:"ip4"`
	IPv6   string           `json:"ip6"`
}

func machineFromRecord(record orbMachineRecord, ipv4 string) orbMachine {
	machine := orbMachine{
		ID:    record.ID,
		Name:  record.Name,
		State: record.State,
		IPv4:  ipv4,
	}
	machine.Image.Distro = record.Image.Distro
	machine.Image.Version = record.Image.Version
	machine.Image.Arch = record.Image.Arch
	machine.Config.DefaultUsername = record.Config.DefaultUsername
	return machine
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
	if err := t.orbStackReady(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(t.config.StateDir, "kubeconfigs"), 0o750); err != nil {
		return fmt.Errorf("creating kubeconfig directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(t.config.StateDir, "configs"), 0o750); err != nil {
		return fmt.Errorf("creating machine config directory: %w", err)
	}

	machines := make(map[string]orbMachine, len(t.config.Clusters()))
	for _, cluster := range t.config.Clusters() {
		machine, err := t.ensureMachine(ctx, cluster)
		if err != nil {
			return err
		}
		if err := t.installK3s(ctx, cluster, machine); err != nil {
			return err
		}
		if err := t.ensureFirewallToolchain(ctx, cluster); err != nil {
			return err
		}
		if err := t.refreshKubeconfig(ctx, cluster, machine); err != nil {
			return err
		}
		machines[cluster.LogicalName] = machine
	}

	if err := t.isolateZones(ctx); err != nil {
		return err
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
	if err := t.orbStackReady(ctx); err != nil {
		return err
	}

	for _, cluster := range t.config.Clusters() {
		if _, err := t.requireRunningMachine(ctx, cluster); err != nil {
			return err
		}
		if err := t.waitForCluster(ctx, cluster); err != nil {
			return err
		}
		if err := t.validateIdentity(ctx, cluster); err != nil {
			return err
		}
	}

	if err := t.validateFirewall(ctx); err != nil {
		return err
	}
	if err := t.checkZoneIsolation(ctx); err != nil {
		return err
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
		"k3s":        K3sVersion,
		"kubectl":    KubectlVersion,
		"runtime":    RuntimeName,
		"probe_port": strconv.Itoa(AllowedProbePort),
	}
	if err := t.ensureToolchain(); err != nil {
		versions["installed"] = "false"
		return versions, nil
	}

	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	result, err := t.runner.Run(commandCtx, Command{Program: "orbctl", Args: []string{"version"}})
	if err != nil {
		return nil, err
	}
	versions["installed"] = "true"
	versions["orbctl"] = strings.TrimSpace(result.Stdout)
	return versions, nil
}

func (t *Topology) ensureToolchain() error {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	k3sAsset, err := k3sAssetFor(runtime.GOARCH)
	if err != nil {
		return err
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
		{path: t.config.K3sPath, hash: k3sAsset.sha256, name: "k3s"},
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

// orbStackReady proves that the OrbStack service itself is running.
func (t *Topology) orbStackReady(ctx context.Context) error {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if _, err := t.runner.Run(commandCtx, Command{Program: "orbctl", Args: []string{"status"}}); err != nil {
		return errors.New("topology: OrbStack is not running; start it first")
	}
	return nil
}

// listMachines returns every OrbStack machine known to the local service.
// The list schema carries no addresses; IPv4 is resolved per machine via info.
func (t *Topology) listMachines(ctx context.Context) ([]orbMachine, error) {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	result, err := t.runner.Run(commandCtx, Command{
		Program: "orbctl",
		Args:    []string{"list", "--format", "json"},
	})
	if err != nil {
		return nil, fmt.Errorf("listing orbstack machines: %w", err)
	}
	return parseMachineList(result.Stdout)
}

func parseMachineList(document string) ([]orbMachine, error) {
	records := []orbMachineRecord{}
	if err := json.Unmarshal([]byte(document), &records); err != nil {
		return nil, errors.New("topology: invalid orbstack machine list document")
	}
	machines := make([]orbMachine, 0, len(records))
	for _, record := range records {
		machines = append(machines, machineFromRecord(record, ""))
	}
	return machines, nil
}

// inspectMachine returns the machine with the exact requested name, failing closed otherwise.
func (t *Topology) inspectMachine(ctx context.Context, name string) (orbMachine, error) {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	result, err := t.runner.Run(commandCtx, Command{
		Program: "orbctl",
		Args:    []string{"info", name, "--format", "json"},
	})
	if err != nil {
		return orbMachine{}, fmt.Errorf("inspecting machine %s: %w", name, err)
	}
	return parseMachineDocument(result.Stdout, name)
}

func parseMachineDocument(document, expectedName string) (orbMachine, error) {
	info := orbMachineInfo{}
	if err := json.Unmarshal([]byte(document), &info); err != nil {
		return orbMachine{}, fmt.Errorf("topology: invalid orbstack machine inspection for %s", expectedName)
	}
	if info.Record.Name != expectedName {
		return orbMachine{}, fmt.Errorf("topology: machine name mismatch for %s", expectedName)
	}
	return machineFromRecord(info.Record, info.IPv4), nil
}

// requireRunningMachine validates name, state, and IPv4 of a topology machine.
func (t *Topology) requireRunningMachine(ctx context.Context, cluster Cluster) (orbMachine, error) {
	machine, err := t.inspectMachine(ctx, cluster.Name)
	if err != nil {
		return orbMachine{}, err
	}
	if machine.State != "running" {
		return orbMachine{}, fmt.Errorf("topology: machine %s is not running", cluster.Name)
	}
	if net.ParseIP(machine.IPv4).To4() == nil {
		return orbMachine{}, fmt.Errorf("topology: machine %s has no valid ipv4 address", cluster.Name)
	}
	return machine, nil
}

// ensureMachine creates the OrbStack VM for one cluster or validates an existing owned one.
func (t *Topology) ensureMachine(ctx context.Context, cluster Cluster) (orbMachine, error) {
	if !t.config.ownsResource(cluster.Name) {
		return orbMachine{}, fmt.Errorf("topology: refusing unexpected machine name %s", cluster.Name)
	}
	machines, err := t.listMachines(ctx)
	if err != nil {
		return orbMachine{}, err
	}
	exists := false
	for _, machine := range machines {
		if machine.Name == cluster.Name {
			exists = true
			break
		}
	}

	if exists {
		machine, infoErr := t.inspectMachine(ctx, cluster.Name)
		if infoErr != nil {
			return orbMachine{}, infoErr
		}
		if err := validateMachineShape(cluster, machine); err != nil {
			return orbMachine{}, err
		}
		if machine.State != "running" {
			commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
			_, startErr := t.runner.Run(commandCtx, Command{
				Program: "orbctl",
				Args:    []string{"start", cluster.Name},
			})
			cancel()
			if startErr != nil {
				return orbMachine{}, fmt.Errorf("starting machine %s: %w", cluster.Name, startErr)
			}
		}
		return t.waitMachineIPv4(ctx, cluster)
	}

	commandCtx, cancel := context.WithTimeout(ctx, machineCreateTimeout)
	_, err = t.runner.Run(commandCtx, Command{
		Program: "orbctl",
		Args: []string{
			"create",
			VMDistro,
			cluster.Name,
			"--cpus",
			VMCPUs,
			"--memory",
			VMMemory,
			"--disk",
			VMDisk,
		},
	})
	cancel()
	if err != nil {
		return orbMachine{}, fmt.Errorf("creating machine %s: %w", cluster.Name, err)
	}
	machine, err := t.waitMachineIPv4(ctx, cluster)
	if err != nil {
		return orbMachine{}, err
	}
	return machine, validateMachineShape(cluster, machine)
}

func validateMachineShape(cluster Cluster, machine orbMachine) error {
	// orbctl отдаёт версию образа кодовым именем: ubuntu:24.04 → "noble".
	if machine.Image.Distro != "ubuntu" || (machine.Image.Version != "noble" && machine.Image.Version != "24.04") {
		return fmt.Errorf("topology: machine %s uses an unexpected image", cluster.Name)
	}
	if machine.Image.Arch != runtime.GOARCH {
		return fmt.Errorf(
			"topology: machine %s architecture %q does not match host architecture %q",
			cluster.Name,
			machine.Image.Arch,
			runtime.GOARCH,
		)
	}
	if !vmUserPattern.MatchString(machine.Config.DefaultUsername) {
		return fmt.Errorf("topology: machine %s has an unexpected default user", cluster.Name)
	}
	return nil
}

// waitMachineIPv4 polls bounded until OrbStack reports an IPv4 address for the machine.
func (t *Topology) waitMachineIPv4(ctx context.Context, cluster Cluster) (orbMachine, error) {
	deadline := time.NewTimer(createTimeout)
	defer deadline.Stop()
	poll := time.NewTicker(machinePollDelay)
	defer poll.Stop()
	for {
		machine, err := t.inspectMachine(ctx, cluster.Name)
		if err == nil && net.ParseIP(machine.IPv4).To4() != nil {
			return machine, nil
		}
		select {
		case <-ctx.Done():
			return orbMachine{}, ctx.Err()
		case <-deadline.C:
			return orbMachine{}, fmt.Errorf("topology: machine %s did not receive an ipv4 address", cluster.Name)
		case <-poll.C:
		}
	}
}

// ensureFirewallToolchain installs the pinned iptables package into the VM.
// The base OrbStack ubuntu:24.04 image ships neither iptables nor nft, so the
// zone firewall cannot be configured otherwise. Ubuntu noble ships iptables
// 1.8.x as iptables-nft over nf_tables, which is compatible with the rule
// syntax used here. The pinned apt version is deliberate fail-fast: when the
// noble repository rotates the candidate, installation fails loudly and the
// pin must be reviewed instead of silently drifting.
func (t *Topology) ensureFirewallToolchain(ctx context.Context, cluster Cluster) error {
	result, err := t.runMachineSudoResult(ctx, commandTimeout, cluster.Name, "iptables", "--version")
	if err == nil {
		return validateIPTablesVersion(cluster.Name, result.Stdout)
	}

	t.logger.InfoContext(ctx, "installing pinned firewall toolchain", "machine", cluster.Name)
	if err := t.runMachineSudo(ctx, createTimeout, cluster.Name,
		"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "update"); err != nil {
		return fmt.Errorf("updating apt indexes in %s: %w", cluster.Name, err)
	}
	if err := t.runMachineSudo(ctx, createTimeout, cluster.Name,
		"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y",
		"--no-install-recommends", "iptables="+IPTablesVersion); err != nil {
		return fmt.Errorf("installing pinned iptables in %s: %w", cluster.Name, err)
	}

	result, err = t.runMachineSudoResult(ctx, commandTimeout, cluster.Name, "iptables", "--version")
	if err != nil {
		return fmt.Errorf("checking installed iptables in %s: %w", cluster.Name, err)
	}
	return validateIPTablesVersion(cluster.Name, result.Stdout)
}

// validateIPTablesVersion fails closed unless the installed binary reports the
// pinned 1.8.10 line (nftables frontend suffix like "(nf_tables)" is allowed).
func validateIPTablesVersion(name, output string) error {
	if !strings.HasPrefix(strings.TrimSpace(output), "iptables v1.8.10") {
		return fmt.Errorf("topology: machine %s has an unexpected iptables version %q", name, strings.TrimSpace(output))
	}
	return nil
}

// installK3s pushes the pinned k3s binary and systemd unit into the VM and starts the service.
func (t *Topology) installK3s(ctx context.Context, cluster Cluster, machine orbMachine) error {
	home := "/home/" + machine.Config.DefaultUsername

	if err := t.pushToMachine(ctx, cluster.Name, t.config.K3sPath, "k3s"); err != nil {
		return err
	}
	if err := t.runMachineSudo(ctx, createTimeout, cluster.Name,
		"install", "-m", "0755", home+"/k3s", "/usr/local/bin/k3s"); err != nil {
		return fmt.Errorf("installing k3s in %s: %w", cluster.Name, err)
	}

	unitPath := filepath.Join(t.config.StateDir, "configs", cluster.LogicalName+"-k3s.service")
	if err := writePrivateFile(unitPath, []byte(k3sUnit(machine.IPv4))); err != nil {
		return fmt.Errorf("writing k3s unit for %s: %w", cluster.Name, err)
	}
	if err := t.pushToMachine(ctx, cluster.Name, unitPath, "k3s.service"); err != nil {
		return err
	}
	if err := t.runMachineSudo(ctx, commandTimeout, cluster.Name,
		"install", "-m", "0644", home+"/k3s.service", k3sServicePath); err != nil {
		return fmt.Errorf("installing k3s unit in %s: %w", cluster.Name, err)
	}
	if err := t.runMachineSudo(ctx, commandTimeout, cluster.Name, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("reloading systemd in %s: %w", cluster.Name, err)
	}
	if err := t.runMachineSudo(ctx, commandTimeout, cluster.Name, "systemctl", "enable", "--now", "k3s"); err != nil {
		return fmt.Errorf("enabling k3s in %s: %w", cluster.Name, err)
	}
	return t.waitK3sServer(ctx, cluster)
}

// pushToMachine copies a host file into the machine home directory (relative destination).
func (t *Topology) pushToMachine(ctx context.Context, name, source, destination string) error {
	commandCtx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	_, err := t.runner.Run(commandCtx, Command{
		Program: "orbctl",
		Args:    []string{"push", "-m", name, source, destination},
	})
	if err != nil {
		return fmt.Errorf("pushing %s to machine %s: %w", filepath.Base(source), name, err)
	}
	return nil
}

// waitK3sServer polls bounded until k3s has written its admin kubeconfig.
func (t *Topology) waitK3sServer(ctx context.Context, cluster Cluster) error {
	deadline := time.NewTimer(readyTimeout)
	defer deadline.Stop()
	poll := time.NewTicker(machinePollDelay)
	defer poll.Stop()
	for {
		commandCtx, checkCancel := context.WithTimeout(ctx, commandTimeout)
		_, err := t.runner.Run(commandCtx, Command{
			Program: "orbctl",
			Args:    []string{"run", "-m", cluster.Name, "sudo", "-n", "test", "-f", k3sKubeconfigPath},
		})
		checkCancel()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("topology: k3s did not become ready in %s", cluster.Name)
		case <-poll.C:
		}
	}
}

// refreshKubeconfig extracts the k3s admin kubeconfig and rewrites it for host access.
func (t *Topology) refreshKubeconfig(ctx context.Context, cluster Cluster, machine orbMachine) error {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	result, err := t.runner.Run(commandCtx, Command{
		Program: "orbctl",
		Args:    []string{"run", "-m", cluster.Name, "sudo", "-n", "cat", k3sKubeconfigPath},
	})
	cancel()
	if err != nil {
		return fmt.Errorf("reading kubeconfig in %s: %w", cluster.Name, err)
	}
	rewritten, err := rewriteKubeconfig([]byte(result.Stdout), cluster.KubeContext, machine.IPv4)
	if err != nil {
		return fmt.Errorf("rewriting kubeconfig for %s: %w", cluster.Name, err)
	}
	if err := writePrivateFile(cluster.Kubeconfig, rewritten); err != nil {
		return fmt.Errorf("writing kubeconfig for %s: %w", cluster.Name, err)
	}
	return nil
}

// rewriteKubeconfig retargets a stock k3s kubeconfig to the VM address and the
// instance-scoped context, user, and cluster name. It fails closed on any
// deviation from the deterministic k3s document shape.
func rewriteKubeconfig(data []byte, contextName, serverIPv4 string) ([]byte, error) {
	if net.ParseIP(serverIPv4).To4() == nil {
		return nil, errors.New("topology: kubeconfig server address is not a valid ipv4")
	}
	if !contextNamePattern.MatchString(contextName) {
		return nil, errors.New("topology: kubeconfig context name is invalid")
	}

	serverLine := "server: https://" + serverIPv4 + ":6443"
	counts := map[string]int{}
	lines := strings.Split(string(data), "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		replacement := ""
		switch trimmed {
		case "server: https://127.0.0.1:6443":
			replacement = indent + serverLine
		case "name: default":
			replacement = indent + "name: " + contextName
		case "- name: default":
			replacement = indent + "- name: " + contextName
		case "cluster: default":
			replacement = indent + "cluster: " + contextName
		case "user: default":
			replacement = indent + "user: " + contextName
		case "current-context: default":
			replacement = indent + "current-context: " + contextName
		default:
			continue
		}
		counts[trimmed]++
		lines[index] = replacement
	}

	expected := map[string]int{
		"server: https://127.0.0.1:6443": 1,
		"name: default":                  2,
		"- name: default":                1,
		"cluster: default":               1,
		"user: default":                  1,
		"current-context: default":       1,
	}
	for line, want := range expected {
		if counts[line] != want {
			return nil, fmt.Errorf("topology: unexpected k3s kubeconfig shape (%q seen %d times, want %d)", line, counts[line], want)
		}
	}
	rewritten := []byte(strings.Join(lines, "\n"))
	if !strings.Contains(string(rewritten), "certificate-authority-data:") {
		return nil, errors.New("topology: rewritten kubeconfig lost the cluster certificate")
	}
	return rewritten, nil
}

// k3sUnit renders the canonical k3s server systemd unit with topology flags.
func k3sUnit(machineIPv4 string) string {
	return fmt.Sprintf(`[Unit]
Description=Lightweight Kubernetes
Documentation=https://k3s.io
Wants=network-online.target
After=network-online.target

[Install]
WantedBy=multi-user.target

[Service]
Type=notify
EnvironmentFile=-/etc/default/%%N
EnvironmentFile=-/etc/sysconfig/%%N
EnvironmentFile=-/etc/systemd/system/k3s.service.env
KillMode=process
Delegate=yes
LimitNOFILE=1048576
LimitNPROC=infinity
LimitCORE=infinity
TasksMax=infinity
TimeoutStartSec=0
Restart=always
RestartSec=5s
ExecStartPre=/bin/sh -xc '! /usr/bin/systemctl is-enabled --quiet nm-cloud-setup.service 2>/dev/null'
ExecStart=/usr/local/bin/k3s server --disable=traefik --disable=servicelb --disable=metrics-server --write-kubeconfig-mode=0600 --node-ip=%[1]s --tls-san=%[1]s
`, machineIPv4)
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

// isolateZones installs the full mesh zone firewall inside every VM. All four
// VMs share one OrbStack L2 segment, so each VM gates INPUT on the IPv4 of each
// of the three other VMs and routes it into one dedicated chain. Exactly one
// VM-to-VM flow is allowed: a DMZ VM accepts tcp/30443 from the internal VM of
// the same DC. Everywhere ESTABLISHED,RELATED is accepted first so replies to
// that allowed outbound flow pass; all other VM-to-VM packets — cross-DC,
// DMZ-to-DMZ, internal-to-internal and DMZ-initiated — are dropped. Traffic
// from the host is left untouched.
func (t *Topology) isolateZones(ctx context.Context) error {
	machines, err := t.runningMachines(ctx)
	if err != nil {
		return err
	}
	for _, cluster := range t.config.Clusters() {
		rules, err := zoneChainRules(cluster, machines)
		if err != nil {
			return err
		}
		if err := t.configureZoneFirewall(
			ctx,
			cluster.Name,
			t.peerIPv4s(machines, cluster.LogicalName),
			zoneChainName(cluster),
			rules,
		); err != nil {
			return err
		}
	}
	return nil
}

// runningMachines validates and returns every owned machine by logical name.
func (t *Topology) runningMachines(ctx context.Context) (map[string]orbMachine, error) {
	machines := make(map[string]orbMachine, len(t.config.Clusters()))
	for _, cluster := range t.config.Clusters() {
		machine, err := t.requireRunningMachine(ctx, cluster)
		if err != nil {
			return nil, err
		}
		machines[cluster.LogicalName] = machine
	}
	return machines, nil
}

// peerIPv4s returns the addresses of all other topology VMs in deterministic order.
func (t *Topology) peerIPv4s(machines map[string]orbMachine, logicalName string) []string {
	peers := make([]string, 0, len(machines)-1)
	for _, cluster := range t.config.Clusters() {
		if cluster.LogicalName == logicalName {
			continue
		}
		peers = append(peers, machines[cluster.LogicalName].IPv4)
	}
	return peers
}

func zoneChainName(cluster Cluster) string {
	if cluster.Zone == "dmz" {
		return dmzChainName
	}
	return internalChainName
}

// zoneChainRules renders the chain content for one VM: established flows, the
// single allowed same-DC internal → DMZ tunnel port for DMZ VMs, then DROP.
func zoneChainRules(cluster Cluster, machines map[string]orbMachine) ([][]string, error) {
	rules := [][]string{
		{"-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
	}
	if cluster.Zone == "dmz" {
		internal, ok := machines[cluster.DC+"-internal"]
		if !ok {
			return nil, fmt.Errorf("topology: no internal machine for %s", cluster.DC)
		}
		rules = append(rules, []string{
			"-s", internal.IPv4,
			"-p", "tcp", "--dport", strconv.Itoa(AllowedProbePort),
			"-j", "ACCEPT",
		})
	}
	return append(rules, []string{"-j", "DROP"}), nil
}

// configureZoneFirewall rebuilds one idempotent INPUT chain gated on every peer VM address.
func (t *Topology) configureZoneFirewall(ctx context.Context, name string, peerIPv4s []string, chain string, rules [][]string) error {
	if err := t.runIPTables(ctx, name, "-L", chain, "-n"); err != nil {
		if err := t.runIPTables(ctx, name, "-N", chain); err != nil {
			return fmt.Errorf("creating firewall chain %s in %s: %w", chain, name, err)
		}
	}
	if err := t.runIPTables(ctx, name, "-F", chain); err != nil {
		return fmt.Errorf("flushing firewall chain %s in %s: %w", chain, name, err)
	}
	for _, rule := range rules {
		args := append([]string{"-A", chain}, rule...)
		if err := t.runIPTables(ctx, name, args...); err != nil {
			return fmt.Errorf("configuring firewall chain %s in %s: %w", chain, name, err)
		}
	}
	for _, peerIPv4 := range peerIPv4s {
		if err := t.runIPTables(ctx, name, "-C", "INPUT", "-s", peerIPv4, "-j", chain); err != nil {
			if err := t.runIPTables(ctx, name, "-I", "INPUT", "1", "-s", peerIPv4, "-j", chain); err != nil {
				return fmt.Errorf("installing firewall jump %s in %s: %w", chain, name, err)
			}
		}
	}
	return nil
}

// validateFirewall proves that every jump and every rule of each VM chain is intact.
func (t *Topology) validateFirewall(ctx context.Context) error {
	machines, err := t.runningMachines(ctx)
	if err != nil {
		return err
	}
	for _, cluster := range t.config.Clusters() {
		chain := zoneChainName(cluster)
		for _, peerIPv4 := range t.peerIPv4s(machines, cluster.LogicalName) {
			if err := t.runIPTables(ctx, cluster.Name, "-C", "INPUT", "-s", peerIPv4, "-j", chain); err != nil {
				return fmt.Errorf("validating firewall jump %s in %s: %w", chain, cluster.Name, err)
			}
		}
		rules, err := zoneChainRules(cluster, machines)
		if err != nil {
			return err
		}
		for _, rule := range rules {
			args := append([]string{"-C", chain}, rule...)
			if err := t.runIPTables(ctx, cluster.Name, args...); err != nil {
				return fmt.Errorf("validating firewall chain %s in %s: %w", chain, cluster.Name, err)
			}
		}
	}
	return nil
}

func (t *Topology) runIPTables(ctx context.Context, name string, args ...string) error {
	command := append([]string{"iptables", "-w", "5"}, args...)
	return t.runMachineSudo(ctx, commandTimeout, name, command...)
}

// runMachineSudo executes one argv-only command as root inside the machine.
func (t *Topology) runMachineSudo(ctx context.Context, timeout time.Duration, name string, args ...string) error {
	_, err := t.runMachineSudoResult(ctx, timeout, name, args...)
	return err
}

// checkZoneIsolation proves the full mesh policy with a bounded probe matrix.
// Listeners are grouped per machine: each DMZ VM serves the tunnel port and the
// denied port once, each internal VM serves the tunnel port once. Allowed:
// same-DC internal → DMZ:30443. Denied: internal → DMZ:30444, DMZ →
// internal:30443, cross-DC internal → DMZ:30443 and DMZ ↔ DMZ:30443.
func (t *Topology) checkZoneIsolation(ctx context.Context) error {
	machines, err := t.runningMachines(ctx)
	if err != nil {
		return err
	}
	clusters := make(map[string]Cluster, len(t.config.Clusters()))
	names := make([]string, 0, len(machines))
	for _, cluster := range t.config.Clusters() {
		clusters[cluster.LogicalName] = cluster
		names = append(names, cluster.Name)
		if err := t.installProbe(ctx, machines[cluster.LogicalName]); err != nil {
			return err
		}
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), commandTimeout)
	defer cleanupCancel()
	defer t.stopProbes(cleanupCtx, names)

	for _, cluster := range t.config.Clusters() {
		ports := []int{AllowedProbePort}
		if cluster.Zone == "dmz" {
			ports = append(ports, DeniedProbePort)
		}
		for _, port := range ports {
			if err := t.startProbeServer(ctx, cluster.Name, port); err != nil {
				return err
			}
		}
	}

	type probeCase struct {
		from  string
		to    string
		port  int
		label string
	}
	allowed := []probeCase{
		{from: "dc-a-internal", to: "dc-a-dmz", port: AllowedProbePort, label: "dc-a internal to dmz"},
		{from: "dc-b-internal", to: "dc-b-dmz", port: AllowedProbePort, label: "dc-b internal to dmz"},
	}
	denied := []probeCase{
		{from: "dc-a-internal", to: "dc-a-dmz", port: DeniedProbePort, label: "dc-a internal to dmz non-tunnel port"},
		{from: "dc-b-internal", to: "dc-b-dmz", port: DeniedProbePort, label: "dc-b internal to dmz non-tunnel port"},
		{from: "dc-a-dmz", to: "dc-a-internal", port: AllowedProbePort, label: "dc-a dmz to internal"},
		{from: "dc-b-dmz", to: "dc-b-internal", port: AllowedProbePort, label: "dc-b dmz to internal"},
		{from: "dc-a-internal", to: "dc-b-dmz", port: AllowedProbePort, label: "cross-dc dc-a internal to dc-b dmz"},
		{from: "dc-b-internal", to: "dc-a-dmz", port: AllowedProbePort, label: "cross-dc dc-b internal to dc-a dmz"},
		{from: "dc-a-dmz", to: "dc-b-dmz", port: AllowedProbePort, label: "dmz dc-a to dmz dc-b"},
		{from: "dc-b-dmz", to: "dc-a-dmz", port: AllowedProbePort, label: "dmz dc-b to dmz dc-a"},
	}

	for _, probe := range allowed {
		target := machines[probe.to].IPv4
		if _, err := t.probeConnection(ctx, clusters[probe.from].Name, target, probe.port); err != nil {
			return fmt.Errorf("topology: allowed %s probe failed: %w", probe.label, err)
		}
	}
	for _, probe := range denied {
		target := machines[probe.to].IPv4
		result, err := t.probeConnection(ctx, clusters[probe.from].Name, target, probe.port)
		if err == nil {
			return fmt.Errorf("topology: forbidden %s probe was reachable", probe.label)
		}
		if !isRejectedProbe(result) {
			return fmt.Errorf("topology: %s negative probe failed unexpectedly: %w", probe.label, err)
		}
	}

	t.logger.InfoContext(ctx, "zone isolation verified", "allowed_port", AllowedProbePort)
	return nil
}

// installProbe copies the probe binary into the machine as a root-owned executable.
func (t *Topology) installProbe(ctx context.Context, machine orbMachine) error {
	if err := t.pushToMachine(ctx, machine.Name, t.config.ProbePath, "mm44-tcpprobe"); err != nil {
		return err
	}
	home := "/home/" + machine.Config.DefaultUsername
	if err := t.runMachineSudo(ctx, commandTimeout, machine.Name,
		"install", "-m", "0755", home+"/mm44-tcpprobe", probeVMPath); err != nil {
		return fmt.Errorf("installing tcp probe in %s: %w", machine.Name, err)
	}
	return nil
}

// startProbeServer launches the probe as a transient systemd unit and waits for readiness.
func (t *Topology) startProbeServer(ctx context.Context, name string, port int) error {
	unit := fmt.Sprintf("mm44-probe-%d", port)
	// Ignore stale transient units left by an interrupted previous run.
	_ = t.runMachineSudo(ctx, commandTimeout, name, "systemctl", "reset-failed", unit)
	if err := t.runMachineSudo(ctx, commandTimeout, name,
		"systemd-run", "--quiet", "--collect", "--unit", unit,
		probeVMPath, "serve", "--port", strconv.Itoa(port), "--lifetime", probeServeLifetime); err != nil {
		return fmt.Errorf("starting tcp probe in %s: %w", name, err)
	}

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	for {
		commandCtx, checkCancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := t.runner.Run(commandCtx, Command{
			Program: "orbctl",
			Args: []string{
				"run", "-m", name, "sudo", "-n",
				"test", "-f", fmt.Sprintf("%s/probe-%d.ready", probeVMRuntimeDir, port),
			},
		})
		checkCancel()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("topology: tcp probe did not become ready in %s", name)
		case <-poll.C:
		}
	}
}

func (t *Topology) probeConnection(ctx context.Context, source, address string, port int) (Result, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return t.runner.Run(commandCtx, Command{
		Program: "orbctl",
		Args: []string{
			"run", "-m", source,
			probeVMPath, "connect",
			"--address", net.JoinHostPort(address, strconv.Itoa(port)),
			"--timeout", "3s",
		},
	})
}

func isRejectedProbe(result Result) bool {
	return strings.Contains(result.Stderr, "tcpprobe: connection failed")
}

func (t *Topology) stopProbes(ctx context.Context, names []string) {
	for _, name := range names {
		for _, port := range []int{AllowedProbePort, DeniedProbePort} {
			_ = t.runMachineSudo(ctx, 5*time.Second, name,
				probeVMPath, "stop", "--port", strconv.Itoa(port))
		}
		_ = t.runMachineSudo(ctx, 5*time.Second, name, "rm", "-f", probeVMPath)
	}
}

func (t *Topology) cleanup(ctx context.Context) error {
	if err := t.orbStackReady(ctx); err != nil {
		return err
	}
	machines, err := t.listMachines(ctx)
	if err != nil {
		return err
	}
	machineNames := make(map[string]struct{}, len(machines))
	for _, machine := range machines {
		machineNames[machine.Name] = struct{}{}
	}

	var cleanupErrors []error
	for _, cluster := range t.config.Clusters() {
		if !t.config.ownsResource(cluster.Name) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("topology: refusing unexpected machine name %s", cluster.Name))
			continue
		}
		if _, exists := machineNames[cluster.Name]; exists {
			commandCtx, cancel := context.WithTimeout(ctx, createTimeout)
			_, deleteErr := t.runner.Run(commandCtx, Command{
				Program: "orbctl",
				Args:    []string{"delete", "--force", cluster.Name},
			})
			cancel()
			if deleteErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("deleting machine %s: %w", cluster.Name, deleteErr))
				continue
			}
		}
		if removeErr := os.Remove(cluster.Kubeconfig); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("removing kubeconfig for %s: %w", cluster.Name, removeErr))
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
