package testkit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TempDir создаёт отдельный каталог с доступом только для текущего
// пользователя и удаляет его через Cleanup.
func TempDir(t testing.TB) string {
	t.Helper()

	directory, err := os.MkdirTemp("", "marketmesh-testkit-")
	if err != nil {
		t.Fatalf("testkit: create temp directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("testkit: remove temp directory: %v", err)
		}
	})

	return directory
}

// TempFile создаёт новый файл с режимом 0600 внутри отдельного TempDir.
// name должен быть локальным именем файла без компонентов каталога.
func TempFile(t testing.TB, name string, data []byte) string {
	t.Helper()

	directory := TempDir(t)
	path, err := writeTempFile(directory, name, data)
	if err != nil {
		t.Fatalf("testkit: create temp file: %v", err)
	}

	return path
}

func writeTempFile(directory string, name string, data []byte) (string, error) {
	if !filepath.IsLocal(name) || filepath.Base(name) != name || name == "." {
		return "", fmt.Errorf("invalid local filename %q", name)
	}

	root, err := os.OpenRoot(directory)
	if err != nil {
		return "", fmt.Errorf("open temp root: %w", err)
	}

	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", errors.Join(
			fmt.Errorf("open temp file: %w", err),
			closeTempRoot(root),
		)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	rootCloseErr := closeTempRoot(root)
	if err := errors.Join(writeErr, closeErr, rootCloseErr); err != nil {
		return "", fmt.Errorf("write temp file: %w", err)
	}

	return filepath.Join(directory, name), nil
}

func closeTempRoot(root *os.Root) error {
	if err := root.Close(); err != nil {
		return fmt.Errorf("close temp root: %w", err)
	}

	return nil
}
