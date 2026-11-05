//go:build darwin || linux

package bolt

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type unixSecureDirectory struct {
	file *os.File
}

func openSecureDirectory(path string) (secureDirectory, error) {
	return openDirectoryPath(path, true)
}

func openDirectoryPath(path string, requirePrivate bool) (*unixSecureDirectory, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrInvalid
	}
	absolute = filepath.Clean(absolute)
	// Resolve aliases in ancestors (such as macOS /var) while leaving the
	// final component untouched so O_NOFOLLOW rejects a symlinked store root.
	if absolute != string(filepath.Separator) {
		resolvedParent, resolveErr := filepath.EvalSymlinks(filepath.Dir(absolute))
		if resolveErr != nil {
			return nil, ErrUnavailable
		}
		absolute = filepath.Join(resolvedParent, filepath.Base(absolute))
	}
	rootFD, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	currentFD := rootFD
	parts := strings.Split(strings.TrimPrefix(absolute, string(filepath.Separator)), string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			_ = unix.Close(currentFD)
			return nil, ErrInvalid
		}
		nextFD, openErr := unix.Openat(currentFD, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if currentFD != rootFD {
			_ = unix.Close(currentFD)
		}
		if openErr != nil {
			_ = unix.Close(rootFD)
			if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) {
				return nil, ErrInvalid
			}
			return nil, ErrUnavailable
		}
		currentFD = nextFD
	}
	if currentFD != rootFD {
		_ = unix.Close(rootFD)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(currentFD, &stat); err != nil {
		_ = unix.Close(currentFD)
		return nil, ErrUnavailable
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(currentFD)
		return nil, ErrInvalid
	}
	if requirePrivate && (int(stat.Uid) != os.Getuid() || stat.Mode&0o077 != 0 || stat.Mode&0o700 != 0o700) {
		_ = unix.Close(currentFD)
		return nil, ErrInvalid
	}
	return &unixSecureDirectory{file: os.NewFile(uintptr(currentFD), absolute)}, nil
}

func (directory *unixSecureDirectory) OpenFile(name string, flag int, mode os.FileMode) (*os.File, error) {
	if directory == nil || directory.file == nil || !validBaseName(name) {
		return nil, ErrInvalid
	}
	openFlags := flag | unix.O_CLOEXEC | unix.O_NOFOLLOW
	created := false
	var fd int
	var err error
	if flag&os.O_CREATE != 0 {
		fd, err = unix.Openat(int(directory.file.Fd()), name, openFlags|unix.O_EXCL, uint32(mode.Perm()))
		if errors.Is(err, unix.EEXIST) {
			fd, err = unix.Openat(int(directory.file.Fd()), name, openFlags&^unix.O_CREAT, uint32(mode.Perm()))
		} else if err == nil {
			created = true
		}
	} else {
		fd, err = unix.Openat(int(directory.file.Fd()), name, openFlags, uint32(mode.Perm()))
	}
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EISDIR) || errors.Is(err, unix.ENOTDIR) {
			return nil, ErrInvalid
		}
		return nil, ErrUnavailable
	}
	if created {
		if err := unix.Fchmod(fd, uint32(mode.Perm())); err != nil {
			_ = unix.Close(fd)
			return nil, ErrUnavailable
		}
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, ErrUnavailable
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != os.Getuid() || stat.Mode&0o777 != 0o600 {
		_ = unix.Close(fd)
		return nil, ErrInvalid
	}
	return os.NewFile(uintptr(fd), name), nil
}

func (directory *unixSecureDirectory) Close() error {
	if directory == nil || directory.file == nil {
		return nil
	}
	return directory.file.Close()
}

func createPrivateDirectory(path string) error {
	if path == "" {
		return ErrInvalid
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return ErrInvalid
	}
	absolute = filepath.Clean(absolute)
	name := filepath.Base(absolute)
	if !validBaseName(name) {
		return ErrInvalid
	}
	parent, err := openDirectoryPath(filepath.Dir(absolute), false)
	if err != nil {
		return err
	}
	defer parent.Close()
	created := false
	if err := unix.Mkdirat(int(parent.file.Fd()), name, 0o700); err == nil {
		created = true
	} else if !errors.Is(err, unix.EEXIST) {
		return ErrUnavailable
	}
	fd, err := unix.Openat(int(parent.file.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return ErrInvalid
		}
		return ErrUnavailable
	}
	defer unix.Close(fd)
	if created {
		if err := unix.Fchmod(fd, 0o700); err != nil {
			return ErrUnavailable
		}
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return ErrUnavailable
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || int(stat.Uid) != os.Getuid() || stat.Mode&0o077 != 0 || stat.Mode&0o700 != 0o700 {
		return ErrInvalid
	}
	return nil
}

func validBaseName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, `/\\`)
}
