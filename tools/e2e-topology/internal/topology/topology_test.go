package topology

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestIsRejectedProbe(t *testing.T) {
	t.Parallel()

	if !isRejectedProbe(Result{Stderr: "tcpprobe: connection failed\n"}) {
		t.Fatal("isRejectedProbe() = false, want true for an executed rejected probe")
	}
	if isRejectedProbe(Result{Stderr: "machine not found\n"}) {
		t.Fatal("isRejectedProbe() = true for an unrelated orbctl failure")
	}
}

func TestValidateKubernetesIdentity(t *testing.T) {
	t.Parallel()

	labels := []struct {
		key   string
		value string
	}{
		{key: "marketmesh.dev/cluster", value: "dc-a-dmz"},
		{key: "marketmesh.dev/owner-task", value: TaskKey},
	}
	object := kubernetesObject{}
	object.Metadata.Name = "mm44-dc-a-dmz"
	object.Metadata.Labels = map[string]string{
		"marketmesh.dev/cluster":    "dc-a-dmz",
		"marketmesh.dev/owner-task": TaskKey,
	}
	if err := validateKubernetesIdentity(object, object.Metadata.Name, labels); err != nil {
		t.Fatalf("validateKubernetesIdentity() error = %v", err)
	}

	object.Metadata.Labels["marketmesh.dev/owner-task"] = "MM-999"
	if err := validateKubernetesIdentity(object, object.Metadata.Name, labels); err == nil {
		t.Fatal("validateKubernetesIdentity() error = nil, want label mismatch")
	}
}

func TestRemoveInventory(t *testing.T) {
	t.Parallel()

	config, err := NewConfig(t.TempDir(), "mm44-test")
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	if err := os.MkdirAll(config.StateDir, 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	inventoryPath := filepath.Join(config.StateDir, "inventory.json")
	if err := os.WriteFile(inventoryPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	manager := New(config, nil, nil)
	if err := manager.removeInventory(); err != nil {
		t.Fatalf("removeInventory() error = %v", err)
	}
	if _, err := os.Stat(inventoryPath); !os.IsNotExist(err) {
		t.Fatalf("inventory still exists: %v", err)
	}
	if err := manager.removeInventory(); err != nil {
		t.Fatalf("idempotent removeInventory() error = %v", err)
	}
}

func TestRewriteKubeconfig(t *testing.T) {
	t.Parallel()

	stock := `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: LS0t
    server: https://127.0.0.1:6443
  name: default
contexts:
- context:
    cluster: default
    user: default
  name: default
current-context: default
kind: Config
preferences: {}
users:
- name: default
  user:
    client-certificate-data: LS0t
    client-key-data: LS0t
`
	rewritten, err := rewriteKubeconfig([]byte(stock), "mm44-dc-a-dmz", "192.168.139.10")
	if err != nil {
		t.Fatalf("rewriteKubeconfig() error = %v", err)
	}
	contents := string(rewritten)
	for _, expected := range []string{
		"server: https://192.168.139.10:6443",
		"name: mm44-dc-a-dmz",
		"cluster: mm44-dc-a-dmz",
		"user: mm44-dc-a-dmz",
		"current-context: mm44-dc-a-dmz",
		"certificate-authority-data:",
	} {
		if !strings.Contains(contents, expected) {
			t.Errorf("rewritten kubeconfig does not contain %q:\n%s", expected, contents)
		}
	}
	if strings.Contains(contents, "127.0.0.1") || strings.Contains(contents, ": default") {
		t.Errorf("rewritten kubeconfig retains stock references:\n%s", contents)
	}
}

func TestRewriteKubeconfigFailClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
		ctx  string
		ip   string
	}{
		{
			name: "missing server",
			data: "apiVersion: v1\n  name: default\n  name: default\n  name: default\ncluster: default\nuser: default\ncurrent-context: default\ncertificate-authority-data: LS0t\n",
			ctx:  "mm44-dc-a-dmz",
			ip:   "192.168.139.10",
		},
		{
			name: "extra default name",
			data: "server: https://127.0.0.1:6443\nname: default\nname: default\nname: default\nname: default\ncluster: default\nuser: default\ncurrent-context: default\ncertificate-authority-data: LS0t\n",
			ctx:  "mm44-dc-a-dmz",
			ip:   "192.168.139.10",
		},
		{
			name: "invalid context",
			data: "server: https://127.0.0.1:6443\nname: default\nname: default\nname: default\ncluster: default\nuser: default\ncurrent-context: default\ncertificate-authority-data: LS0t\n",
			ctx:  "mm44;rm",
			ip:   "192.168.139.10",
		},
		{
			name: "invalid address",
			data: "server: https://127.0.0.1:6443\nname: default\nname: default\nname: default\ncluster: default\nuser: default\ncurrent-context: default\ncertificate-authority-data: LS0t\n",
			ctx:  "mm44-dc-a-dmz",
			ip:   "not-an-ip",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := rewriteKubeconfig([]byte(test.data), test.ctx, test.ip); err == nil {
				t.Fatal("rewriteKubeconfig() error = nil, want fail-closed rejection")
			}
		})
	}
}

func TestParseMachineDocument(t *testing.T) {
	t.Parallel()

	// Структура 1:1 повторяет живой вывод `orbctl info <name> -f json` v2.2.3.
	document := `{` +
		`"record":{"id":"01M1M7N61KT3MYNC8KGX55MSGN","name":"mm44-dc-a-dmz",` +
		`"image":{"distro":"ubuntu","version":"noble","arch":"arm64","variant":"default"},` +
		`"config":{"isolated":false,"forward_ssh_agent":true,"isolate_network":false,` +
		`"default_username":"vh","http_port":0,"https_port":0,"memory_limit_mib":2048,` +
		`"cpu_limit":2,"disk_limit_bytes":21474836480},"builtin":false,"state":"running"},` +
		`"disk_size":683962368,"ip4":"192.168.139.10",` +
		`"ip6":"fd07:b51a:cc66:0:ac8c:31ff:fe6b:b491"}`
	machine, err := parseMachineDocument(document, "mm44-dc-a-dmz")
	if err != nil {
		t.Fatalf("parseMachineDocument() error = %v", err)
	}
	if machine.ID != "01M1M7N61KT3MYNC8KGX55MSGN" || machine.IPv4 != "192.168.139.10" ||
		machine.State != "running" || machine.Config.DefaultUsername != "vh" ||
		machine.Image.Distro != "ubuntu" || machine.Image.Version != "noble" ||
		machine.Image.Arch != "arm64" {
		t.Errorf("machine document is incomplete: %+v", machine)
	}

	if _, err := parseMachineDocument(`{}`, "mm44-dc-a-dmz"); err == nil {
		t.Fatal("parseMachineDocument() error = nil, want empty document rejection")
	}
	if _, err := parseMachineDocument(document, "mm44-dc-b-dmz"); err == nil {
		t.Fatal("parseMachineDocument() error = nil, want name mismatch rejection")
	}
	if _, err := parseMachineDocument(`{not json`, "mm44-dc-a-dmz"); err == nil {
		t.Fatal("parseMachineDocument() error = nil, want invalid document rejection")
	}
}

func TestParseMachineList(t *testing.T) {
	t.Parallel()

	// Структура 1:1 повторяет живой вывод `orbctl list -f json` v2.2.3: плоские
	// записи без адресов.
	document := `[{"id":"01M1M7N61KT3MYNC8KGX55MSGN","name":"mm44-dc-a-dmz",` +
		`"image":{"distro":"ubuntu","version":"noble","arch":"arm64","variant":"default"},` +
		`"config":{"isolated":false,"default_username":"vh"},"builtin":false,"state":"running"}]`
	machines, err := parseMachineList(document)
	if err != nil {
		t.Fatalf("parseMachineList() error = %v", err)
	}
	if len(machines) != 1 || machines[0].Name != "mm44-dc-a-dmz" ||
		machines[0].ID != "01M1M7N61KT3MYNC8KGX55MSGN" || machines[0].State != "running" ||
		machines[0].Config.DefaultUsername != "vh" || machines[0].IPv4 != "" {
		t.Errorf("machine list is incomplete: %+v", machines)
	}

	empty, err := parseMachineList(`[]`)
	if err != nil || len(empty) != 0 {
		t.Errorf("parseMachineList(empty) = %+v, %v; want empty list", empty, err)
	}
	if _, err := parseMachineList(`{not json`); err == nil {
		t.Fatal("parseMachineList() error = nil, want invalid document rejection")
	}
}

func TestK3sUnitPinsServerFlags(t *testing.T) {
	t.Parallel()

	unit := k3sUnit("192.168.139.10")
	for _, expected := range []string{
		"Type=notify",
		"KillMode=process",
		"Delegate=yes",
		"LimitNOFILE=1048576",
		"LimitNPROC=infinity",
		"LimitCORE=infinity",
		"TasksMax=infinity",
		"Restart=always",
		"--disable=traefik",
		"--disable=servicelb",
		"--disable=metrics-server",
		"--write-kubeconfig-mode=0600",
		"--node-ip=192.168.139.10",
		"--tls-san=192.168.139.10",
	} {
		if !strings.Contains(unit, expected) {
			t.Errorf("k3s unit does not contain %q", expected)
		}
	}
}

func testMeshFixture(t *testing.T) (Config, map[string]orbMachine) {
	t.Helper()
	config, err := NewConfig("/workspace/marketmesh", "mm44")
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	addresses := map[string]string{
		"dc-a-dmz":      "192.168.139.10",
		"dc-a-internal": "192.168.139.11",
		"dc-b-dmz":      "192.168.139.12",
		"dc-b-internal": "192.168.139.13",
	}
	machines := make(map[string]orbMachine, len(addresses))
	for _, cluster := range config.Clusters() {
		machine := orbMachine{Name: cluster.Name, State: "running"}
		machine.IPv4 = addresses[cluster.LogicalName]
		machines[cluster.LogicalName] = machine
	}
	return config, machines
}

func TestZoneChainRulesMeshPolicy(t *testing.T) {
	t.Parallel()

	config, machines := testMeshFixture(t)
	for _, cluster := range config.Clusters() {
		rules, err := zoneChainRules(cluster, machines)
		if err != nil {
			t.Fatalf("zoneChainRules(%s) error = %v", cluster.LogicalName, err)
		}
		last := rules[len(rules)-1]
		if len(last) != 2 || last[0] != "-j" || last[1] != "DROP" {
			t.Errorf("zoneChainRules(%s) last rule = %v, want DROP", cluster.LogicalName, last)
		}
		first := rules[0]
		if !slices.Contains(first, "ESTABLISHED,RELATED") || !slices.Contains(first, "ACCEPT") {
			t.Errorf("zoneChainRules(%s) first rule = %v, want established accept", cluster.LogicalName, first)
		}

		accepts := 0
		for _, rule := range rules[1 : len(rules)-1] {
			if slices.Contains(rule, "ACCEPT") {
				accepts++
			}
		}
		if cluster.Zone == "dmz" {
			if len(rules) != 3 || accepts != 1 {
				t.Fatalf("zoneChainRules(%s) = %v, want exactly one tunnel accept", cluster.LogicalName, rules)
			}
			tunnel := rules[1]
			sameDCInternal := machines[cluster.DC+"-internal"].IPv4
			if !slices.Contains(tunnel, sameDCInternal) ||
				!slices.Contains(tunnel, strconv.Itoa(AllowedProbePort)) {
				t.Errorf("zoneChainRules(%s) tunnel rule = %v, want %s:%d",
					cluster.LogicalName, tunnel, sameDCInternal, AllowedProbePort)
			}
			for logical, machine := range machines {
				if logical != cluster.DC+"-internal" && slices.Contains(tunnel, machine.IPv4) {
					t.Errorf("zoneChainRules(%s) accepts foreign peer %s", cluster.LogicalName, logical)
				}
			}
		} else if len(rules) != 2 || accepts != 0 {
			t.Errorf("zoneChainRules(%s) = %v, want established accept + DROP only", cluster.LogicalName, rules)
		}
	}
}

func TestPeerIPv4sCoversMesh(t *testing.T) {
	t.Parallel()

	config, machines := testMeshFixture(t)
	manager := New(config, nil, nil)
	for _, cluster := range config.Clusters() {
		peers := manager.peerIPv4s(machines, cluster.LogicalName)
		if len(peers) != 3 {
			t.Fatalf("peerIPv4s(%s) = %v, want 3 peers", cluster.LogicalName, peers)
		}
		own := machines[cluster.LogicalName].IPv4
		if slices.Contains(peers, own) {
			t.Errorf("peerIPv4s(%s) contains own address %s", cluster.LogicalName, own)
		}
		seen := map[string]struct{}{}
		for _, peer := range peers {
			if _, exists := seen[peer]; exists {
				t.Errorf("peerIPv4s(%s) contains duplicate %s", cluster.LogicalName, peer)
			}
			seen[peer] = struct{}{}
		}
	}
}

func TestZoneChainName(t *testing.T) {
	t.Parallel()

	config, _ := testMeshFixture(t)
	for _, cluster := range config.Clusters() {
		name := zoneChainName(cluster)
		if cluster.Zone == "dmz" && name != dmzChainName {
			t.Errorf("zoneChainName(%s) = %q, want %q", cluster.LogicalName, name, dmzChainName)
		}
		if cluster.Zone == "internal" && name != internalChainName {
			t.Errorf("zoneChainName(%s) = %q, want %q", cluster.LogicalName, name, internalChainName)
		}
	}
}

func TestValidateIPTablesVersion(t *testing.T) {
	t.Parallel()

	valid := []string{
		"iptables v1.8.10 (nf_tables)\n",
		"iptables v1.8.10 (legacy)\n",
	}
	for _, output := range valid {
		if err := validateIPTablesVersion("mm44-dc-a-dmz", output); err != nil {
			t.Errorf("validateIPTablesVersion(%q) error = %v, want nil", output, err)
		}
	}

	invalid := []string{
		"iptables v1.8.9 (nf_tables)\n",
		"iptables v1.8.11 (nf_tables)\n",
		"nftables v1.0.0\n",
		"",
	}
	for _, output := range invalid {
		if err := validateIPTablesVersion("mm44-dc-a-dmz", output); err == nil {
			t.Errorf("validateIPTablesVersion(%q) error = nil, want rejection", output)
		}
	}
}

// firewallFakeRunner имитирует VM, в которой iptables появляется только после
// pinned apt install.
type firewallFakeRunner struct {
	installed bool
	commands  []Command
}

func (r *firewallFakeRunner) Run(_ context.Context, command Command) (Result, error) {
	r.commands = append(r.commands, command)
	if command.Program != "orbctl" || len(command.Args) < 6 ||
		command.Args[0] != "run" || command.Args[1] != "-m" ||
		command.Args[3] != "sudo" || command.Args[4] != "-n" {
		return Result{}, fmt.Errorf("unexpected command: %+v", command)
	}
	args := command.Args[5:]
	switch {
	case len(args) == 2 && args[0] == "iptables" && args[1] == "--version":
		if !r.installed {
			return Result{}, errors.New("sudo: iptables: command not found")
		}
		return Result{Stdout: "iptables v1.8.10 (nf_tables)\n"}, nil
	case len(args) == 4 && args[0] == "env" && args[2] == "apt-get" && args[3] == "update":
		return Result{}, nil
	case len(args) == 7 && args[0] == "env" && args[2] == "apt-get" && args[3] == "install":
		if args[6] != "iptables="+IPTablesVersion {
			return Result{}, fmt.Errorf("unexpected apt package pin: %v", args)
		}
		r.installed = true
		return Result{}, nil
	}
	return Result{}, fmt.Errorf("unexpected guest command: %v", args)
}

func TestEnsureFirewallToolchainInstallsPinnedPackage(t *testing.T) {
	t.Parallel()

	config, err := NewConfig("/workspace/marketmesh", "mm44")
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	cluster, err := config.Cluster("dc-a", "dmz")
	if err != nil {
		t.Fatalf("Cluster() error = %v", err)
	}
	runner := &firewallFakeRunner{}
	manager := New(config, runner, discardLogger())

	if err := manager.ensureFirewallToolchain(t.Context(), cluster); err != nil {
		t.Fatalf("ensureFirewallToolchain() error = %v", err)
	}
	if !runner.installed {
		t.Fatal("ensureFirewallToolchain() did not install the pinned package")
	}
	var sequences [][]string
	for _, command := range runner.commands {
		sequences = append(sequences, command.Args[5:])
	}
	want := [][]string{
		{"iptables", "--version"},
		{"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "update"},
		{"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y",
			"--no-install-recommends", "iptables=" + IPTablesVersion},
		{"iptables", "--version"},
	}
	if fmt.Sprint(sequences) != fmt.Sprint(want) {
		t.Errorf("command sequence = %v, want %v", sequences, want)
	}

	runner.commands = nil
	if err := manager.ensureFirewallToolchain(t.Context(), cluster); err != nil {
		t.Fatalf("idempotent ensureFirewallToolchain() error = %v", err)
	}
	if len(runner.commands) != 1 {
		t.Errorf("idempotent run executed %d commands, want a single version check", len(runner.commands))
	}
}

func TestEnsureFirewallToolchainRejectsUnexpectedVersion(t *testing.T) {
	t.Parallel()

	config, err := NewConfig("/workspace/marketmesh", "mm44")
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	cluster, err := config.Cluster("dc-a", "dmz")
	if err != nil {
		t.Fatalf("Cluster() error = %v", err)
	}
	manager := New(config, &staleVersionRunner{}, discardLogger())
	if err := manager.ensureFirewallToolchain(t.Context(), cluster); err == nil {
		t.Fatal("ensureFirewallToolchain() error = nil, want unexpected version rejection")
	}
}

// staleVersionRunner имитирует VM с уже установленным iptables не той версии.
type staleVersionRunner struct{}

func (staleVersionRunner) Run(_ context.Context, _ Command) (Result, error) {
	return Result{Stdout: "iptables v1.8.9 (nf_tables)\n"}, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
