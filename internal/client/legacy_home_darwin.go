//go:build darwin

package client

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/ebitengine/purego"
)

// legacyDataDirectory returns <real home>/Library/Application Support/Blur.
// Inside the sandbox HOME is the container, and cgo-free os/user falls back to
// $HOME, so the real home comes from getpwuid (the passwd database is not
// remapped by the sandbox), then from the container path itself.
func legacyDataDirectory() string {
	home := realUserHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "Blur")
}

func realUserHome() string {
	if u, err := user.Current(); err == nil && u.HomeDir != "" && !strings.Contains(u.HomeDir, "/Library/Containers/") {
		return u.HomeDir
	}
	if home := passwdHome(); home != "" && !strings.Contains(home, "/Library/Containers/") {
		return home
	}
	containerHome, _ := os.UserHomeDir()
	return legacyHomeFromContainer(containerHome)
}

// passwdHome reads pw_dir from getpwuid(getuid()) in libSystem.
func passwdHome() string {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return ""
	}
	var getpwuid func(uid uint32) unsafe.Pointer
	purego.RegisterLibFunc(&getpwuid, lib, "getpwuid")
	entry := getpwuid(uint32(os.Getuid()))
	if entry == nil {
		return ""
	}
	// struct passwd (Darwin): pw_dir is the char* at offset 48.
	dir := *(**byte)(unsafe.Add(entry, 48))
	if dir == nil {
		return ""
	}
	var out []byte
	for i := 0; i < 4096; i++ {
		b := *(*byte)(unsafe.Add(unsafe.Pointer(dir), i))
		if b == 0 {
			break
		}
		out = append(out, b)
	}
	return string(out)
}
