//go:build !darwin && !linux

package probe

import (
	"errors"
	"fmt"
)

func validateArtifactPublicationPlatform() error {
	return fmt.Errorf("atomic artifact publication: %w", errors.ErrUnsupported)
}

func renameArtifactDirectoryNoReplace(string, string) error {
	return fmt.Errorf("atomic artifact publication without replacement: %w", errors.ErrUnsupported)
}
