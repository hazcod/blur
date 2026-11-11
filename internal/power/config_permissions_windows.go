//go:build windows

package power

import "os"

// The per-user configuration directory is created with a current-user-only
// Windows ACL by the bootstrap. Files created there inherit that ACL; Windows
// does not expose meaningful Unix permission bits through FileMode.
func privateConfigFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular()
}
