//go:build !darwin && !windows && !linux

package power

import "context"

func (nativeSystemStateSource) ReadSystemState(ctx context.Context) (SystemState, error) {
	if ctx != nil && ctx.Err() != nil {
		return SystemState{}, ctx.Err()
	}
	return SystemState{}, ErrSystemStateUnavailable
}
