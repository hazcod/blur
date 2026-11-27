package client

import "testing"

func TestDisplayAppNameHandlesMissingAndIncompleteIdentifiers(t *testing.T) {
	for _, id := range []string{"", "com.example."} {
		t.Run(id, func(t *testing.T) {
			if got := displayAppName(id); got != "Unknown app" {
				t.Fatalf("missing app metadata was not labeled: %q", got)
			}
		})
	}
}
