package probe

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

func TestWriteArtifactsCreatesPrivateCompleteBundle(t *testing.T) {
	t.Parallel()

	scenario, input := passingReportFixture()
	result, err := BuildReport(scenario, input)
	if err != nil {
		t.Fatalf("BuildReport() error = %v", err)
	}
	directory := filepath.Join(t.TempDir(), "run-31")
	if err := WriteArtifacts(directory, result); err != nil {
		t.Fatalf("WriteArtifacts() error = %v", err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("os.Stat(directory) error = %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o700 {
		t.Fatalf("directory permissions = %o, want 700", permissions)
	}

	for _, name := range []string{
		RunArtifactName,
		JSONReportArtifactName,
		JUnitArtifactName,
		TextReportArtifactName,
	} {
		path := filepath.Join(directory, name)
		artifactInfo, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("os.Stat(%s) error = %v", name, statErr)
		}
		if permissions := artifactInfo.Mode().Perm(); permissions != 0o600 {
			t.Fatalf("%s permissions = %o, want 600", name, permissions)
		}
	}

	runData, err := os.ReadFile(filepath.Join(directory, RunArtifactName))
	if err != nil {
		t.Fatalf("os.ReadFile(run) error = %v", err)
	}
	if _, err := spec.DecodeRun(bytes.NewReader(runData)); err != nil {
		t.Fatalf("spec.DecodeRun() error = %v", err)
	}
	for _, name := range []string{
		JSONReportArtifactName,
		JUnitArtifactName,
		TextReportArtifactName,
	} {
		data, readErr := os.ReadFile(filepath.Join(directory, name))
		if readErr != nil {
			t.Fatalf("os.ReadFile(%s) error = %v", name, readErr)
		}
		for _, forbidden := range []string{requestID1, requestID2} {
			if strings.Contains(string(data), forbidden) {
				t.Fatalf("%s contains request data %q", name, forbidden)
			}
		}
	}

	if err := WriteArtifacts(directory, result); err == nil {
		t.Fatal("second WriteArtifacts() error = nil")
	}
}
