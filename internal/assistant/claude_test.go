package assistant

import (
	"context"
	"testing"
)

func TestClaudeGeneratorUsesExplicitCloudIdentityWithoutSendingPrompt(t *testing.T) {
	if _, err := NewClaudeGenerator("claude-test", "", ""); err == nil {
		t.Fatal("Claude configured without an API credential")
	}
	g, err := NewClaudeGenerator("claude-test", "synthetic-api-key", "wrkspc_test")
	if err != nil {
		t.Fatal(err)
	}
	defer g.CloseIdleConnections()
	if identity, err := g.client.Identity(context.Background()); err != nil || identity != "anthropic:claude-test" || len(g.schema) == 0 {
		t.Fatalf("wrong completion identity/schema: %q %v", identity, err)
	}
}
