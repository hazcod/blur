//go:build !darwin || !blur_acceptance

// Package appidentity defines stable identifiers shared across Blur components.
package appidentity

const (
	BundleID                = "com.hazcod.blur"
	DisplayName             = "Blur"
	SafariExtensionBundleID = BundleID + ".safaricapture"
)
