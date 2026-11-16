package client

import (
	"errors"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestSetupIssueExplainsSafeRecoveryWithoutLeakingUnderlyingError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"missing key", ErrMissingDataKey, "key for existing memory is missing"},
		{"corrupt database", ErrEncryptedStoreCorrupt, "database"},
		{"newer database", classifyStoreOpenError(durable.ErrUnsupportedSchema), "newer version of Blur"},
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

func TestStartupErrorIsVisibleWithCopyableSanitizedDiagnostics(t *testing.T) {
	err := errors.Join(errors.New("secret captured text /Users/alice/private"), &platform.SecretStoreError{State: platform.SecretDenied, Operation: "get", BackendCode: -25293})
	d := &desktop{}
	d.setStartupIssue(err)
	if !strings.Contains(d.startupIssue, "encryption key was denied") || !strings.Contains(d.startupDiagnostic, "-25293") {
		t.Fatalf("startup recovery lost its reason or diagnostics: %q %q", d.startupIssue, d.startupDiagnostic)
	}
	if strings.Contains(d.startupDiagnostic, "secret captured") || strings.Contains(d.startupDiagnostic, "/Users/") {
		t.Fatal("copyable startup diagnostic contains private underlying error")
	}
	if !strings.Contains(d.status, "encryption key was denied") {
		t.Fatal("status still hides the startup error")
	}
}
