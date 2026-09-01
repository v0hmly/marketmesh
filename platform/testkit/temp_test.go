package testkit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTempDirUsesRestrictedMode(t *testing.T) {
	t.Parallel()

	directory := TempDir(t)
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("stat temp directory: %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o700 {
		t.Fatalf("temp directory permissions = %o, want 700", permissions)
	}
}

func TestTempFileUsesRestrictedMode(t *testing.T) {
	t.Parallel()

	path := TempFile(t, "secret.pem", []byte("temporary"))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat temp file: %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("temp file permissions = %o, want 600", permissions)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp file: %v", err)
	}
	if string(data) != "temporary" {
		t.Fatalf("temp file data = %q", data)
	}
}

func TestWriteTempFileRejectsTraversalAndDuplicates(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	for _, name := range []string{"../outside", "/absolute", "nested/file", "."} {
		if _, err := writeTempFile(directory, name, nil); err == nil {
			t.Errorf("writeTempFile(%q) error = nil", name)
		}
	}
	if _, err := writeTempFile(directory, "safe", nil); err != nil {
		t.Fatalf("write safe temp file: %v", err)
	}
	if _, err := writeTempFile(directory, "safe", nil); err == nil {
		t.Fatal("duplicate temp file error = nil")
	}
	if _, err := os.Stat(filepath.Join(directory, "safe")); err != nil {
		t.Fatalf("safe temp file missing: %v", err)
	}
}
