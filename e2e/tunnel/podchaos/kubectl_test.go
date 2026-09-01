package podchaos

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateKubectlPathRequiresExactExecutable(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	executable := filepath.Join(directory, "kubectl")
	if err := os.WriteFile(executable, []byte("executable\n"), 0o700); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := validateKubectlPath(executable); err != nil {
		t.Fatalf("validateKubectlPath(executable) error = %v", err)
	}

	nonExecutable := filepath.Join(directory, "non-executable")
	if err := os.WriteFile(nonExecutable, []byte("data\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	symlink := filepath.Join(directory, "kubectl-link")
	if err := os.Symlink(executable, symlink); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	for _, path := range []string{"kubectl", nonExecutable, symlink, string(filepath.Separator)} {
		if err := validateKubectlPath(path); err == nil {
			t.Fatalf("validateKubectlPath(%q) error = nil", path)
		}
	}
}
