//go:build darwin || linux

package bolt

import (
	"os"

	"golang.org/x/sys/unix"
)

func validatePrivateFile(file *os.File) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return ErrUnavailable
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != os.Getuid() || stat.Mode&0o077 != 0 {
		return ErrInvalid
	}
	return nil
}
