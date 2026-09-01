package probe

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

const (
	// RunArtifactName is the complete MM-27 run ledger with opaque request data.
	RunArtifactName = "run.json"
	// JSONReportArtifactName is the request-data-free MM-27 JSON summary.
	JSONReportArtifactName = "report.json"
	// JUnitArtifactName is the request-data-free MM-27 JUnit summary.
	JUnitArtifactName = "report.junit.xml"
	// TextReportArtifactName is the request-data-free human-readable summary.
	TextReportArtifactName = "report.txt"
)

// WriteArtifacts creates one new private directory and writes the complete run
// plus JSON, JUnit, and human-readable reports. It refuses an existing target
// and never replaces files, so diagnostics from an earlier run are preserved.
func WriteArtifacts(directory string, result ReportResult) error {
	if directory == "" {
		return errors.New("probe: artifact directory must not be empty")
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		return fmt.Errorf("probe: create artifact directory: %w", err)
	}

	writers := []struct {
		name  string
		write func(io.Writer) error
	}{
		{name: RunArtifactName, write: func(writer io.Writer) error {
			encoder := json.NewEncoder(writer)
			encoder.SetIndent("", "  ")
			encoder.SetEscapeHTML(true)
			return encoder.Encode(result.Run)
		}},
		{name: JSONReportArtifactName, write: func(writer io.Writer) error {
			return spec.WriteJSONReport(writer, result.Report)
		}},
		{name: JUnitArtifactName, write: func(writer io.Writer) error {
			return spec.WriteJUnitReport(writer, result.Report)
		}},
		{name: TextReportArtifactName, write: func(writer io.Writer) error {
			return WriteTextReport(writer, result.Report)
		}},
	}
	for _, artifact := range writers {
		if err := writeArtifact(directory, artifact.name, artifact.write); err != nil {
			return err
		}
	}

	return nil
}

func writeArtifact(
	directory string,
	name string,
	write func(io.Writer) error,
) (resultErr error) {
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("probe: create artifact %s: %w", name, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("probe: close artifact %s: %w", name, closeErr),
			)
		}
	}()

	if err := write(file); err != nil {
		return fmt.Errorf("probe: write artifact %s: %w", name, err)
	}
	return nil
}
