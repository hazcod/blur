//go:build !darwin && !linux && !windows

package client

import "github.com/hazcod/blur/internal/platform"

func newMCPPreferenceControl(_ string, _ platform.SecretStore, _ *Client) (MCPPreferenceControl, error) {
	return unavailableMCPControl{}, nil
}
