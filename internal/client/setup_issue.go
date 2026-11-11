package client

import (
	"errors"

	"github.com/hazcod/blur/internal/platform"
)

// SetupIssueMessage returns a user-safe recovery hint for startup failures.
// It deliberately does not include wrapped errors, paths, or backend details.
func SetupIssueMessage(err error) string {
	if errors.Is(err, ErrEncryptedStoreCorrupt) {
		return "Blur could not verify the encrypted memory database. It has not been repaired or replaced; restore a known-good backup before continuing."
	}
	if errors.Is(err, ErrMissingDataKey) {
		return "The encryption key for existing memory is missing. Blur did not decrypt, modify, or replace that data. Restore the key from your login Keychain backup before continuing; do not reset it."
	}
	var secretErr *platform.SecretStoreError
	if errors.As(err, &secretErr) && secretErr != nil {
		switch secretErr.State {
		case platform.SecretLocked:
			return "Your login Keychain is locked. Unlock it, then quit and reopen Blur."
		case platform.SecretDenied:
			return "Access to Blur’s encryption key was denied. Check the app’s Keychain access, then quit and reopen Blur."
		case platform.SecretCorrupt:
			return "Blur’s Keychain key is unreadable. Existing encrypted memory was left untouched; restore the key before continuing."
		case platform.SecretNotFound:
			return "Blur’s encryption key is missing. Existing encrypted memory was not decrypted or modified; restore the key before continuing."
		case platform.SecretUnavailable, platform.SecretAmbiguous:
			return "The login Keychain is unavailable. Check that it is unlocked, then quit and reopen Blur."
		}
	}
	return "Local encrypted storage could not be opened. Blur did not start capture or save anything. Check disk access and Keychain availability, then quit and reopen Blur."
}
