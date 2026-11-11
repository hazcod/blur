package capture

import "github.com/hazcod/blur/internal/browserprivacy"

// isPrivateBrowserWindowTitle recognizes private-mode markers emitted in
// browser accessibility window titles. The title is examined only in the
// host capture stage and is never persisted or logged.
func isPrivateBrowserWindowTitle(title string) bool {
	return browserprivacy.IsPrivateWindowTitle(title)
}
