package notes

import (
	"context"
	"testing"
)

func TestClaudeExtractorUsesExplicitCloudIdentity(t *testing.T) {
	if _, err := NewClaudeExtractor("claude-test", "", ""); err == nil {
		t.Fatal("Claude configured without an API credential")
	}
	e, err := NewClaudeExtractor("claude-test", "synthetic-api-key", "wrkspc_test")
	if err != nil {
		t.Fatal(err)
	}
	defer e.CloseIdleConnections()
	if identity, err := e.client.Identity(context.Background()); err != nil || identity != "anthropic:claude-test" {
		t.Fatalf("wrong completion identity: %q %v", identity, err)
	}
}
