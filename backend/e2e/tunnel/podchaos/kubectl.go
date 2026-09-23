package podchaos

import (
	"errors"
	"os"
	"path/filepath"
)

func validateKubectlPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		path == string(filepath.Separator) {
		return errors.New("podchaos: kubectl path must be absolute, clean, and non-root")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("podchaos: kubectl path must identify a non-symlink executable file")
	}
	return nil
}
