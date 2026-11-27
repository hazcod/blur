//go:build !darwin && !linux

package replica

import "os"

func renameExclusive(from, to string) error { return os.Link(from, to) }
