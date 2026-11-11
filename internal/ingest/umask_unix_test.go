//go:build unix

package ingest

import (
	"os"
	"syscall"
	"testing"
)

// TestMain tightens the umask so t.TempDir() creates private (0700)
// directories. The encrypted store refuses a parent directory that other
// users can read, and CI runners default to umask 022.
func TestMain(m *testing.M) {
	syscall.Umask(0o077)
	os.Exit(m.Run())
}
