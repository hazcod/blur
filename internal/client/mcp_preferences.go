package client

import "context"

type unavailableMCPControl struct{}

func (unavailableMCPControl) SetEnabled(_ context.Context, enabled bool) error {
	if enabled {
		return ErrNotConfigured
	}
	return nil
}
func (unavailableMCPControl) Enabled() bool { return false }
func (unavailableMCPControl) Close() error  { return nil }

// MCPPreferenceControl manages the read-only local Claude bridge lifecycle.
type MCPPreferenceControl interface {
	SetEnabled(context.Context, bool) error
	Enabled() bool
	Close() error
}

func (c *Client) WithMCPControl(control MCPPreferenceControl) *Client {
	if c != nil {
		c.mcpControl = control
	}
	return c
}

func (c *Client) ClaudeMCPAvailable() bool { return c != nil && c.mcpControl != nil }

func (c *Client) ClaudeMCPRunning() bool {
	return c != nil && c.mcpControl != nil && c.mcpControl.Enabled()
}

// RestoreClaudeMCP starts the saved opt-in at app launch. A false setting is
// intentionally a no-op, including no Keychain lookup.
func (c *Client) RestoreClaudeMCP(ctx context.Context, enabled bool) error {
	if !enabled {
		return nil
	}
	if c == nil || c.mcpControl == nil || ctx == nil {
		return ErrNotConfigured
	}
	return c.mcpControl.SetEnabled(ctx, true)
}
