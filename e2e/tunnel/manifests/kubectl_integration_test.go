//go:build integration

package manifests

import (
	"bytes"
	"flag"
	"os/exec"
	"testing"
)

var (
	integrationKubeconfig = flag.String(
		"mm29-kubeconfig",
		"",
		"explicit disposable kubeconfig used for server-side manifest validation",
	)
	integrationContext = flag.String(
		"mm29-context",
		"",
		"explicit disposable Kubernetes context used for manifest validation",
	)
)

func TestRenderedManifestsPassKubectlClientValidation(t *testing.T) {
	t.Parallel()

	kubectl, err := exec.LookPath("kubectl")
	if err != nil {
		t.Skip("kubectl is not installed")
	}
	if *integrationKubeconfig == "" || *integrationContext == "" {
		t.Skip("explicit -mm29-kubeconfig and -mm29-context are required")
	}
	parameters := validParameters()
	renderers := []struct {
		name   string
		render func(Parameters) ([]byte, error)
	}{
		{name: "dmz", render: RenderDMZ},
		{name: "internal", render: RenderInternal},
	}
	for _, renderer := range renderers {
		t.Run(renderer.name, func(t *testing.T) {
			t.Parallel()
			content, renderErr := renderer.render(parameters)
			if renderErr != nil {
				t.Fatalf("render() error = %v", renderErr)
			}
			command := exec.CommandContext(
				t.Context(),
				kubectl,
				"--kubeconfig="+*integrationKubeconfig,
				"--context="+*integrationContext,
				"apply",
				"--dry-run=server",
				"--filename=-",
				"--output=name",
			)
			command.Stdin = bytes.NewReader(content)
			output, commandErr := command.CombinedOutput()
			if commandErr != nil {
				t.Fatalf("kubectl validation error = %v: %s", commandErr, output)
			}
		})
	}
}
