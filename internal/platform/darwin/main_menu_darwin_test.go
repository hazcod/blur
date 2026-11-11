package darwin

import "testing"

func TestInstallApplicationMenuCreatesNativeAboutAndQuitItems(t *testing.T) {
	t.Skip("AppKit mutation requires the native main run loop; descriptors are covered by unit tests")
}
