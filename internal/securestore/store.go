// Package securestore defines the data-key lifecycle over the platform secret port.
package securestore

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"

	"github.com/hazcod/blur/internal/platform"
)

// SecretState and SecretError alias the shared platform types so OS adapters
// can report typed outcomes without importing this higher-level package.
type SecretState = platform.SecretState
type SecretError = platform.SecretStoreError

const (
	Found       SecretState = platform.SecretFound
	NotFound    SecretState = platform.SecretNotFound
	Locked      SecretState = platform.SecretLocked
	Corrupt     SecretState = platform.SecretCorrupt
	Denied      SecretState = platform.SecretDenied
	Unavailable SecretState = platform.SecretUnavailable
	Ambiguous   SecretState = platform.SecretAmbiguous
)

// Key is an opaque DEK. Explicit Bytes access returns a copy for cryptographic
// operations; ordinary formatting and encoding cannot disclose its contents.
type Key struct{ bytes [32]byte }

// OfflineFixtureKey returns a fixed, publicly known key for the explicitly
// fixture-only CLI. It must never be used for personal or production stores.
// Production callers must obtain a key through ResolveKey and an OS secret port.
func OfflineFixtureKey() Key {
	return Key{bytes: [32]byte{0x62, 0x6c, 0x75, 0x72, 0x2d, 0x6f, 0x66, 0x66, 0x6c, 0x69, 0x6e, 0x65, 0x2d, 0x66, 0x69, 0x78, 0x74, 0x75, 0x72, 0x65, 0x2d, 0x6f, 0x6e, 0x6c, 0x79, 0x2d, 0x6e, 0x6f, 0x2d, 0x70, 0x72, 0x64}}
}

var ErrKeySerialization = errors.New("securestore: key serialization disabled")

func (Key) String() string               { return "securestore.Key([redacted])" }
func (Key) GoString() string             { return "securestore.Key([redacted])" }
func (Key) MarshalJSON() ([]byte, error) { return nil, ErrKeySerialization }
func (Key) MarshalText() ([]byte, error) { return nil, ErrKeySerialization }
func (k Key) Bytes() [32]byte            { return k.bytes }
func (k Key) Equal(other Key) bool {
	return subtle.ConstantTimeCompare(k.bytes[:], other.bytes[:]) == 1
}

// creationGate serializes Get-then-Put within this process. The future OS
// backend or core single-instance gate must provide atomic conditional creation
// across processes; this primitive port cannot guarantee that on its own.
var creationGate = make(chan struct{}, 1)

// ResolveKey obtains or creates a 32-byte DEK. It never overwrites a found key.
// Production callers must inject crypto/rand.Reader and limit calls to Bytes.
// SecretStore.Put must synchronously copy its input without mutating it.
func ResolveKey(ctx context.Context, store platform.SecretStore, name string, random io.Reader) (Key, error) {
	var zero [32]byte
	if ctx == nil {
		return Key{}, &SecretError{State: Unavailable}
	}
	if err := ctx.Err(); err != nil {
		return Key{}, err
	}
	if store == nil || name == "" {
		return Key{}, &SecretError{State: Unavailable}
	}
	select {
	case creationGate <- struct{}{}:
	case <-ctx.Done():
		return Key{}, ctx.Err()
	}
	defer func() { <-creationGate }()
	if err := ctx.Err(); err != nil {
		return Key{}, err
	}
	resolvedKey, err := loadKey(ctx, store, name)
	if err == nil {
		return resolvedKey, nil
	}
	state, typed := err.(*SecretError)
	if !typed || state == nil {
		return Key{}, &SecretError{State: Unavailable}
	}
	if state.State != NotFound {
		switch state.State {
		case Locked, Corrupt, Denied, Unavailable, Ambiguous:
			return Key{}, &SecretError{State: state.State, Operation: "read", BackendCode: state.BackendCode}
		default:
			return Key{}, &SecretError{State: Unavailable}
		}
	}
	if random == nil {
		return Key{}, &SecretError{State: Unavailable}
	}
	if _, err := io.ReadFull(random, zero[:]); err != nil {
		clear(zero[:])
		return Key{}, &SecretError{State: Unavailable}
	}
	if err := ctx.Err(); err != nil {
		clear(zero[:])
		return Key{}, err
	}
	// Put receives a separate temporary buffer. The port must copy it before
	// returning; we clear it immediately after checking for mutation.
	putValue := make([]byte, len(zero))
	copy(putValue, zero[:])
	putErr := store.Put(ctx, name, putValue)
	putUnchanged := subtle.ConstantTimeCompare(putValue, zero[:]) == 1
	clear(putValue)
	if err := ctx.Err(); err != nil {
		clear(zero[:])
		return Key{}, err
	}
	if putErr != nil {
		clear(zero[:])
		var putState *SecretError
		if errors.As(putErr, &putState) && putState != nil {
			switch putState.State {
			case Locked, Corrupt, Denied, Unavailable, Ambiguous:
				return Key{}, &SecretError{State: putState.State, Operation: "create", BackendCode: putState.BackendCode}
			}
		}
		return Key{}, &SecretError{State: Unavailable, Operation: "create"}
	}
	if !putUnchanged {
		clear(zero[:])
		return Key{}, &SecretError{State: Corrupt}
	}
	stored, readErr := store.Get(ctx, name, len(zero))
	if err := ctx.Err(); err != nil {
		clear(stored)
		clear(zero[:])
		return Key{}, err
	}
	if readErr != nil {
		clear(stored)
		clear(zero[:])
		state, typed := readErr.(*SecretError)
		if typed && state != nil {
			switch state.State {
			case Locked, Corrupt, Denied, Unavailable, Ambiguous:
				return Key{}, &SecretError{State: state.State, Operation: "verify", BackendCode: state.BackendCode}
			}
		}
		return Key{}, &SecretError{State: Unavailable, Operation: "verify"}
	}
	valid := len(stored) == len(zero) && subtle.ConstantTimeCompare(stored, zero[:]) == 1
	clear(stored)
	if !valid {
		clear(zero[:])
		return Key{}, &SecretError{State: Corrupt}
	}
	key := Key{bytes: zero}
	clear(zero[:])
	return key, nil
}

// LoadKey retrieves a previously stored DEK and never creates or mutates it.
// This is required when an encrypted store already exists: a missing key must
// fail closed rather than being replaced with a new, unusable key.
func LoadKey(ctx context.Context, store platform.SecretStore, name string) (Key, error) {
	return loadKey(ctx, store, name)
}

func loadKey(ctx context.Context, store platform.SecretStore, name string) (Key, error) {
	if ctx == nil {
		return Key{}, &SecretError{State: Unavailable}
	}
	if err := ctx.Err(); err != nil {
		return Key{}, err
	}
	if store == nil || name == "" {
		return Key{}, &SecretError{State: Unavailable}
	}
	var expected [32]byte
	value, err := store.Get(ctx, name, len(expected))
	if contextErr := ctx.Err(); contextErr != nil {
		clear(value)
		return Key{}, contextErr
	}
	if err != nil {
		valueLen := len(value)
		clear(value)
		state, typed := err.(*SecretError)
		if typed && state != nil {
			switch state.State {
			case NotFound:
				if valueLen != 0 {
					return Key{}, &SecretError{State: Unavailable}
				}
				return Key{}, &SecretError{State: NotFound}
			case Locked, Corrupt, Denied, Unavailable, Ambiguous:
				return Key{}, &SecretError{State: state.State, BackendCode: state.BackendCode}
			}
		}
		return Key{}, &SecretError{State: Unavailable}
	}
	if len(value) != len(expected) {
		clear(value)
		return Key{}, &SecretError{State: Corrupt}
	}
	var key Key
	copy(key.bytes[:], value)
	clear(value)
	return key, nil
}
