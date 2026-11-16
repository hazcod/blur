//go:build windows

package bolt

import (
	"os"

	"golang.org/x/sys/windows"
)

func validatePrivateFile(file *os.File) error {
	return verifyWindowsObject(windows.Handle(file.Fd()), false, true)
}
