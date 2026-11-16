package client

import (
	"errors"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/storage/durable"
)

// The stage is a fixed application label, never a path or backend message.
// Preserve typed causes for recovery without exposing their raw error text.
type storageStartupError struct {
	stage string
	cause error
}

func (e *storageStartupError) Error() string { return "local memory startup: " + e.stage }
func (e *storageStartupError) Unwrap() error { return e.cause }

func storageStartupFailure(stage string, cause error) error {
	return &storageStartupError{stage: stage, cause: cause}
}

func storageStartupStage(err error) string {
	var failure *storageStartupError
	if errors.As(err, &failure) && failure != nil {
		switch failure.stage {
		case "configuration_directory", "storage_directory", "instance_lock", "database_probe", "encryption_key", "database_open":
			return failure.stage
		}
	}
	return "unknown"
}

// SetupIssueMessage returns a user-safe recovery hint for startup failures.
// It deliberately does not include wrapped errors, paths, or backend details.
func SetupIssueMessage(err error) string {
	if errors.Is(err, durable.ErrUnsupportedSchema) {
		return "This memory was saved by a newer version of Blur. Open the latest Blur app to use it. Your encrypted memory has not been changed."
	}
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
	switch storageStartupStage(err) {
	case "configuration_directory", "storage_directory":
		return "Blur could not access its local storage folder. Check disk and folder access, then quit and reopen Blur."
	case "instance_lock":
		return "Blur could not lock its local memory for use. Close any other Blur instance, then reopen this app."
	case "database_probe", "database_open":
		return "Blur could not open its local memory file. Check disk access and available space, then quit and reopen Blur."
	}
	return "Local encrypted storage could not be opened. Blur did not start capture or save anything. Check disk access and Keychain availability, then quit and reopen Blur."
}
