package bolt

import (
	"os"
)

type secureDirectory interface {
	OpenFile(name string, flag int, mode os.FileMode) (*os.File, error)
	Close() error
}

// CreatePrivateDirectory creates path with owner-only access and verifies the
// resulting directory from its open handle. Existing paths are accepted only
// when they already meet the platform's private-directory policy.
func CreatePrivateDirectory(path string) error {
	return createPrivateDirectory(path)
}
