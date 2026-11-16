package assistant

import (
	"context"
	"encoding/json"
	"github.com/hazcod/blur/internal/model"
	"testing"
)

type assistantCountPorts struct{ request model.Request }

func (c *assistantCountPorts) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, nil
}
func (c *assistantCountPorts) CountInputTokens(context.Context, string, string) (int, error) {
	return 10, nil
}
func (c *assistantCountPorts) CountRequestTokens(_ context.Context, request model.Request) (int, error) {
	c.request = request
	return 37, nil
}
func (c *assistantCountPorts) ContextTokenLimit(context.Context) (int, error) { return 4096, nil }
func (c *assistantCountPorts) CloseIdleConnections()                          {}
func (c *assistantCountPorts) Identity(context.Context) (string, error)       { return "synthetic", nil }

func TestAssistantMemoryCountIncludesSchemaAndGeneralRemainsPlain(t *testing.T) {
	client := &assistantCountPorts{}
	generator := &OllamaGenerator{client: client, schema: json.RawMessage(appleAnswerSchema)}
	user := `{"evidence":[{"text":"synthetic source"}]}`
	expectedSchema, schemaErr := appleGroundedSchema()
	if schemaErr != nil {
		t.Fatal(schemaErr)
	}
	count, err := generator.CountMemoryInputTokens(context.Background(), "policy", user)
	if err != nil || count != 37 || client.request.Format != "" || string(client.request.Schema) != string(expectedSchema) || client.request.System != "policy" || client.request.User != user {
		t.Fatalf("memory count=%d request=%#v error=%v", count, client.request, err)
	}
	count, err = generator.CountInputTokens(context.Background(), "policy", "synthetic source")
	if err != nil || count != 10 {
		t.Fatalf("plain count=%d error=%v", count, err)
	}
	capacity, err := generator.ContextTokenLimit(context.Background())
	if err != nil || capacity != 4096 {
		t.Fatalf("capacity=%d error=%v", capacity, err)
	}
}
