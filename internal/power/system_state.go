package power

import (
	"context"
	"errors"
)

var ErrSystemStateUnavailable = errors.New("power: system power state unavailable")

// SystemStateSource is deliberately read-only. Implementations read only the
// OS saver signal and expose no battery percentage, device identity, or text.
type SystemStateSource interface {
	ReadSystemState(context.Context) (SystemState, error)
}

type nativeSystemStateSource struct{}

func NewSystemStateSource() SystemStateSource { return nativeSystemStateSource{} }
