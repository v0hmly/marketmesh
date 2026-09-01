package topology

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

type diagnosticsSummary struct {
	Task          string               `json:"task"`
	Instance      string               `json:"instance"`
	DockerContext string               `json:"docker_context"`
	CreatedAt     string               `json:"created_at"`
	Clusters      []diagnosticsCluster `json:"clusters"`
}

type diagnosticsCluster struct {
	LogicalName   string `json:"logical_name"`
	ResourceName  string `json:"resource_name"`
	DC            string `json:"dc"`
	Zone          string `json:"zone"`
	ClusterExists bool   `json:"cluster_exists"`
	NetworkExists bool   `json:"network_exists"`
}

// Inspect records bounded, non-secret diagnostics in repository-local state.
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
		Task:          TaskKey,
		Instance:      t.config.Instance,
		DockerContext: t.config.DockerContext,
		CreatedAt:     t.now().UTC().Format("2006-01-02T15:04:05.000000000Z"),
		Clusters:      []diagnosticsCluster{},
	}
	var diagnosticsErrors []error

	if err := t.captureCommand(
		ctx,
		directory,
		"docker-info.txt",
		t.dockerCommand("info", "--format", safeDockerInfoFormat),
	); err != nil {
		diagnosticsErrors = append(diagnosticsErrors, err)
	}

	clusters, err := t.kindClusters(ctx)
	if err != nil {
		diagnosticsErrors = append(diagnosticsErrors, err)
		clusters = []string{}
	}

	for _, cluster := range t.config.Clusters() {
		clusterExists := slices.Contains(clusters, cluster.Name)
		networkExists, networkErr := t.networkExists(ctx, cluster.NetworkName)
		if networkErr != nil {
			diagnosticsErrors = append(diagnosticsErrors, networkErr)
		}
		summary.Clusters = append(summary.Clusters, diagnosticsCluster{
			LogicalName:   cluster.LogicalName,
			ResourceName:  cluster.Name,
			DC:            cluster.DC,
			Zone:          cluster.Zone,
			ClusterExists: clusterExists,
			NetworkExists: networkExists,
		})

		clusterDirectory := filepath.Join(directory, cluster.LogicalName)
		if err := os.Mkdir(clusterDirectory, 0o750); err != nil {
			diagnosticsErrors = append(diagnosticsErrors, fmt.Errorf("creating diagnostics for %s: %w", cluster.Name, err))
			continue
		}
		if networkExists {
			if captureErr := t.captureCommand(
				ctx,
				clusterDirectory,
				"network-inspect.json",
				t.dockerCommand("network", "inspect", cluster.NetworkName),
			); captureErr != nil {
				diagnosticsErrors = append(diagnosticsErrors, captureErr)
			}
		}
		if !clusterExists {
			continue
		}
		if _, validateErr := t.validateContainer(ctx, cluster); validateErr != nil {
			diagnosticsErrors = append(diagnosticsErrors, validateErr)
			continue
		}

		commands := []struct {
			file    string
			command Command
		}{
			{
				file: "container-inspect.json",
				command: t.dockerCommand(
					"container",
					"inspect",
					"--format",
					safeContainerInspectFormat,
					cluster.NodeName,
				),
			},
			{
				file: "container-stats.txt",
				command: t.dockerCommand(
					"stats",
					"--no-stream",
					"--format",
					"{{json .}}",
					cluster.NodeName,
				),
			},
			{
				file: "container-logs.txt",
				command: t.dockerCommand(
					"logs",
					"--tail",
					"500",
					"--since",
					"30m",
					cluster.NodeName,
				),
			},
		}
		for _, item := range commands {
			if captureErr := t.captureCommand(ctx, clusterDirectory, item.file, item.command); captureErr != nil {
				diagnosticsErrors = append(diagnosticsErrors, captureErr)
			}
		}

		if _, statErr := os.Stat(cluster.Kubeconfig); statErr != nil {
			if !errors.Is(statErr, os.ErrNotExist) {
				diagnosticsErrors = append(diagnosticsErrors, statErr)
			}
			continue
		}
		kubectlCommands := []struct {
			file string
			args []string
		}{
			{file: "nodes.json", args: []string{"get", "nodes", "-o", "json"}},
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
				diagnosticsErrors = append(
					diagnosticsErrors,
					fmt.Errorf("collecting %s for %s: %w", item.file, cluster.Name, captureErr),
				)
				continue
			}
			if writeErr := writePrivateFile(
				filepath.Join(clusterDirectory, item.file),
				[]byte(result.Stdout+result.Stderr),
			); writeErr != nil {
				diagnosticsErrors = append(diagnosticsErrors, writeErr)
			}
		}
	}

	summaryBytes, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		diagnosticsErrors = append(diagnosticsErrors, fmt.Errorf("encoding diagnostics summary: %w", err))
	} else if err := writePrivateFile(filepath.Join(directory, "summary.json"), append(summaryBytes, '\n')); err != nil {
		diagnosticsErrors = append(diagnosticsErrors, fmt.Errorf("writing diagnostics summary: %w", err))
	}

	t.logger.InfoContext(ctx, "topology diagnostics collected", "directory", directory)
	return errors.Join(diagnosticsErrors...)
}

const safeDockerInfoFormat = `ServerVersion={{.ServerVersion}}
Architecture={{.Architecture}}
NCPU={{.NCPU}}
MemTotal={{.MemTotal}}
Driver={{.Driver}}
OperatingSystem={{.OperatingSystem}}
OSType={{.OSType}}
KernelVersion={{.KernelVersion}}
Containers={{.Containers}}
ContainersRunning={{.ContainersRunning}}
Images={{.Images}}`

const safeContainerInspectFormat = `{"name":{{json .Name}},"image":{{json .Config.Image}},"labels":{{json .Config.Labels}},"status":{{json .State.Status}},"running":{{json .State.Running}},"started_at":{{json .State.StartedAt}},"networks":{{json .NetworkSettings.Networks}}}`

func (t *Topology) captureCommand(
	ctx context.Context,
	directory string,
	filename string,
	command Command,
) error {
	commandCtx, cancel := context.WithTimeout(ctx, diagnosticsTimeout)
	defer cancel()
	result, err := t.runner.Run(commandCtx, command)
	if err != nil {
		return fmt.Errorf("collecting %s: %w", filename, err)
	}
	contents := result.Stdout
	if result.Stderr != "" {
		contents += "\n[stderr]\n" + result.Stderr
	}
	if err := writePrivateFile(filepath.Join(directory, filename), []byte(contents)); err != nil {
		return fmt.Errorf("writing %s: %w", filename, err)
	}
	return nil
}
