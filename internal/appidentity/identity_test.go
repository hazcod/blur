package appidentity

import (
	"github.com/hazcod/blur/internal/acceptance"
	"testing"
)

func TestProductBundleIdentifiers(t *testing.T) {
	want := "com.hazcod.blur"
	if acceptance.Enabled {
		want += ".acceptance"
	}
	if BundleID != want {
		t.Fatalf("BundleID = %q, want %q", BundleID, want)
	}
	if SafariExtensionBundleID != want+".safaricapture" {
		t.Fatalf("SafariExtensionBundleID = %q, want %q", SafariExtensionBundleID, want+".safaricapture")
	}
}
