//go:build windows

package client

import (
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/mcpserver"
	"github.com/hazcod/blur/internal/platform"
)

func newMCPPreferenceControl(directory string, secrets platform.SecretStore, client *Client) (MCPPreferenceControl, error) {
	if directory == "" || secrets == nil || client == nil {
		return nil, ErrNotConfigured
	}
	endpoint, err := localapi.CurrentUserNamedPipeEndpoint()
	if err != nil || endpoint.Validate() != nil {
		return nil, ErrNotConfigured
	}
	return mcpserver.NewLocalOwner(secrets, localapi.NewNamedPipeTransport(32<<10), endpoint, client.HandleMCPRead), nil
}
