//go:build darwin

package client

import "github.com/hazcod/blur/internal/platform/darwin"

func safariPageCaptureBuildConfigured() bool {
	return darwin.SafariAppGroupID != "" && darwin.SafariKeychainAccessGroup != ""
}

func supportsSafariPageCaptureSetting() bool { return true }
