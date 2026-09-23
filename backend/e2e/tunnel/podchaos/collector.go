package podchaos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/workload"
)

const defaultDiagnosticBytes = 8 * 1024 * 1024

var sensitiveDiagnosticMarkers = [][]byte{
	[]byte("authorization"),
	[]byte("cookie"),
	[]byte("idempotency"),
	[]byte("password"),
	[]byte("payload"),
	[]byte("private key"),
	[]byte("certificate"),
	[]byte("secret"),
	[]byte("token"),
}

// KubernetesCollector writes bounded Kubernetes metadata/events/logs under an
// os.Root scoped to the explicitly configured artifact directory.
type KubernetesCollector struct {
	outputDirectory string
	maxBytes        int
	kubectl         kubeCommandRunner
}

type diagnosticSummary struct {
	RunID          string    `json:"run_id"`
	FaultID        string    `json:"fault_id"`
	DC             DC        `json:"dc"`
	Component      Component `json:"component"`
	Role           Role      `json:"role"`
	Pod            string    `json:"pod,omitempty"`
	PodUID         string    `json:"pod_uid,omitempty"`
	Replacement    string    `json:"replacement,omitempty"`
	ReplacementUID string    `json:"replacement_uid,omitempty"`
	IsDeleted      bool      `json:"is_deleted"`
	IsRecovered    bool      `json:"is_recovered"`
}

// NewKubernetesCollector creates the bounded production collector.
func NewKubernetesCollector(
	outputDirectory string,
	maxBytes int,
	kubectlPath string,
) (*KubernetesCollector, error) {
	if err := validateKubectlPath(kubectlPath); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfiguration, err)
	}
	return newKubernetesCollector(
		outputDirectory,
		maxBytes,
		kubectlCommandRunner{path: kubectlPath},
	)
}

func newKubernetesCollector(
	outputDirectory string,
	maxBytes int,
	runner kubeCommandRunner,
) (*KubernetesCollector, error) {
	if runner == nil {
		return nil, fmt.Errorf("%w: diagnostic kubectl runner is required", ErrInvalidConfiguration)
	}
	if maxBytes == 0 {
		maxBytes = defaultDiagnosticBytes
	}
	if maxBytes < 1024 || maxBytes > 32*1024*1024 {
		return nil, fmt.Errorf("%w: diagnostic byte limit is outside bounds", ErrInvalidConfiguration)
	}
	if !filepath.IsAbs(outputDirectory) ||
		filepath.Clean(outputDirectory) != outputDirectory ||
		outputDirectory == string(filepath.Separator) ||
		strings.TrimSpace(outputDirectory) != outputDirectory {
		return nil, fmt.Errorf("%w: diagnostic directory is invalid", ErrInvalidConfiguration)
	}
	if err := os.MkdirAll(outputDirectory, 0o750); err != nil {
		return nil, errors.New("podchaos: creating diagnostic directory")
	}

	return &KubernetesCollector{
		outputDirectory: outputDirectory,
		maxBytes:        maxBytes,
		kubectl:         runner,
	}, nil
}

// Collect implements Collector. All command output is bounded before it is
// written, and a truncation or failed diagnostic command keeps the fault failed.
func (collector *KubernetesCollector) Collect(
	ctx context.Context,
	request DiagnosticRequest,
) (resultErr error) {
	if !hasDeadline(ctx) || collector == nil ||
		!isMM32RunID(request.RunID) || !isDNSLabel(request.FaultID) ||
		!validStep(request.Step) {
		return fmt.Errorf("%w: diagnostic request is invalid", ErrUnsafeState)
	}
	root, err := os.OpenRoot(collector.outputDirectory)
	if err != nil {
		return errors.New("podchaos: opening diagnostic root")
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, errors.New("podchaos: closing diagnostic root"))
		}
	}()
	directory := request.RunID + "/" + request.FaultID
	if err := root.MkdirAll(directory, 0o750); err != nil {
		return errors.New("podchaos: creating fault diagnostic directory")
	}

	remaining := collector.maxBytes
	summary, err := json.MarshalIndent(diagnosticSummary{
		RunID: request.RunID, FaultID: request.FaultID,
		DC: request.Step.DC, Component: request.Step.Component, Role: request.Step.Role,
		Pod: request.Pod.Name, PodUID: request.Pod.UID,
		Replacement: request.Replacement.Name, ReplacementUID: request.Replacement.UID,
		IsDeleted: request.IsDeleted, IsRecovered: request.IsRecovered,
	}, "", "  ")
	if err != nil {
		return errors.New("podchaos: encoding diagnostic summary")
	}
	summary = append(summary, '\n')
	if err := writeDiagnostic(root, directory+"/summary.json", summary, &remaining); err != nil {
		return err
	}
	if request.Pod.Name == "" {
		return nil
	}
	if err := validatePodRef(request.RunID, request.Pod); err != nil {
		return err
	}
	target := KubernetesTarget{
		DC:             request.Step.DC,
		Zone:           zoneForComponent(request.Step.Component),
		KubeconfigPath: request.Pod.KubeconfigPath,
		ContextName:    request.Pod.ContextName,
	}
	selector := "marketmesh.io/run-id=" + request.RunID
	commands := []struct {
		name      string
		arguments []string
	}{
		{
			name: "resources.txt",
			arguments: []string{
				"get", "deployment,replicaset,pod", "--namespace=" + workload.Namespace,
				"--selector=" + selector, "--output=wide", "--show-labels=true",
			},
		},
		{
			name: "events.txt",
			arguments: []string{
				"get", "events", "--namespace=" + workload.Namespace,
				"--sort-by=.lastTimestamp",
			},
		},
	}
	// A successfully deleted exact UID is expected to be absent. Avoid turning
	// that expected NotFound into a diagnostic failure; the replacement log and
	// namespace events preserve the post-fault evidence. If deletion did not
	// occur, the still-present selected pod log is collected.
	if !request.IsDeleted {
		commands = append(commands, struct {
			name      string
			arguments []string
		}{
			name: "old-pod.log",
			arguments: []string{
				"logs", "--namespace=" + workload.Namespace, "pod/" + request.Pod.Name,
				"--all-containers=true", "--prefix=true", "--tail=200",
			},
		})
	}
	if request.Replacement.Name != "" {
		if err := validatePodRef(request.RunID, request.Replacement); err != nil {
			return err
		}
		commands = append(commands, struct {
			name      string
			arguments []string
		}{
			name: "replacement-pod.log",
			arguments: []string{
				"logs", "--namespace=" + workload.Namespace,
				"pod/" + request.Replacement.Name,
				"--all-containers=true", "--prefix=true", "--tail=200",
			},
		})
	}

	for _, command := range commands {
		output, commandErr := collector.kubectl.Run(ctx, target, command.arguments...)
		if len(output) > 0 {
			if err := writeDiagnostic(
				root,
				directory+"/"+command.name,
				sanitizeDiagnosticOutput(output),
				&remaining,
			); err != nil {
				resultErr = errors.Join(resultErr, err)
				break
			}
		}
		if commandErr != nil {
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("podchaos: collecting %s", command.name),
			)
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(resultErr, err)
		}
	}
	return resultErr
}

func sanitizeDiagnosticOutput(content []byte) []byte {
	lines := bytes.SplitAfter(content, []byte{'\n'})
	result := make([]byte, 0, len(content))
	for _, line := range lines {
		lower := bytes.ToLower(line)
		isSensitive := false
		for _, marker := range sensitiveDiagnosticMarkers {
			if bytes.Contains(lower, marker) {
				isSensitive = true
				break
			}
		}
		if !isSensitive {
			result = append(result, line...)
			continue
		}
		result = append(result, "[REDACTED]"...)
		if len(line) > 0 && line[len(line)-1] == '\n' {
			result = append(result, '\n')
		}
	}
	return result
}

func writeDiagnostic(root *os.Root, name string, content []byte, remaining *int) error {
	if len(content) > *remaining {
		return errors.New("podchaos: diagnostic byte limit exceeded")
	}
	if err := root.WriteFile(name, content, 0o600); err != nil {
		return errors.New("podchaos: writing diagnostic artifact")
	}
	*remaining -= len(content)
	return nil
}

func zoneForComponent(component Component) string {
	if component == ComponentGatewayIn {
		return ZoneDMZ
	}
	return ZoneInternal
}
