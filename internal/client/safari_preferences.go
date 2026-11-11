package client

func shouldRunSafariPageCapture(globalCapture, safariOptIn, buildConfigured bool) bool {
	return globalCapture && safariOptIn && buildConfigured
}

func safariPageCaptureToggleAllowed(currentlyEnabled, buildConfigured bool) bool {
	return currentlyEnabled || buildConfigured
}
