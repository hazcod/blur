//go:build !darwin && !linux && !windows

package client

import (
	"context"
	"github.com/hazcod/blur/internal/platform"
)

func newMCPPreferenceControl(_ string, _ platform.SecretStore, _ *Client) (MCPPreferenceControl, error) {
	return unavailableMCPControl{}, nil
}

type unavailableMCPControl struct{}

func (unavailableMCPControl) SetEnabled(_ context.Context, enabled bool) error {
	if enabled {
		return ErrNotConfigured
	}
	return nil
}
func (unavailableMCPControl) Enabled() bool { return false }
func (unavailableMCPControl) Close() error  { return nil }
