package dcfailover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMM28TopologyRunsExactSymmetricContainerLifecycle(t *testing.T) {
	t.Parallel()

	config := testMM28TopologyConfig()
	commands := newFakeMM28Commands(config)
	topology := newTestMM28Topology(t, config, commands)
	snapshot, err := topology.Preflight(t.Context(), config.Instance)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	target, err := targetForDC(snapshot, DCA)
	if err != nil {
		t.Fatalf("targetForDC() error = %v", err)
	}

	if err := topology.StopDC(t.Context(), target, OutageSudden); err != nil {
		t.Fatalf("StopDC() error = %v", err)
	}
	assertRunningState(t, commands, target, false)
	if err := topology.RestoreDC(t.Context(), target); err != nil {
		t.Fatalf("RestoreDC() error = %v", err)
	}
	assertRunningState(t, commands, target, true)
	if err := topology.Inspect(t.Context(), snapshot); err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if err := topology.Cleanup(t.Context(), snapshot); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if err := topology.Cleanup(t.Context(), snapshot); err != nil {
		t.Fatalf("second Cleanup() error = %v", err)
	}

	wantMutations := []string{
		"container stop --time 30 mm35-test-dc-a-dmz-control-plane",
		"container stop --time 30 mm35-test-dc-a-internal-control-plane",
		"container start mm35-test-dc-a-dmz-control-plane",
		"container start mm35-test-dc-a-internal-control-plane",
	}
	if got := commands.dockerMutations(); !slices.Equal(got, wantMutations) {
		t.Fatalf("Docker mutations = %v, want %v", got, wantMutations)
	}
	wantActions := []string{"inventory", "ready", "inspect", "down"}
	if got := commands.mm28Actions(); !slices.Equal(got, wantActions) {
		t.Fatalf("MM-28 actions = %v, want %v", got, wantActions)
	}
	if commands.contextWithoutDeadline {
		t.Fatal("command received a context without a deadline")
	}
	for _, command := range commands.calls {
		if command.program != "docker" {
			continue
		}
		if slices.Equal(command.args, []string{
			"context", "inspect", config.DockerContext,
		}) {
			continue
		}
		if len(command.args) < 2 || command.args[0] != "--host" ||
			command.args[1] != commands.expectedDockerHost {
			t.Fatalf("Docker command does not use the pinned endpoint: %v", command.args)
		}
	}
}

func TestMM28TopologyRejectsUntrustedInventoryBeforeRuntimeCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*mm28Inventory)
	}{
		{
			name: "foreign task",
			mutate: func(inventory *mm28Inventory) {
				inventory.Task = "MM-999"
			},
		},
		{
			name: "foreign topology label",
			mutate: func(inventory *mm28Inventory) {
				inventory.Ownership.DockerLabels[mm28InstanceLabel] = "mm35-other"
			},
		},
		{
			name: "changed lifecycle command",
			mutate: func(inventory *mm28Inventory) {
				inventory.Commands.Down = "docker system prune"
			},
		},
		{
			name: "reordered clusters",
			mutate: func(inventory *mm28Inventory) {
				inventory.Clusters[0], inventory.Clusters[1] =
					inventory.Clusters[1], inventory.Clusters[0]
			},
		},
		{
			name: "ambient kube context",
			mutate: func(inventory *mm28Inventory) {
				inventory.Clusters[0].Context = "current-context"
			},
		},
		{
			name: "unexpected kubeconfig",
			mutate: func(inventory *mm28Inventory) {
				inventory.Clusters[0].Kubeconfig = "/tmp/foreign.yaml"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := testMM28TopologyConfig()
			commands := newFakeMM28Commands(config)
			tt.mutate(&commands.inventory)
			topology := newTestMM28Topology(t, config, commands)
			if _, err := topology.Preflight(t.Context(), config.Instance); err == nil {
				t.Fatal("Preflight() error = nil, want fail-closed rejection")
			}
			if got := commands.mm28Actions(); !slices.Equal(got, []string{"inventory"}) {
				t.Fatalf("MM-28 actions = %v, want inventory only", got)
			}
			if got := commands.dockerMutations(); len(got) != 0 {
				t.Fatalf("Docker mutations = %v, want none", got)
			}
			if commands.hasRuntimeDockerInspection() {
				t.Fatal("untrusted inventory reached runtime inspection")
			}
		})
	}
}

func TestMM28TopologyRejectsForeignRuntimeOwnershipBeforeAuthorization(t *testing.T) {
	t.Parallel()

	config := testMM28TopologyConfig()
	commands := newFakeMM28Commands(config)
	commands.networkOwned[config.Instance+"-dc-a-dmz"] = false
	topology := newTestMM28Topology(t, config, commands)
	_, err := topology.Preflight(t.Context(), config.Instance)
	if err == nil || !strings.Contains(err.Error(), "refusing unowned network") {
		t.Fatalf("Preflight() error = %v, want unowned network rejection", err)
	}
	if got := commands.dockerMutations(); len(got) != 0 {
		t.Fatalf("Docker mutations = %v, want none", got)
	}
	if slices.Contains(commands.mm28Actions(), "ready") {
		t.Fatal("ready ran after runtime ownership rejection")
	}
}

func TestMM28TopologyRejectsRemoteDockerContextBeforeRuntimeInspection(t *testing.T) {
	t.Parallel()

	config := testMM28TopologyConfig()
	commands := newFakeMM28Commands(config)
	commands.dockerContextHost = "ssh://operator@production.example"
	topology := newTestMM28Topology(t, config, commands)
	_, err := topology.Preflight(t.Context(), config.Instance)
	if err == nil || !strings.Contains(err.Error(), "local absolute unix socket") {
		t.Fatalf("Preflight() error = %v, want remote context rejection", err)
	}
	if got := commands.dockerMutations(); len(got) != 0 {
		t.Fatalf("Docker mutations = %v, want none", got)
	}
	if got := commands.mm28Actions(); len(got) != 0 {
		t.Fatalf("MM-28 actions = %v, want none", got)
	}
	if slices.Contains(commands.mm28Actions(), "ready") {
		t.Fatal("ready ran after remote Docker context rejection")
	}
}

func TestMM28TopologyRejectsRetargetedDockerContextBeforeMutation(t *testing.T) {
	t.Parallel()

	config := testMM28TopologyConfig()
	commands := newFakeMM28Commands(config)
	topology := newTestMM28Topology(t, config, commands)
	snapshot, err := topology.Preflight(t.Context(), config.Instance)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	target, err := targetForDC(snapshot, DCA)
	if err != nil {
		t.Fatalf("targetForDC() error = %v", err)
	}
	commands.dockerContextHost = "unix:///tmp/retargeted-docker.sock"

	err = topology.StopDC(t.Context(), target, OutageSudden)
	if err == nil || !strings.Contains(err.Error(), "endpoint changed") {
		t.Fatalf("StopDC() error = %v, want retargeted context rejection", err)
	}
	if got := commands.dockerMutations(); len(got) != 0 {
		t.Fatalf("Docker mutations = %v, want none", got)
	}
}

func TestMM28TopologyRevalidatesWholeDCBeforeFirstStop(t *testing.T) {
	t.Parallel()

	config := testMM28TopologyConfig()
	commands := newFakeMM28Commands(config)
	topology := newTestMM28Topology(t, config, commands)
	snapshot, err := topology.Preflight(t.Context(), config.Instance)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	target, err := targetForDC(snapshot, DCA)
	if err != nil {
		t.Fatalf("targetForDC() error = %v", err)
	}
	commands.networkOwned[config.Instance+"-dc-a-internal"] = false

	err = topology.StopDC(t.Context(), target, OutageManaged)
	if err == nil || !strings.Contains(err.Error(), "refusing unowned network") {
		t.Fatalf("StopDC() error = %v, want revalidation error", err)
	}
	if got := commands.dockerMutations(); len(got) != 0 {
		t.Fatalf("Docker mutations = %v, want none", got)
	}
}

func TestMM28TopologyRevalidatesImmediatelyBeforeContainerStop(t *testing.T) {
	t.Parallel()

	config := testMM28TopologyConfig()
	commands := newFakeMM28Commands(config)
	topology := newTestMM28Topology(t, config, commands)
	snapshot, err := topology.Preflight(t.Context(), config.Instance)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	target, err := targetForDC(snapshot, DCA)
	if err != nil {
		t.Fatalf("targetForDC() error = %v", err)
	}
	commands.foreignNetworkAtInspection[config.Instance+"-dc-a-dmz"] = 3

	err = topology.StopDC(t.Context(), target, OutageSudden)
	if err == nil || !strings.Contains(err.Error(), "revalidating exact container") {
		t.Fatalf("StopDC() error = %v, want immediate revalidation error", err)
	}
	if got := commands.dockerMutations(); len(got) != 0 {
		t.Fatalf("Docker mutations = %v, want none", got)
	}
}

func TestMM28TopologyRejectsChangedAuthorizedTarget(t *testing.T) {
	t.Parallel()

	config := testMM28TopologyConfig()
	commands := newFakeMM28Commands(config)
	topology := newTestMM28Topology(t, config, commands)
	snapshot, err := topology.Preflight(t.Context(), config.Instance)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	target, err := targetForDC(snapshot, DCA)
	if err != nil {
		t.Fatalf("targetForDC() error = %v", err)
	}
	target.Clusters[0].ContainerNames[0] = "foreign-control-plane"

	err = topology.StopDC(t.Context(), target, OutageSudden)
	if err == nil || !strings.Contains(err.Error(), "differs from authorized") {
		t.Fatalf("StopDC() error = %v, want authorized target rejection", err)
	}
	if commands.hasDockerInspectionAfterReady() {
		t.Fatal("changed target reached post-preflight Docker inspection")
	}
}

func TestMM28TopologyCleanupRestoresInterruptedOutageBeforeDown(t *testing.T) {
	t.Parallel()

	config := testMM28TopologyConfig()
	commands := newFakeMM28Commands(config)
	topology := newTestMM28Topology(t, config, commands)
	snapshot, err := topology.Preflight(t.Context(), config.Instance)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	target, err := targetForDC(snapshot, DCB)
	if err != nil {
		t.Fatalf("targetForDC() error = %v", err)
	}
	if err := topology.StopDC(t.Context(), target, OutageSudden); err != nil {
		t.Fatalf("StopDC() error = %v", err)
	}

	if err := topology.Cleanup(t.Context(), snapshot); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	mutations := commands.dockerMutations()
	wantSuffix := []string{
		"container start mm35-test-dc-b-dmz-control-plane",
		"container start mm35-test-dc-b-internal-control-plane",
	}
	if !slices.Equal(mutations[len(mutations)-len(wantSuffix):], wantSuffix) {
		t.Fatalf("Docker mutation suffix = %v, want %v", mutations, wantSuffix)
	}
	if got := commands.mm28Actions(); got[len(got)-1] != "down" {
		t.Fatalf("last MM-28 action = %v, want down", got)
	}
}

func TestNewMM28TopologyRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()

	valid := testMM28TopologyConfig()
	tests := []struct {
		name   string
		mutate func(*MM28TopologyConfig)
	}{
		{
			name: "relative repository",
			mutate: func(config *MM28TopologyConfig) {
				config.RepositoryRoot = "marketmesh"
			},
		},
		{
			name: "filesystem root",
			mutate: func(config *MM28TopologyConfig) {
				config.RepositoryRoot = string(filepath.Separator)
			},
		},
		{
			name: "non task instance",
			mutate: func(config *MM28TopologyConfig) {
				config.Instance = "mm28"
			},
		},
		{
			name: "glob instance",
			mutate: func(config *MM28TopologyConfig) {
				config.Instance = "mm35-*"
			},
		},
		{
			name: "ambient docker context",
			mutate: func(config *MM28TopologyConfig) {
				config.DockerContext = ""
			},
		},
		{
			name: "production docker context",
			mutate: func(config *MM28TopologyConfig) {
				config.DockerContext = "marketmesh-production"
			},
		},
		{
			name: "unbounded command timeout",
			mutate: func(config *MM28TopologyConfig) {
				config.CommandTimeout = 31 * time.Minute
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := valid
			tt.mutate(&config)
			if _, err := NewMM28Topology(config); err == nil {
				t.Fatal("NewMM28Topology() error = nil, want validation error")
			}
		})
	}
}

func TestNewMM28TopologyRequiresExactRepositoryFiles(t *testing.T) {
	t.Parallel()

	config := testMM28TopologyConfig()
	config.RepositoryRoot = t.TempDir()
	if _, err := NewMM28Topology(config); err == nil {
		t.Fatal("NewMM28Topology() error = nil, want missing MM-28 tool rejection")
	}

	for _, relativePath := range []string{
		"Taskfile.yml",
		filepath.Join("tools", "e2e-topology", "go.mod"),
		filepath.Join("tools", "e2e-topology", "main.go"),
	} {
		path := filepath.Join(config.RepositoryRoot, relativePath)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(path, []byte("test\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}
	if _, err := NewMM28Topology(config); err != nil {
		t.Fatalf("NewMM28Topology() error = %v", err)
	}
}

func TestDecodeMM28InventoryRejectsTrailingDocument(t *testing.T) {
	t.Parallel()

	if _, err := decodeMM28Inventory([]byte(`{} {}`)); err == nil {
		t.Fatal("decodeMM28Inventory() error = nil, want trailing JSON rejection")
	}
}

func TestExecTopologyCommandRunnerKillsDescendantsOnTimeout(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	directory := t.TempDir()
	goPath := filepath.Join(directory, "go")
	if err := os.Symlink(executable, goPath); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	marker := filepath.Join(directory, "descendant-finished")
	readyMarker := filepath.Join(directory, "descendant-started")
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MM35_PROCESS_HELPER", "parent")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, runErr := (execTopologyCommandRunner{}).Run(ctx, topologyCommand{
			program: "go",
			args: []string{
				"-test.run=^TestMM35ProcessGroupHelper$",
				"--",
				marker,
				readyMarker,
			},
		})
		result <- runErr
	}()

	waitForFile(t, readyMarker, 5*time.Second)
	cancel()
	select {
	case err = <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after process-group cancellation")
	}

	timer := time.NewTimer(750 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-t.Context().Done():
		t.Fatalf("test context ended while checking descendant: %v", context.Cause(t.Context()))
	case <-timer.C:
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descendant survived command timeout: stat error = %v", err)
	}
}

func TestMM35ProcessGroupHelper(t *testing.T) {
	mode := os.Getenv("MM35_PROCESS_HELPER")
	if mode == "" {
		return
	}
	arguments := argumentsAfterDoubleDash(os.Args)
	if len(arguments) != 2 {
		os.Exit(2)
	}
	marker := arguments[0]
	readyMarker := arguments[1]
	if mode == "child" {
		if err := os.WriteFile(readyMarker, []byte("started\n"), 0o600); err != nil {
			os.Exit(3)
		}
		time.Sleep(500 * time.Millisecond)
		if err := os.WriteFile(marker, []byte("survived\n"), 0o600); err != nil {
			os.Exit(4)
		}
		os.Exit(0)
	}

	process := exec.Command(
		os.Args[0],
		"-test.run=^TestMM35ProcessGroupHelper$",
		"--",
		marker,
		readyMarker,
	)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "MM35_PROCESS_HELPER=") {
			process.Env = append(process.Env, value)
		}
	}
	process.Env = append(process.Env, "MM35_PROCESS_HELPER=child")
	if err := process.Start(); err != nil {
		os.Exit(5)
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func argumentsAfterDoubleDash(arguments []string) []string {
	for index, argument := range arguments {
		if argument == "--" && index+1 < len(arguments) {
			return arguments[index+1:]
		}
	}

	return nil
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Stat() error = %v", err)
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("timed out waiting for helper marker %s", filepath.Base(path))
		case <-t.Context().Done():
			t.Fatalf("test context ended waiting for helper: %v", context.Cause(t.Context()))
		}
	}
}

func newTestMM28Topology(
	t *testing.T,
	config MM28TopologyConfig,
	commands topologyCommandRunner,
) *MM28Topology {
	t.Helper()

	topology, err := newMM28Topology(config, commands)
	if err != nil {
		t.Fatalf("newMM28Topology() error = %v", err)
	}

	return topology
}

func testMM28TopologyConfig() MM28TopologyConfig {
	return MM28TopologyConfig{
		RepositoryRoot: "/workspace/marketmesh",
		Instance:       "mm35-test",
		DockerContext:  "orbstack",
		CommandTimeout: time.Minute,
	}
}

type fakeMM28Commands struct {
	config                     MM28TopologyConfig
	inventory                  mm28Inventory
	running                    map[string]bool
	networkOwned               map[string]bool
	networkInspections         map[string]int
	foreignNetworkAtInspection map[string]int
	dockerContextHost          string
	expectedDockerHost         string
	calls                      []topologyCommand
	readyCallIndex             int
	contextWithoutDeadline     bool
}

func newFakeMM28Commands(config MM28TopologyConfig) *fakeMM28Commands {
	commands := &fakeMM28Commands{
		config:                     config,
		running:                    map[string]bool{},
		networkOwned:               map[string]bool{},
		networkInspections:         map[string]int{},
		foreignNetworkAtInspection: map[string]int{},
		dockerContextHost:          "unix:///Users/test/.orbstack/run/docker.sock",
		expectedDockerHost:         "unix:///Users/test/.orbstack/run/docker.sock",
		readyCallIndex:             -1,
	}
	commands.inventory = testMM28Inventory(config)
	for _, cluster := range mm28ExpectedClusters(config) {
		commands.running[cluster.containerName] = true
		commands.networkOwned[cluster.networkName] = true
	}

	return commands
}

func (commands *fakeMM28Commands) Run(
	ctx context.Context,
	command topologyCommand,
) (topologyCommandResult, error) {
	if _, ok := ctx.Deadline(); !ok {
		commands.contextWithoutDeadline = true
	}
	commands.calls = append(commands.calls, topologyCommand{
		program: command.program,
		args:    slices.Clone(command.args),
		dir:     command.dir,
	})

	switch command.program {
	case "go":
		return commands.runGo(command)
	case "docker":
		return commands.runDocker(command)
	default:
		return topologyCommandResult{}, errors.New("unexpected program")
	}
}

func (commands *fakeMM28Commands) runGo(command topologyCommand) (topologyCommandResult, error) {
	if command.dir != commands.config.RepositoryRoot || len(command.args) != 7 ||
		!slices.Equal(command.args[:6], []string{
			"run",
			"./tools/e2e-topology",
			"--instance",
			commands.config.Instance,
			"--docker-context",
			commands.config.DockerContext,
		}) {
		return topologyCommandResult{}, fmt.Errorf("unexpected MM-28 command: %v", command.args)
	}
	action := command.args[6]
	if action == "ready" {
		commands.readyCallIndex = len(commands.calls) - 1
	}
	if action != "inventory" {
		return topologyCommandResult{}, nil
	}
	content, err := json.Marshal(commands.inventory)
	if err != nil {
		return topologyCommandResult{}, err
	}

	return topologyCommandResult{stdout: content}, nil
}

func (commands *fakeMM28Commands) runDocker(
	command topologyCommand,
) (topologyCommandResult, error) {
	if len(command.args) == 3 && command.args[0] == "context" &&
		command.args[1] == "inspect" {
		return commands.inspectContext(command.args[2])
	}
	if len(command.args) < 4 || command.args[0] != "--host" ||
		command.args[1] != commands.expectedDockerHost {
		return topologyCommandResult{}, fmt.Errorf("unexpected Docker command: %v", command.args)
	}
	args := command.args[2:]
	switch {
	case len(args) == 3 && args[0] == "container" && args[1] == "inspect":
		return commands.inspectContainer(args[2])
	case len(args) == 3 && args[0] == "network" && args[1] == "inspect":
		return commands.inspectNetwork(args[2])
	case len(args) == 5 && args[0] == "container" && args[1] == "stop" &&
		args[2] == "--time" && args[3] == "30":
		if _, exists := commands.running[args[4]]; !exists {
			return topologyCommandResult{}, errors.New("unknown container")
		}
		commands.running[args[4]] = false
		return topologyCommandResult{}, nil
	case len(args) == 3 && args[0] == "container" && args[1] == "start":
		if _, exists := commands.running[args[2]]; !exists {
			return topologyCommandResult{}, errors.New("unknown container")
		}
		commands.running[args[2]] = true
		return topologyCommandResult{}, nil
	default:
		return topologyCommandResult{}, fmt.Errorf("unexpected Docker args: %v", args)
	}
}

func (commands *fakeMM28Commands) inspectContext(
	name string,
) (topologyCommandResult, error) {
	if name != commands.config.DockerContext {
		return topologyCommandResult{}, errors.New("unknown Docker context")
	}
	inspection := dockerContextInspection{Name: name}
	inspection.Endpoints = map[string]struct {
		Host string `json:"Host"`
	}{
		"docker": {Host: commands.dockerContextHost},
	}
	content, err := json.Marshal([]dockerContextInspection{inspection})
	if err != nil {
		return topologyCommandResult{}, err
	}

	return topologyCommandResult{stdout: content}, nil
}

func (commands *fakeMM28Commands) inspectContainer(
	name string,
) (topologyCommandResult, error) {
	cluster, found := commands.clusterByContainer(name)
	if !found {
		return topologyCommandResult{}, errors.New("unknown container")
	}
	inspection := dockerContainerInspection{Name: "/" + name}
	inspection.Config.Labels = map[string]string{mm28ClusterLabel: cluster.resourceName}
	inspection.State.Running = commands.running[name]
	inspection.HostConfig.NetworkMode = cluster.networkName
	inspection.NetworkSettings.Networks = map[string]json.RawMessage{
		cluster.networkName: json.RawMessage(`{}`),
	}
	content, err := json.Marshal([]dockerContainerInspection{inspection})
	if err != nil {
		return topologyCommandResult{}, err
	}

	return topologyCommandResult{stdout: content}, nil
}

func (commands *fakeMM28Commands) inspectNetwork(
	name string,
) (topologyCommandResult, error) {
	cluster, found := commands.clusterByNetwork(name)
	if !found {
		return topologyCommandResult{}, errors.New("unknown network")
	}
	inspection := dockerNetworkInspection{
		Name: name,
		Labels: map[string]string{
			mm28OwnerLabel:    mm28TaskKey,
			mm28InstanceLabel: commands.config.Instance,
		},
		Containers: map[string]struct {
			Name string `json:"Name"`
		}{},
	}
	if commands.running[cluster.containerName] {
		inspection.Containers["container-id"] = struct {
			Name string `json:"Name"`
		}{Name: cluster.containerName}
	}
	commands.networkInspections[name]++
	if !commands.networkOwned[name] ||
		commands.foreignNetworkAtInspection[name] == commands.networkInspections[name] {
		inspection.Labels[mm28InstanceLabel] = "mm35-foreign"
	}
	content, err := json.Marshal([]dockerNetworkInspection{inspection})
	if err != nil {
		return topologyCommandResult{}, err
	}

	return topologyCommandResult{stdout: content}, nil
}

func (commands *fakeMM28Commands) clusterByContainer(
	name string,
) (expectedMM28Cluster, bool) {
	for _, cluster := range mm28ExpectedClusters(commands.config) {
		if cluster.containerName == name {
			return cluster, true
		}
	}

	return expectedMM28Cluster{}, false
}

func (commands *fakeMM28Commands) clusterByNetwork(
	name string,
) (expectedMM28Cluster, bool) {
	for _, cluster := range mm28ExpectedClusters(commands.config) {
		if cluster.networkName == name {
			return cluster, true
		}
	}

	return expectedMM28Cluster{}, false
}

func (commands *fakeMM28Commands) mm28Actions() []string {
	var actions []string
	for _, command := range commands.calls {
		if command.program == "go" {
			actions = append(actions, command.args[len(command.args)-1])
		}
	}

	return actions
}

func (commands *fakeMM28Commands) dockerMutations() []string {
	var mutations []string
	for _, command := range commands.calls {
		if command.program != "docker" || len(command.args) < 5 {
			continue
		}
		args := command.args[2:]
		if args[0] == "container" && (args[1] == "stop" || args[1] == "start") {
			mutations = append(mutations, strings.Join(args, " "))
		}
	}

	return mutations
}

func (commands *fakeMM28Commands) hasRuntimeDockerInspection() bool {
	for _, command := range commands.calls {
		if command.program == "docker" && command.args[0] != "context" {
			return true
		}
	}

	return false
}

func (commands *fakeMM28Commands) hasDockerInspectionAfterReady() bool {
	if commands.readyCallIndex < 0 {
		return false
	}
	for _, command := range commands.calls[commands.readyCallIndex+1:] {
		if command.program == "docker" {
			return true
		}
	}

	return false
}

func testMM28Inventory(config MM28TopologyConfig) mm28Inventory {
	prefix := fmt.Sprintf(
		"go run ./tools/e2e-topology --instance %s --docker-context %s",
		config.Instance,
		config.DockerContext,
	)
	inventory := mm28Inventory{
		APIVersion:    mm28InventoryAPIVersion,
		Task:          mm28TaskKey,
		Instance:      config.Instance,
		DockerContext: config.DockerContext,
		Namespace:     mm28Namespace,
		TunnelPort:    mm28TunnelPort,
		Ownership: mm28InventoryOwner{
			DockerLabels: map[string]string{
				mm28OwnerLabel:    mm28TaskKey,
				mm28InstanceLabel: config.Instance,
			},
			KubernetesLabels: map[string]string{
				"marketmesh.dev/owner-task":        mm28TaskKey,
				"marketmesh.dev/topology-instance": config.Instance,
			},
		},
		Commands: mm28InventoryCommand{
			Ready:   prefix + " ready",
			Inspect: prefix + " inspect",
			Down:    prefix + " down",
		},
	}
	addresses := []string{"172.28.10.2", "172.28.11.2", "172.28.20.2", "172.28.21.2"}
	for index, cluster := range mm28ExpectedClusters(config) {
		inventory.Clusters = append(inventory.Clusters, mm28InventoryEntry{
			LogicalName:            cluster.logicalName,
			ResourceName:           cluster.resourceName,
			DC:                     string(cluster.dc),
			Zone:                   string(cluster.zone),
			NetworkName:            cluster.networkName,
			ControlPlaneAddress:    addresses[index],
			Kubeconfig:             cluster.kubeconfig,
			Context:                cluster.kubeContext,
			Namespace:              mm28Namespace,
			WorkloadIdentityFormat: "<pod>/<namespace>/<logical-cluster>",
		})
	}

	return inventory
}

func assertRunningState(
	t *testing.T,
	commands *fakeMM28Commands,
	target DCTarget,
	want bool,
) {
	t.Helper()

	for _, cluster := range target.Clusters {
		if got := commands.running[cluster.ContainerNames[0]]; got != want {
			t.Errorf("container %s running = %t, want %t", cluster.ContainerNames[0], got, want)
		}
	}
}
