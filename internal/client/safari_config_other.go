//go:build !darwin

package client

func safariPageCaptureBuildConfigured() bool { return false }

func supportsSafariPageCaptureSetting() bool { return false }
