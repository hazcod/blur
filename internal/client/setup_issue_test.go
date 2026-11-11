package client

import (
	"errors"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/platform"
)

func TestSetupIssueExplainsSafeRecoveryWithoutLeakingUnderlyingError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"missing key", ErrMissingDataKey, "key for existing memory is missing"},
		{"corrupt database", ErrEncryptedStoreCorrupt, "database"},
		{"locked keychain", &platform.SecretStoreError{State: platform.SecretLocked}, "Keychain is locked"},
		{"denied keychain", &platform.SecretStoreError{State: platform.SecretDenied}, "encryption key was denied"},
		{"unavailable keychain", &platform.SecretStoreError{State: platform.SecretUnavailable}, "Keychain is unavailable"},
		{"corrupt keychain item", &platform.SecretStoreError{State: platform.SecretCorrupt}, "Keychain key is unreadable"},
		{"unknown error", errors.New("sensitive /Users/alice/Library/Application Support/Blur/memory.db details"), "Local encrypted storage could not be opened"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := SetupIssueMessage(test.err)
			if !strings.Contains(got, test.want) {
				t.Fatalf("SetupIssueMessage() = %q, want substring %q", got, test.want)
			}
			if strings.Contains(got, "/Users/") || strings.Contains(got, "alice") {
				t.Fatalf("SetupIssueMessage() leaked underlying error: %q", got)
			}
		})
	}
}
