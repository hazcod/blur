package main

import (
	"context"

	"github.com/hazcod/blur/internal/client"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/mcpserver"
	"github.com/hazcod/blur/internal/platform"
)

func mcpClientFromSecret(ctx context.Context, secrets platform.SecretStore, transport localapi.LocalTransport, endpoint localapi.EndpointDescriptor) (*localapi.RPCClient, error) {
	if ctx == nil || ctx.Err() != nil || secrets == nil || transport == nil || endpoint.Validate() != nil {
		return nil, client.ErrNotConfigured
	}
	data, err := secrets.Get(ctx, mcpserver.GrantSecretName, 2048)
	if err != nil {
		return nil, client.ErrNotConfigured
	}
	grant, err := localapi.DecodeIssuedGrant(data)
	clear(data)
	if err != nil {
		return nil, client.ErrNotConfigured
	}
	rpc := localapi.NewRPCClient(transport, endpoint, grant)
	clear(grant.Credential)
	return rpc, nil
}
