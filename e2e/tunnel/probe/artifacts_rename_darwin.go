//go:build darwin

package probe

import "golang.org/x/sys/unix"

func validateArtifactPublicationPlatform() error {
	return nil
}

func renameArtifactDirectoryNoReplace(source, target string) error {
	return unix.RenameatxNp(
		unix.AT_FDCWD,
		source,
		unix.AT_FDCWD,
		target,
		unix.RENAME_EXCL,
	)
}
