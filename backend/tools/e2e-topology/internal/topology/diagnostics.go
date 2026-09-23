package topology

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type diagnosticsSummary struct {
	Task      string               `json:"task"`
	Instance  string               `json:"instance"`
	Runtime   string               `json:"runtime"`
	CreatedAt string               `json:"created_at"`
	Clusters  []diagnosticsCluster `json:"clusters"`
}

type diagnosticsCluster struct {
	LogicalName   string `json:"logical_name"`
	ResourceName  string `json:"resource_name"`
	DC            string `json:"dc"`
	Zone          string `json:"zone"`
	MachineExists bool   `json:"machine_exists"`
	State         string `json:"state,omitempty"`
}

// Inspect records bounded, non-secret diagnostics in repository-local state.
// Collection is best-effort per artifact: a failed capture is recorded in a
// neighboring <name>.err file and never cancels the remaining artifacts, so
// diagnostics survive partially created machines. Inspect returns an error
// only when the diagnostics run itself cannot be created or summarized.
func (t *Topology) Inspect(ctx context.Context) error {
	if err := os.MkdirAll(t.config.DiagnosticsDir, 0o750); err != nil {
		return fmt.Errorf("creating diagnostics directory: %w", err)
	}
	directory := filepath.Join(
		t.config.DiagnosticsDir,
		t.now().UTC().Format("20060102T150405.000000000Z"),
	)
	if err := os.Mkdir(directory, 0o750); err != nil {
		return fmt.Errorf("creating diagnostics run directory: %w", err)
	}

	summary := diagnosticsSummary{
		Task:      TaskKey,
		Instance:  t.config.Instance,
		Runtime:   RuntimeName,
		CreatedAt: t.now().UTC().Format("2006-01-02T15:04:05.000000000Z"),
		Clusters:  []diagnosticsCluster{},
	}

	machineIndex := map[string]orbMachine{}
	machines, err := t.listMachines(ctx)
	if err != nil {
		t.recordArtifactError(directory, "orbctl-list", err)
	} else {
		encoded, err := json.MarshalIndent(machines, "", "  ")
		if err != nil {
			t.recordArtifactError(directory, "orbctl-list", fmt.Errorf("encoding machine list: %w", err))
		} else if err := writePrivateFile(filepath.Join(directory, "orbctl-list.json"), append(encoded, '\n')); err != nil {
			t.recordArtifactError(directory, "orbctl-list", err)
		}
		for _, machine := range machines {
			machineIndex[machine.Name] = machine
		}
	}

	for _, cluster := range t.config.Clusters() {
		machine, machineExists := machineIndex[cluster.Name]
		entry := diagnosticsCluster{
			LogicalName:   cluster.LogicalName,
			ResourceName:  cluster.Name,
			DC:            cluster.DC,
			Zone:          cluster.Zone,
			MachineExists: machineExists,
		}
		if machineExists {
			entry.State = machine.State
		}
		summary.Clusters = append(summary.Clusters, entry)

		clusterDirectory := filepath.Join(directory, cluster.LogicalName)
		if err := os.Mkdir(clusterDirectory, 0o750); err != nil {
			t.recordArtifactError(directory, cluster.LogicalName,
				fmt.Errorf("creating diagnostics for %s: %w", cluster.Name, err))
			continue
		}
		if !machineExists {
			continue
		}
		t.captureCommand(ctx, clusterDirectory, "machine-info.json", Command{
			Program: "orbctl",
			Args:    []string{"info", cluster.Name, "--format", "json"},
		})
		if machine.State != "running" {
			continue
		}

		guestCommands := []struct {
			file string
			args []string
		}{
			{file: "k3s-journal.txt", args: []string{"journalctl", "-u", "k3s", "--no-pager", "-n", "200"}},
			{file: "iptables.txt", args: []string{"iptables", "-L", "-n", "-v"}},
		}
		for _, item := range guestCommands {
			commandArgs := append([]string{"run", "-m", cluster.Name, "sudo", "-n"}, item.args...)
			t.captureCommand(ctx, clusterDirectory, item.file, Command{
				Program: "orbctl",
				Args:    commandArgs,
			})
		}

		if _, statErr := os.Stat(cluster.Kubeconfig); statErr != nil {
			if !errors.Is(statErr, os.ErrNotExist) {
				t.recordArtifactError(clusterDirectory, "kubeconfig-stat", statErr)
			}
			continue
		}
		kubectlCommands := []struct {
			file string
			args []string
		}{
			{file: "nodes.json", args: []string{"get", "nodes", "-o", "wide"}},
			{file: "namespace.json", args: []string{"get", "namespace", Namespace, "-o", "json"}},
			{
				file: "events.txt",
				args: []string{
					"get",
					"events",
					"--all-namespaces",
					"--sort-by=.metadata.creationTimestamp",
					"--chunk-size=200",
				},
			},
		}
		for _, item := range kubectlCommands {
			result, captureErr := t.runKubectl(ctx, diagnosticsTimeout, cluster, item.args...)
			if captureErr != nil {
				t.recordArtifactError(clusterDirectory, item.file,
					fmt.Errorf("collecting %s for %s: %w", item.file, cluster.Name, captureErr))
				continue
			}
			if writeErr := writePrivateFile(
				filepath.Join(clusterDirectory, item.file),
				[]byte(result.Stdout+result.Stderr),
			); writeErr != nil {
				t.recordArtifactError(clusterDirectory, item.file, writeErr)
			}
		}
	}

	summaryBytes, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding diagnostics summary: %w", err)
	}
	if err := writePrivateFile(filepath.Join(directory, "summary.json"), append(summaryBytes, '\n')); err != nil {
		return fmt.Errorf("writing diagnostics summary: %w", err)
	}

	t.logger.InfoContext(ctx, "topology diagnostics collected", "directory", directory)
	return nil
}

// captureCommand records one bounded command output, or a <name>.err artifact.
func (t *Topology) captureCommand(
	ctx context.Context,
	directory string,
	filename string,
	command Command,
) {
	commandCtx, cancel := context.WithTimeout(ctx, diagnosticsTimeout)
	defer cancel()
	result, err := t.runner.Run(commandCtx, command)
	if err != nil {
		t.recordArtifactError(directory, filename, fmt.Errorf("collecting %s: %w", filename, err))
		return
	}
	contents := result.Stdout
	if result.Stderr != "" {
		contents += "\n[stderr]\n" + result.Stderr
	}
	if err := writePrivateFile(filepath.Join(directory, filename), []byte(contents)); err != nil {
		t.recordArtifactError(directory, filename, fmt.Errorf("writing %s: %w", filename, err))
	}
}

// recordArtifactError persists a per-artifact failure next to the artifact.
func (t *Topology) recordArtifactError(directory, name string, artifactErr error) {
	path := filepath.Join(directory, name+".err")
	if err := writePrivateFile(path, []byte(artifactErr.Error()+"\n")); err != nil && t.logger != nil {
		t.logger.WarnContext(
			context.Background(),
			"recording diagnostics artifact error failed",
			"artifact", name,
			"error", artifactErr,
		)
	}
}
