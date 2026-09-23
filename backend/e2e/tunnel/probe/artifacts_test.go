//go:build darwin || linux

package probe

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
		switch name {
		case JSONReportArtifactName:
			var report spec.Report
			decoder := json.NewDecoder(bytes.NewReader(data))
			if decodeErr := decoder.Decode(&report); decodeErr != nil {
				t.Fatalf("json.Decode(%s) error = %v", name, decodeErr)
			}
			if decodeErr := decoder.Decode(&struct{}{}); !errors.Is(decodeErr, io.EOF) {
				t.Fatalf("json.Decode(%s trailing data) error = %v, want EOF", name, decodeErr)
			}
		case JUnitArtifactName:
			decoder := xml.NewDecoder(bytes.NewReader(data))
			for {
				if _, decodeErr := decoder.Token(); errors.Is(decodeErr, io.EOF) {
					break
				} else if decodeErr != nil {
					t.Fatalf("xml.Decode(%s) error = %v", name, decodeErr)
				}
			}
		case TextReportArtifactName:
			if len(bytes.TrimSpace(data)) == 0 {
				t.Fatalf("%s is empty", name)
			}
		}
	}

	if err := WriteArtifacts(directory, result); err == nil {
		t.Fatal("second WriteArtifacts() error = nil")
	}
	assertNoArtifactStaging(t, directory)
}

func TestWriteArtifactsDoesNotPublishUntilBundleIsComplete(t *testing.T) {
	result := passingArtifactResult(t)
	directory := filepath.Join(t.TempDir(), "blocked-run")
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	ops := defaultArtifactOperations()
	openDirectory := ops.openDirectory
	ops.openDirectory = func(path string) (artifactDirectory, error) {
		directoryRoot, err := openDirectory(path)
		if err != nil {
			return nil, err
		}
		return &artifactDirectoryInterceptor{
			artifactDirectory: directoryRoot,
			intercept: func(opened int, file artifactFile) artifactFile {
				if opened != 1 {
					return file
				}
				return &artifactFileInterceptor{
					artifactFile: file,
					write: func(data []byte) (int, error) {
						once.Do(func() { close(started) })
						<-release
						return file.Write(data)
					},
				}
			},
		}, nil
	}

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- writeArtifacts(directory, result, ops)
	}()
	<-started
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		close(release)
		t.Fatalf("os.Lstat(final) error = %v, want not exist during write", err)
	}
	staging := artifactStagingPaths(t, directory)
	if len(staging) != 1 {
		close(release)
		t.Fatalf("staging paths = %v, want one private sibling", staging)
	}
	info, err := os.Stat(staging[0])
	if err != nil {
		close(release)
		t.Fatalf("os.Stat(staging) error = %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o700 {
		close(release)
		t.Fatalf("staging permissions = %o, want 700", permissions)
	}

	close(release)
	if err := <-writeDone; err != nil {
		t.Fatalf("writeArtifacts() error = %v", err)
	}
	if _, err := os.Stat(directory); err != nil {
		t.Fatalf("os.Stat(final) error = %v", err)
	}
	assertNoArtifactStaging(t, directory)
}

func TestWriteArtifactsCleansStagingAfterFileFailure(t *testing.T) {
	result := passingArtifactResult(t)

	for _, operation := range []string{"write", "close"} {
		operation := operation
		for failedFile := 1; failedFile <= 4; failedFile++ {
			failedFile := failedFile
			t.Run(operation+"-file-"+strconv.Itoa(failedFile), func(t *testing.T) {
				directory := filepath.Join(t.TempDir(), "failed-run")
				injectedErr := errors.New("injected " + operation + " failure")
				ops := operationsWithFileFailure(operation, failedFile, injectedErr)

				err := writeArtifacts(directory, result, ops)
				if !errors.Is(err, injectedErr) {
					t.Fatalf("writeArtifacts() error = %v, want %v", err, injectedErr)
				}
				assertArtifactUnpublished(t, directory)
			})
		}
	}
}

func TestWriteArtifactsCleansStagingAfterRenameFailure(t *testing.T) {
	result := passingArtifactResult(t)
	directory := filepath.Join(t.TempDir(), "failed-rename")
	injectedErr := errors.New("injected rename failure")
	ops := defaultArtifactOperations()
	ops.renameNoReplace = func(string, string) error { return injectedErr }

	err := writeArtifacts(directory, result, ops)
	if !errors.Is(err, injectedErr) {
		t.Fatalf("writeArtifacts() error = %v, want %v", err, injectedErr)
	}
	assertArtifactUnpublished(t, directory)
}

func TestWriteArtifactsRejectsUnsupportedPlatformBeforeStaging(t *testing.T) {
	parent := t.TempDir()
	directory := filepath.Join(parent, "unsupported-run")
	ops := defaultArtifactOperations()
	ops.validatePlatform = func() error { return errors.ErrUnsupported }
	ops.mkdirTemp = func(string, string) (string, error) {
		t.Fatal("mkdirTemp() called on unsupported platform")
		return "", nil
	}

	err := writeArtifacts(directory, ReportResult{}, ops)
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("writeArtifacts() error = %v, want unsupported", err)
	}
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		t.Fatalf("os.Lstat(final) error = %v, want not exist", err)
	}
	assertNoArtifactStaging(t, directory)
}

func TestWriteArtifactsJoinsCleanupFailure(t *testing.T) {
	result := passingArtifactResult(t)
	directory := filepath.Join(t.TempDir(), "failed-cleanup")
	renameErr := errors.New("injected rename failure")
	cleanupErr := errors.New("injected cleanup failure")
	ops := defaultArtifactOperations()
	ops.renameNoReplace = func(string, string) error { return renameErr }
	removeAll := ops.removeAll
	ops.removeAll = func(path string) error {
		return errors.Join(removeAll(path), cleanupErr)
	}

	err := writeArtifacts(directory, result, ops)
	if !errors.Is(err, renameErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("writeArtifacts() error = %v, want joined rename and cleanup errors", err)
	}
	assertArtifactUnpublished(t, directory)
}

func TestWriteArtifactsRejectsExistingTargetUnchanged(t *testing.T) {
	result := passingArtifactResult(t)
	directory := filepath.Join(t.TempDir(), "existing-run")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("os.Mkdir() error = %v", err)
	}
	marker := filepath.Join(directory, "keep")
	if err := os.WriteFile(marker, []byte("unchanged"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(marker) error = %v", err)
	}

	if err := WriteArtifacts(directory, result); err == nil {
		t.Fatal("WriteArtifacts() error = nil for existing target")
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("os.ReadFile(marker) error = %v", err)
	}
	if string(data) != "unchanged" {
		t.Fatalf("marker = %q, want unchanged", data)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("os.ReadDir(existing target) error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "keep" {
		t.Fatalf("existing target entries = %v, want only keep", entries)
	}
	assertNoArtifactStaging(t, directory)
}

func TestWriteArtifactsRejectsSymlinkTargetWithoutFollowingIt(t *testing.T) {
	result := passingArtifactResult(t)
	parent := t.TempDir()
	victim := filepath.Join(parent, "victim")
	if err := os.Mkdir(victim, 0o700); err != nil {
		t.Fatalf("os.Mkdir(victim) error = %v", err)
	}
	marker := filepath.Join(victim, "keep")
	if err := os.WriteFile(marker, []byte("unchanged"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(marker) error = %v", err)
	}
	directory := filepath.Join(parent, "run-link")
	if err := os.Symlink(victim, directory); err != nil {
		t.Fatalf("os.Symlink() error = %v", err)
	}

	if err := WriteArtifacts(directory, result); err == nil {
		t.Fatal("WriteArtifacts() error = nil for symlink target")
	}
	info, err := os.Lstat(directory)
	if err != nil {
		t.Fatalf("os.Lstat(symlink) error = %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("target mode = %v, want symlink unchanged", info.Mode())
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("os.ReadFile(marker) error = %v", err)
	}
	if string(data) != "unchanged" {
		t.Fatalf("marker = %q, want unchanged", data)
	}
	assertNoArtifactStaging(t, directory)
}

func TestWriteArtifactsDoesNotReplaceTargetCreatedDuringPublication(t *testing.T) {
	result := passingArtifactResult(t)
	directory := filepath.Join(t.TempDir(), "raced-run")
	injectedTarget := []byte("created concurrently")
	ops := defaultArtifactOperations()
	renameNoReplace := ops.renameNoReplace
	ops.renameNoReplace = func(source, target string) error {
		if err := os.WriteFile(target, injectedTarget, 0o600); err != nil {
			return err
		}
		return renameNoReplace(source, target)
	}

	if err := writeArtifacts(directory, result, ops); err == nil {
		t.Fatal("writeArtifacts() error = nil for concurrently created target")
	}
	data, err := os.ReadFile(directory)
	if err != nil {
		t.Fatalf("os.ReadFile(concurrent target) error = %v", err)
	}
	if !bytes.Equal(data, injectedTarget) {
		t.Fatalf("concurrent target = %q, want unchanged", data)
	}
	assertNoArtifactStaging(t, directory)
}

func TestWriteArtifactsRejectsOversizedRunBeforePublication(t *testing.T) {
	scenario, input := passingReportFixture()
	result, err := BuildReport(scenario, input)
	if err != nil {
		t.Fatalf("BuildReport() error = %v", err)
	}
	result.Run.RunID = strings.Repeat("a", 65<<20)
	directory := filepath.Join(t.TempDir(), "oversized-run")

	if err := WriteArtifacts(directory, result); err == nil {
		t.Fatal("WriteArtifacts() error = nil for run larger than DecodeRun capacity")
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("os.Stat(directory) error = %v, want not exist", err)
	}
}

type artifactDirectoryInterceptor struct {
	artifactDirectory
	opened    int
	intercept func(opened int, file artifactFile) artifactFile
}

func (directory *artifactDirectoryInterceptor) OpenFile(
	name string,
	flag int,
	permissions fs.FileMode,
) (artifactFile, error) {
	file, err := directory.artifactDirectory.OpenFile(name, flag, permissions)
	if err != nil {
		return nil, err
	}
	directory.opened++
	return directory.intercept(directory.opened, file), nil
}

type artifactFileInterceptor struct {
	artifactFile
	write func([]byte) (int, error)
	close func() error
}

func (file *artifactFileInterceptor) Write(data []byte) (int, error) {
	if file.write != nil {
		return file.write(data)
	}
	return file.artifactFile.Write(data)
}

func (file *artifactFileInterceptor) Close() error {
	if file.close != nil {
		return file.close()
	}
	return file.artifactFile.Close()
}

func passingArtifactResult(t *testing.T) ReportResult {
	t.Helper()
	scenario, input := passingReportFixture()
	result, err := BuildReport(scenario, input)
	if err != nil {
		t.Fatalf("BuildReport() error = %v", err)
	}
	return result
}

func operationsWithFileFailure(
	operation string,
	failedFile int,
	injectedErr error,
) artifactOperations {
	ops := defaultArtifactOperations()
	openDirectory := ops.openDirectory
	ops.openDirectory = func(path string) (artifactDirectory, error) {
		directory, err := openDirectory(path)
		if err != nil {
			return nil, err
		}
		return &artifactDirectoryInterceptor{
			artifactDirectory: directory,
			intercept: func(opened int, file artifactFile) artifactFile {
				if opened != failedFile {
					return file
				}
				interceptor := &artifactFileInterceptor{artifactFile: file}
				switch operation {
				case "write":
					interceptor.write = func([]byte) (int, error) {
						return 0, injectedErr
					}
				case "close":
					interceptor.close = func() error {
						return errors.Join(file.Close(), injectedErr)
					}
				}
				return interceptor
			},
		}, nil
	}
	return ops
}

func assertArtifactUnpublished(t *testing.T, directory string) {
	t.Helper()
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		t.Fatalf("os.Lstat(final) error = %v, want not exist", err)
	}
	assertNoArtifactStaging(t, directory)
}

func assertNoArtifactStaging(t *testing.T, directory string) {
	t.Helper()
	if staging := artifactStagingPaths(t, directory); len(staging) != 0 {
		t.Fatalf("staging paths = %v, want none", staging)
	}
}

func artifactStagingPaths(t *testing.T, directory string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(
		filepath.Dir(directory),
		"."+filepath.Base(directory)+".staging-*",
	))
	if err != nil {
		t.Fatalf("filepath.Glob(staging) error = %v", err)
	}
	return paths
}

var _ io.Writer = (*artifactFileInterceptor)(nil)
