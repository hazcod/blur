//go:build darwin || linux

package client

import (
	"path/filepath"

	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/mcpserver"
	"github.com/hazcod/blur/internal/platform"
)

func newMCPPreferenceControl(directory string, secrets platform.SecretStore, client *Client) (MCPPreferenceControl, error) {
	if directory == "" || secrets == nil || client == nil {
		return nil, ErrNotConfigured
	}
	endpoint := localapi.EndpointDescriptor{Kind: localapi.EndpointUnix, Address: MCPSocketAddress(directory)}
	if endpoint.Validate() != nil || prepareMCPSocketDir(filepath.Dir(endpoint.Address)) != nil {
		return nil, ErrNotConfigured
	}
	return mcpserver.NewLocalOwner(secrets, localapi.NewUnixTransport(32<<10), endpoint, client.HandleMCPRead), nil
}
