package bolt

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// CreatePrivateTempFile creates an empty file with private permissions before
// callers can write data. Parent traversal and file creation use pinned handles;
// Windows files receive an explicit protected current-user-only ACL.
func CreatePrivateTempFile(directory, prefix string) (*os.File, string, error) {
	if prefix == "" || strings.ContainsAny(prefix, `/\:*`) {
		return nil, "", ErrInvalid
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, "", ErrInvalid
	}
	parent, err := openDirectoryPath(absolute, false)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = parent.Close() }()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, "", ErrUnavailable
	}
	name := prefix + hex.EncodeToString(nonce[:]) + ".tmp"
	file, err := parent.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, "", err
	}
	return file, filepath.Join(absolute, name), nil
}

// ValidatePrivateFile verifies the open regular file's owner and private access
// policy. It never substitutes Unix mode bits for a Windows ACL check.
func ValidatePrivateFile(file *os.File) error {
	if file == nil {
		return ErrInvalid
	}
	return validatePrivateFile(file)
}
