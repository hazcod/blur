//go:build !darwin && !linux && !windows

package bolt

import "os"

func openSecureDirectory(string) (secureDirectory, error) { return nil, ErrUnavailable }
func createPrivateDirectory(string) error                 { return ErrUnavailable }

type unavailableDirectory struct{}

func (unavailableDirectory) OpenFile(string, int, os.FileMode) (*os.File, error) {
	return nil, ErrUnavailable
}
func (unavailableDirectory) Close() error { return nil }
