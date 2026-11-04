// Package securestore defines the data-key lifecycle over the platform secret port.
package securestore

import (
	"context"
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

// creationGate serializes Get-then-Put within this process. The future OS
// backend or core single-instance gate must provide atomic conditional creation
// across processes; this primitive port cannot guarantee that on its own.
var creationGate = make(chan struct{}, 1)

// ResolveKey obtains or creates a 32-byte DEK. It never overwrites a found key.
// Production callers must inject crypto/rand.Reader, treat the returned array
// as secret, and never format or log it. SecretStore.Put must synchronously
// copy or consume its input before returning.
func ResolveKey(ctx context.Context, store platform.SecretStore, name string, random io.Reader) ([32]byte, error) {
	var zero [32]byte
	if ctx == nil {
		return zero, &SecretError{State: Unavailable}
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if store == nil || name == "" {
		return zero, &SecretError{State: Unavailable}
	}
	select {
	case creationGate <- struct{}{}:
	case <-ctx.Done():
		return zero, ctx.Err()
	}
	defer func() { <-creationGate }()
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	value, err := store.Get(ctx, name, len(zero))
	if contextErr := ctx.Err(); contextErr != nil {
		return zero, contextErr
	}
	if err == nil {
		if len(value) != len(zero) {
			return zero, &SecretError{State: Corrupt}
		}
		copy(zero[:], value)
		return zero, nil
	}
	state, typed := err.(*SecretError)
	if !typed || state == nil || state.State != NotFound || len(value) != 0 {
		if state != nil && typed {
			switch state.State {
			case Locked, Corrupt, Denied, Unavailable:
				return zero, &SecretError{State: state.State}
			}
		}
		return zero, &SecretError{State: Unavailable}
	}
	if random == nil {
		return zero, &SecretError{State: Unavailable}
	}
	if _, err := io.ReadFull(random, zero[:]); err != nil {
		clear(zero[:])
		return zero, &SecretError{State: Unavailable}
	}
	if err := ctx.Err(); err != nil {
		clear(zero[:])
		return [32]byte{}, err
	}
	// Put receives a copy so a backend retaining its argument cannot alias the
	// returned key. This copy is cleared as soon as Put returns.
	putValue := make([]byte, len(zero))
	copy(putValue, zero[:])
	putErr := store.Put(ctx, name, putValue)
	clear(putValue)
	if err := ctx.Err(); err != nil {
		clear(zero[:])
		return [32]byte{}, err
	}
	if putErr != nil {
		clear(zero[:])
		return [32]byte{}, &SecretError{State: Unavailable}
	}
	return zero, nil
}
