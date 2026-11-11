//go:build windows

package preferences

import "os"

// The settings file inherits the per-user ACL of the bootstrap directory.
func privateSettingsFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular()
}
