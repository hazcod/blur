package chronicle

import (
	"context"
	"encoding/json"
	"github.com/hazcod/blur/internal/model"
	"strings"
	"testing"
	"time"
)

type chronicleCountPorts struct{ request model.Request }

func (c *chronicleCountPorts) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, nil
}
func (c *chronicleCountPorts) CountInputTokens(context.Context, string, string) (int, error) {
	return 10, nil
}
func (c *chronicleCountPorts) CountRequestTokens(_ context.Context, request model.Request) (int, error) {
	c.request = request
	return 37, nil
}
func (c *chronicleCountPorts) ContextTokenLimit(context.Context) (int, error) { return 4096, nil }
func (c *chronicleCountPorts) CloseIdleConnections()                          {}
func (c *chronicleCountPorts) Identity(context.Context) (string, error)       { return "synthetic", nil }

func TestChronicleCountIncludesSchemaAndRuntimeCapacity(t *testing.T) {
	client := &chronicleCountPorts{}
	generator := &OllamaGenerator{client: client, schema: json.RawMessage(`{"type":"object"}`)}
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	packed, err := PackHour(Hour{Start: start, End: start.Add(time.Hour), Evidence: []Evidence{evidence(1, start.Add(time.Minute), "synthetic meeting")}})
	if err != nil {
		t.Fatal(err)
	}
	count, err := generator.CountInputTokens(context.Background(), packed)
	if err != nil || count != 37 || client.request.Format != "json" || string(client.request.Schema) != `{"type":"object"}` || client.request.System != summarySystemPrompt || !strings.Contains(client.request.User, "synthetic meeting") {
		t.Fatalf("count=%d request=%#v error=%v", count, client.request, err)
	}
	capacity, err := generator.ContextTokenLimit(context.Background())
	if err != nil || capacity != 4096 {
		t.Fatalf("capacity=%d error=%v", capacity, err)
	}
}
