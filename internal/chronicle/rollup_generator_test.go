package chronicle

import (
	"context"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/model"
)

type rollupCompletion struct {
	reply   string
	request model.Request
}

func (c *rollupCompletion) Generate(_ context.Context, r model.Request) (model.Response, error) {
	c.request = r
	return model.Response{Text: c.reply}, nil
}
func (c *rollupCompletion) CountInputTokens(context.Context, string, string) (int, error) {
	return 42, nil
}
func (c *rollupCompletion) CloseIdleConnections()                    {}
func (c *rollupCompletion) Identity(context.Context) (string, error) { return "model-x", nil }

func TestGeneratorReturnsOnlyStrictRollupText(t *testing.T) {
	prompt := RollupPrompt{System: "system", User: "user"}
	for _, test := range []struct {
		reply string
		ok    bool
	}{
		{`{"text":"- Did things."}`, true},
		{`{"text":"- Did things.","source_ids":["x"]}`, false},
		{`not json`, false},
		{`{"text":"a"} {"text":"b"}`, false},
	} {
		client := &rollupCompletion{reply: test.reply}
		generator := &OllamaGenerator{client: client}
		text, err := generator.GenerateRollup(context.Background(), prompt, MaxRollupOutputTokens)
		if test.ok != (err == nil) || test.ok && text != "- Did things." {
			t.Errorf("reply %q: text=%q err=%v", test.reply, text, err)
		}
		if client.request.System != "system" || client.request.User != "user" || client.request.MaxOutputTokens != MaxRollupOutputTokens {
			t.Errorf("request = %+v", client.request)
		}
	}
	client := &rollupCompletion{reply: `{"text":"x"}`}
	apple := &OllamaGenerator{client: client, schema: []byte(appleSummarySchema)}
	if _, err := apple.GenerateRollup(context.Background(), prompt, 10); err != nil || client.request.Format != "" || !strings.Contains(string(client.request.Schema), `"text"`) || strings.Contains(string(client.request.Schema), "source_ids") {
		t.Fatalf("apple rollup request = %+v err=%v", client.request, err)
	}
	if n, err := apple.CountRollupTokens(context.Background(), prompt); err != nil || n != 42 {
		t.Fatalf("token count = %d %v", n, err)
	}
	if name, err := apple.RollupModel(context.Background()); err != nil || name != "model-x" {
		t.Fatalf("model name = %q %v", name, err)
	}
	var _ RollupGenerator = (*OllamaGenerator)(nil)
	if _, err := (&OllamaGenerator{}).GenerateRollup(context.Background(), prompt, 1); err == nil {
		t.Fatal("a generator without a client generated")
	}
}
