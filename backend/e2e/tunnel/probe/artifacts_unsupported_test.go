//go:build !darwin && !linux

package probe

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteArtifactsFailsBeforeStagingOnUnsupportedPlatform(t *testing.T) {
	parent := t.TempDir()
	directory := filepath.Join(parent, "unsupported-run")

	err := WriteArtifacts(directory, ReportResult{})
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("WriteArtifacts() error = %v, want unsupported", err)
	}
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		t.Fatalf("os.Lstat(final) error = %v, want not exist", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("os.ReadDir(parent) error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("parent entries = %v, want no staging artifacts", entries)
	}
}
