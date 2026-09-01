package topology

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDownloadURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     string
		wantError bool
	}{
		{name: "github", value: "https://github.com/org/repo/file"},
		{name: "github asset", value: "https://release-assets.githubusercontent.com/file"},
		{name: "kubernetes", value: "https://dl.k8s.io/release/file"},
		{name: "plain http", value: "http://github.com/org/repo/file", wantError: true},
		{name: "lookalike host", value: "https://github.com.evil.example/file", wantError: true},
		{name: "metadata", value: "https://169.254.169.254/latest/meta-data", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			parsed, err := url.Parse(test.value)
			if err != nil {
				t.Fatalf("urlParse() error = %v", err)
			}
			err = validateDownloadURL(parsed)
			if (err != nil) != test.wantError {
				t.Errorf("validateDownloadURL() error = %v, wantError %v", err, test.wantError)
			}
		})
	}
}

func TestInstallAsset(t *testing.T) {
	t.Parallel()

	content := []byte("verified executable")
	hash := sha256.Sum256(content)
	expectedChecksum := hex.EncodeToString(hash[:])
	directory := t.TempDir()
	destination := filepath.Join(directory, "tool")
	if err := installAsset(destination, bytes.NewReader(content), int64(len(content)), expectedChecksum); err != nil {
		t.Fatalf("installAsset() error = %v", err)
	}
	// #nosec G304 -- destination is derived exclusively from t.TempDir.
	installed, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(installed, content) {
		t.Errorf("installed content = %q, want %q", installed, content)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Errorf("installed mode = %o, want 750", info.Mode().Perm())
	}
}

func TestInstallAssetRejectsChecksumMismatch(t *testing.T) {
	t.Parallel()

	destination := filepath.Join(t.TempDir(), "tool")
	err := installAsset(destination, strings.NewReader("content"), 7, strings.Repeat("0", 64))
	if err == nil {
		t.Fatal("installAsset() error = nil, want checksum error")
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Errorf("destination exists after checksum failure: %v", statErr)
	}
}
