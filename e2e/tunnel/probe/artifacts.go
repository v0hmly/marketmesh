package probe

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

// WriteArtifacts atomically publishes one new private directory containing the
// complete run plus JSON, JUnit, and human-readable reports. It refuses an
// existing target, so diagnostics from an earlier run are preserved. This is
// an atomic-visibility contract, not crash durability; files are not fsynced.
// Linux and Darwin are supported; other platforms fail before staging data.
func WriteArtifacts(directory string, result ReportResult) error {
	return writeArtifacts(directory, result, defaultArtifactOperations())
}

type artifactFile interface {
	io.Writer
	io.Closer
}

type artifactDirectory interface {
	OpenFile(name string, flag int, permissions fs.FileMode) (artifactFile, error)
	Close() error
}

type artifactOperations struct {
	validatePlatform func() error
	lstat            func(string) (fs.FileInfo, error)
	mkdirTemp        func(string, string) (string, error)
	openDirectory    func(string) (artifactDirectory, error)
	renameNoReplace  func(string, string) error
	removeAll        func(string) error
}

type osArtifactDirectory struct {
	root *os.Root
}

func (directory osArtifactDirectory) OpenFile(
	name string,
	flag int,
	permissions fs.FileMode,
) (artifactFile, error) {
	return directory.root.OpenFile(name, flag, permissions)
}

func (directory osArtifactDirectory) Close() error {
	return directory.root.Close()
}

func defaultArtifactOperations() artifactOperations {
	return artifactOperations{
		validatePlatform: validateArtifactPublicationPlatform,
		lstat:            os.Lstat,
		mkdirTemp:        os.MkdirTemp,
		openDirectory: func(path string) (artifactDirectory, error) {
			root, err := os.OpenRoot(path)
			if err != nil {
				return nil, err
			}
			return osArtifactDirectory{root: root}, nil
		},
		renameNoReplace: renameArtifactDirectoryNoReplace,
		removeAll:       os.RemoveAll,
	}
}

func writeArtifacts(
	directory string,
	result ReportResult,
	operations artifactOperations,
) (resultErr error) {
	if directory == "" {
		return errors.New("probe: artifact directory must not be empty")
	}
	if err := operations.validatePlatform(); err != nil {
		return fmt.Errorf("probe: validate artifact publication platform: %w", err)
	}
	directory = filepath.Clean(directory)
	var run bytes.Buffer
	if err := spec.WriteRun(&run, result.Run); err != nil {
		return fmt.Errorf("probe: prepare run artifact: %w", err)
	}
	if _, err := operations.lstat(directory); err == nil {
		return fmt.Errorf("probe: publish artifact directory: %w", fs.ErrExist)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("probe: inspect artifact directory: %w", err)
	}

	staging, err := operations.mkdirTemp(
		filepath.Dir(directory),
		"."+filepath.Base(directory)+".staging-*",
	)
	if err != nil {
		return fmt.Errorf("probe: create artifact staging directory: %w", err)
	}
	defer func() {
		if staging == "" {
			return
		}
		if cleanupErr := operations.removeAll(staging); cleanupErr != nil {
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("probe: remove artifact staging directory: %w", cleanupErr),
			)
		}
	}()

	root, err := operations.openDirectory(staging)
	if err != nil {
		return fmt.Errorf("probe: open artifact staging directory: %w", err)
	}

	writers := []struct {
		name  string
		write func(io.Writer) error
	}{
		{name: RunArtifactName, write: func(writer io.Writer) error {
			_, err := io.Copy(writer, bytes.NewReader(run.Bytes()))
			return err
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
		if err := writeArtifact(root, artifact.name, artifact.write); err != nil {
			if closeErr := root.Close(); closeErr != nil {
				err = errors.Join(
					err,
					fmt.Errorf("probe: close artifact staging directory: %w", closeErr),
				)
			}
			return err
		}
	}
	if err := root.Close(); err != nil {
		return fmt.Errorf("probe: close artifact staging directory: %w", err)
	}
	if err := operations.renameNoReplace(staging, directory); err != nil {
		return fmt.Errorf("probe: publish artifact directory: %w", err)
	}
	staging = ""

	return nil
}

func writeArtifact(
	root artifactDirectory,
	name string,
	write func(io.Writer) error,
) (resultErr error) {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
