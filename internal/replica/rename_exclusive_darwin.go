//go:build darwin

package replica

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func renameExclusive(from, to string) error {
	err := unix.RenameatxNp(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_EXCL)
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
		return os.Link(from, to)
	}
	return err
}
