package appidentity

import "testing"

func TestProductBundleIdentifiers(t *testing.T) {
	if BundleID != "com.hazcod.blur" {
		t.Fatalf("BundleID = %q, want com.hazcod.blur", BundleID)
	}
	if SafariExtensionBundleID != "com.hazcod.blur.safaricapture" {
		t.Fatalf("SafariExtensionBundleID = %q, want com.hazcod.blur.safaricapture", SafariExtensionBundleID)
	}
}
