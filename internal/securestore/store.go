// Package securestore defines the data-key lifecycle over the platform secret port.
package securestore

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"

	"github.com/nielsgroen/blur/internal/platform"
)

// SecretState is a closed set. A successful Get with a 32-byte value means Found;
// a typed NotFound error is the sole authorization to create a key.
type SecretState string

const (
	Found       SecretState = "found"
	NotFound    SecretState = "not_found"
	Locked      SecretState = "locked"
	Corrupt     SecretState = "corrupt"
	Denied      SecretState = "denied"
	Unavailable SecretState = "unavailable"
)

// SecretError intentionally contains no backend cause or key material.
type SecretError struct{ State SecretState }

func (e *SecretError) Error() string { return "securestore: " + string(e.State) }

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
	value, err := store.Get(ctx, name, len(zero))
	if contextErr := ctx.Err(); contextErr != nil {
		return Key{}, contextErr
	}
	if err == nil {
		if len(value) != len(zero) {
			return Key{}, &SecretError{State: Corrupt}
		}
		var key Key
		copy(key.bytes[:], value)
		return key, nil
	}
	state, typed := err.(*SecretError)
	if !typed || state == nil || state.State != NotFound || len(value) != 0 {
		if state != nil && typed {
			switch state.State {
			case Locked, Corrupt, Denied, Unavailable:
				return Key{}, &SecretError{State: state.State}
			}
		}
		return Key{}, &SecretError{State: Unavailable}
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
		return Key{}, &SecretError{State: Unavailable}
	}
	if !putUnchanged {
		clear(zero[:])
		return Key{}, &SecretError{State: Corrupt}
	}
	stored, readErr := store.Get(ctx, name, len(zero))
	if err := ctx.Err(); err != nil {
		clear(zero[:])
		return Key{}, err
	}
	if readErr != nil {
		clear(zero[:])
		state, typed := readErr.(*SecretError)
		if typed && state != nil {
			switch state.State {
			case Locked, Corrupt, Denied, Unavailable:
				return Key{}, &SecretError{State: state.State}
			}
		}
		return Key{}, &SecretError{State: Unavailable}
	}
	if len(stored) != len(zero) || subtle.ConstantTimeCompare(stored, zero[:]) != 1 {
		clear(zero[:])
		return Key{}, &SecretError{State: Corrupt}
	}
	key := Key{bytes: zero}
	clear(zero[:])
	return key, nil
}
