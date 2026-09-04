package podchaos

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKubernetesCollectorWritesBoundedRunScopedArtifacts(t *testing.T) {
	t.Parallel()

	outputDirectory := t.TempDir()
	runner := &diagnosticRunner{output: []byte("bounded diagnostic\n")}
	collector, err := newKubernetesCollector(outputDirectory, 16*1024, runner)
	if err != nil {
		t.Fatalf("newKubernetesCollector() error = %v", err)
	}
	request := diagnosticTestRequest()
	if err := collector.Collect(boundedTestContext(t), request); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}

	directory := filepath.Join(outputDirectory, request.RunID, request.FaultID)
	for _, name := range []string{
		"summary.json", "resources.txt", "events.txt", "replacement-pod.log",
	} {
		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("os.ReadFile(%s) error = %v", name, err)
		}
		if len(content) == 0 {
			t.Fatalf("artifact %s is empty", name)
		}
	}
	if len(runner.calls) != 3 {
		t.Fatalf("diagnostic calls = %v", runner.calls)
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "secret") || strings.Contains(call, "--all-namespaces") {
			t.Fatalf("unsafe diagnostic call = %q", call)
		}
	}
}

func TestKubernetesCollectorFailsWhenArtifactsExceedBound(t *testing.T) {
	t.Parallel()

	runner := &diagnosticRunner{output: make([]byte, 2048)}
	collector, err := newKubernetesCollector(t.TempDir(), 1024, runner)
	if err != nil {
		t.Fatalf("newKubernetesCollector() error = %v", err)
	}
	if err := collector.Collect(boundedTestContext(t), diagnosticTestRequest()); err == nil {
		t.Fatal("Collect() error = nil")
	}
}

func TestKubernetesCollectorRejectsAmbientOrUnboundedOutputDirectory(t *testing.T) {
	t.Parallel()

	runner := &diagnosticRunner{}
	for _, directory := range []string{"", ".", string(filepath.Separator), "relative/output"} {
		if _, err := newKubernetesCollector(directory, 4096, runner); err == nil {
			t.Fatalf("newKubernetesCollector(%q) error = nil", directory)
		}
	}
}

func TestKubernetesCollectorPersistsSummaryWithoutSelectedPod(t *testing.T) {
	t.Parallel()

	outputDirectory := t.TempDir()
	runner := &diagnosticRunner{}
	collector, err := newKubernetesCollector(outputDirectory, 4096, runner)
	if err != nil {
		t.Fatalf("newKubernetesCollector() error = %v", err)
	}
	request := diagnosticTestRequest()
	request.Pod = PodRef{}
	request.Replacement = PodRef{}
	if err := collector.Collect(boundedTestContext(t), request); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("diagnostic calls = %v, want none", runner.calls)
	}
	if _, err := os.Stat(filepath.Join(
		outputDirectory,
		request.RunID,
		request.FaultID,
		"summary.json",
	)); err != nil {
		t.Fatalf("os.Stat(summary) error = %v", err)
	}
}

func TestKubernetesCollectorRedactsSensitiveLogLines(t *testing.T) {
	t.Parallel()

	outputDirectory := t.TempDir()
	runner := &diagnosticRunner{output: []byte(
		"normal diagnostic\nAuthorization: Bearer sensitive\npayload=private\n",
	)}
	collector, err := newKubernetesCollector(outputDirectory, 16*1024, runner)
	if err != nil {
		t.Fatalf("newKubernetesCollector() error = %v", err)
	}
	request := diagnosticTestRequest()
	if err := collector.Collect(boundedTestContext(t), request); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(
		outputDirectory,
		request.RunID,
		request.FaultID,
		"replacement-pod.log",
	))
	if err != nil {
		t.Fatalf("os.ReadFile(replacement-pod.log) error = %v", err)
	}
	if strings.Contains(string(content), "sensitive") ||
		strings.Contains(string(content), "private") ||
		!strings.Contains(string(content), "normal diagnostic") ||
		strings.Count(string(content), "[REDACTED]") != 2 {
		t.Fatalf("sanitized diagnostic = %q", content)
	}
}

func TestKubernetesCollectorReadsSelectedPodLogOnlyBeforeDeletion(t *testing.T) {
	t.Parallel()

	runner := &diagnosticRunner{output: []byte("diagnostic\n")}
	collector, err := newKubernetesCollector(t.TempDir(), 16*1024, runner)
	if err != nil {
		t.Fatalf("newKubernetesCollector() error = %v", err)
	}
	request := diagnosticTestRequest()
	request.IsDeleted = false
	request.IsRecovered = false
	request.Replacement = PodRef{}
	if err := collector.Collect(boundedTestContext(t), request); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(runner.calls) != 3 || !strings.Contains(runner.calls[2], "pod/"+request.Pod.Name) {
		t.Fatalf("diagnostic calls = %v, want exact selected pod log", runner.calls)
	}
}

type diagnosticRunner struct {
	output []byte
	err    error
	calls  []string
}

func (runner *diagnosticRunner) Run(
	_ context.Context,
	_ KubernetesTarget,
	arguments ...string,
) ([]byte, error) {
	runner.calls = append(runner.calls, strings.Join(arguments, " "))
	return append([]byte(nil), runner.output...), runner.err
}

func diagnosticTestRequest() DiagnosticRequest {
	pod := PodRef{
		KubeconfigPath: "/tmp/mm32-kubeconfig",
		ContextName:    "kind-mm32-a-dmz",
		Namespace:      "marketmesh-e2e-tunnel",
		Deployment:     "mm29-gateway-in",
		Name:           "mm29-gateway-in-old",
		UID:            "pod-uid-old",
		OwnerRunID:     "mm32-diagnostics",
	}
	replacement := pod
	replacement.Name = "mm29-gateway-in-new"
	replacement.UID = "pod-uid-new"
	return DiagnosticRequest{
		RunID: "mm32-diagnostics", FaultID: "fault-01",
		Step: Step{DC: DCA, Component: ComponentGatewayIn, Role: RoleActive},
		Pod:  pod, Replacement: replacement, IsDeleted: true, IsRecovered: true,
	}
}

var _ kubeCommandRunner = (*diagnosticRunner)(nil)
