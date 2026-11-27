//go:build linux

package replica

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func renameExclusive(from, to string) error {
	err := unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
		return os.Link(from, to)
	}
	return err
}
