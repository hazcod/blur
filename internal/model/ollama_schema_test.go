package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hazcod/blur/internal/model/modeltest"
)

func TestOllamaStructuredOutputSendsBoundedSchemaObject(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["ask"]}},"required":["kind"],"additionalProperties":false}`)
	server := httptest.NewServer(modeltest.LocalOllama("fixture", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Format json.RawMessage `json:"format"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || string(request.Format) != string(schema) {
			t.Fatal("schema was not an object", string(request.Format))
		}
		_, _ = w.Write([]byte(`{"done":true,"message":{"content":"{\"kind\":\"ask\"}"},"eval_count":8}`))
	})))
	defer server.Close()
	g, _ := NewOllama(server.URL, "fixture")
	defer g.CloseIdleConnections()
	if _, err := g.Generate(context.Background(), Request{System: "Return the schema.", User: "synthetic input", Schema: schema, MaxOutputTokens: 32}); err != nil {
		t.Fatal(err)
	}
}
