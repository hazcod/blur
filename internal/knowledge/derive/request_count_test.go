package derive

import (
	"context"
	"github.com/hazcod/blur/internal/model"
	"strings"
	"testing"
)

type deriveCountPorts struct{ request model.Request }

func (c *deriveCountPorts) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, nil
}
func (c *deriveCountPorts) CountInputTokens(context.Context, string, string) (int, error) {
	return 10, nil
}
func (c *deriveCountPorts) CountRequestTokens(_ context.Context, request model.Request) (int, error) {
	c.request = request
	return 37, nil
}
func (c *deriveCountPorts) ContextTokenLimit(context.Context) (int, error) { return 4096, nil }
func (c *deriveCountPorts) CloseIdleConnections()                          {}
func (c *deriveCountPorts) Identity(context.Context) (string, error)       { return "synthetic", nil }

func TestDeriveCountIncludesPromptAndRuntimeCapacity(t *testing.T) {
	client := &deriveCountPorts{}
	generator := newAppleGenerator(client)
	count, err := generator.CountInputTokens(context.Background(), []Evidence{{SourceID: sourceID('a'), Text: "Mira owns rollback."}})
	if err != nil || count != 37 || client.request.Format != "" || len(client.request.Schema) != 0 || client.request.System != systemPrompt || !strings.Contains(client.request.User, `"source_id":"e1"`) {
		t.Fatalf("count=%d request=%#v error=%v", count, client.request, err)
	}
	capacity, err := generator.ContextTokenLimit(context.Background())
	if err != nil || capacity != 4096 {
		t.Fatalf("capacity=%d error=%v", capacity, err)
	}
}
