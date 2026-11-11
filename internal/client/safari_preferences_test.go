package client

import "testing"

func TestShouldRunSafariPageCaptureRequiresAllGates(t *testing.T) {
	tests := []struct {
		name                        string
		global, optedIn, configured bool
		want                        bool
	}{
		{name: "all enabled", global: true, optedIn: true, configured: true, want: true},
		{name: "global capture paused", optedIn: true, configured: true},
		{name: "Safari opt-in absent", global: true, configured: true},
		{name: "build not configured", global: true, optedIn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRunSafariPageCapture(tt.global, tt.optedIn, tt.configured); got != tt.want {
				t.Fatalf("shouldRunSafariPageCapture(%t, %t, %t) = %t, want %t", tt.global, tt.optedIn, tt.configured, got, tt.want)
			}
		})
	}
}

func TestSafariPageCaptureToggleOnlyEnablesConfiguredBuilds(t *testing.T) {
	tests := []struct {
		name, currentlyEnabled string
		configured             bool
		want                   bool
	}{
		{name: "configured can enable", configured: true, want: true},
		{name: "unconfigured can remain disabled", configured: false},
		{name: "unconfigured can turn persisted opt-in off", currentlyEnabled: "yes", configured: false, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := safariPageCaptureToggleAllowed(tt.currentlyEnabled == "yes", tt.configured); got != tt.want {
				t.Fatalf("safariPageCaptureToggleAllowed(%t, %t) = %t, want %t", tt.currentlyEnabled == "yes", tt.configured, got, tt.want)
			}
		})
	}
}
