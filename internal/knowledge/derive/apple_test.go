package derive

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/model"
)

type appleCompletionStub struct {
	text    string
	err     error
	request model.Request
	count   int
	closed  bool
}

func (s *appleCompletionStub) Generate(_ context.Context, r model.Request) (model.Response, error) {
	s.request = r
	return model.Response{Text: s.text}, s.err
}
func (s *appleCompletionStub) CountInputTokens(_ context.Context, system, user string) (int, error) {
	s.request = model.Request{System: system, User: user}
	return s.count, s.err
}
func (s *appleCompletionStub) CountRequestTokens(_ context.Context, request model.Request) (int, error) {
	s.request = request
	return s.count, s.err
}
func (s *appleCompletionStub) Identity(context.Context) (string, error) {
	return "apple-foundation-model:os-build:test:context:4096", s.err
}
func (s *appleCompletionStub) CloseIdleConnections() { s.closed = true }

func TestAppleDerivationPreservesEvidenceLabelsAndStrictSuggestions(t *testing.T) {
	input := []Evidence{{SourceID: sourceID('a'), Text: "Project Atlas is owned by Mina."}}
	client := &appleCompletionStub{count: 4500, text: `{"suggestions":[{"kind":"project","name":"Project Atlas","fact":"Mina owns the project.","open_work":false,"source_id":"e1"}]}`}
	g := newAppleGenerator(client)
	if count, err := g.CountInputTokens(context.Background(), input); err != nil || count != 4500 {
		t.Fatalf("runtime token count=%d,%v", count, err)
	}
	counted := client.request
	got, err := g.Generate(context.Background(), input)
	if err != nil || len(got) != 1 || got[0].Kind != knowledge.Project || got[0].SourceID != input[0].SourceID {
		t.Fatalf("Generate()=%+v,%v", got, err)
	}
	r := client.request
	if r.System != counted.System || r.User != counted.User || r.Format != "" || len(r.Schema) != 0 || r.MaxOutputTokens != MaxOutputTokens || r.KeepAlive != 0 || strings.Contains(r.User, input[0].SourceID) {
		t.Fatalf("invalid bounded derivation request: %+v", r)
	}
	var wire evidencePayload
	if json.Unmarshal([]byte(r.User), &wire) != nil || len(wire.Evidence) != 1 || wire.Evidence[0].SourceID != "e1" || wire.Evidence[0].Text != input[0].Text {
		t.Fatalf("invalid evidence: %s", r.User)
	}
	for _, text := range []string{
		`{"suggestions":[{"kind":"project","name":"Atlas","fact":"Mina owns it.","open_work":false,"source_id":"e2"}]}`,
		`{"suggestions":[{"kind":"project","name":"Atlas","fact":"Mina owns it.","open_work":false,"source_id":"e1","state":"confirmed"}]}`,
		`{"suggestions":[]} {}`,
	} {
		client.text = text
		if _, err := g.Generate(context.Background(), input); !errors.Is(err, model.ErrOllamaResponse) {
			t.Fatalf("unsafe suggestion accepted: %s, %v", text, err)
		}
	}
	g.CloseIdleConnections()
	if !client.closed {
		t.Fatal("derivation completion resources remained open")
	}
}

func TestAppleDerivationBindsProgressToProviderScopedIdentity(t *testing.T) {
	client := &appleCompletionStub{}
	g := &OllamaGenerator{client: client}
	if identity, err := g.Identity(context.Background()); err != nil || identity != "apple-foundation-model:os-build:test:context:4096" {
		t.Fatalf("Identity()=%q,%v", identity, err)
	}
	want := errors.New("foundation model unavailable")
	client.err = want
	if _, err := g.Identity(context.Background()); !errors.Is(err, want) {
		t.Fatalf("identity error=%v", err)
	}
	if _, err := g.Generate(context.Background(), []Evidence{{SourceID: sourceID('a'), Text: "Project Atlas."}}); !errors.Is(err, want) {
		t.Fatalf("provider error=%v", err)
	}
}

func TestAppleDerivationGuidanceUsesArrayValuesAndExactSourceChoices(t *testing.T) {
	input := []Evidence{
		{SourceID: sourceID('a'), Text: "Project Atlas has a local parser."},
		{SourceID: sourceID('b'), Text: "Mina maintains the parser."},
	}
	client := &appleCompletionStub{count: 100, text: `{"suggestions":[]}`}
	g := newAppleGenerator(client)
	if _, err := g.CountInputTokens(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	counted := client.request
	if _, err := g.Generate(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if string(counted.Schema) != string(client.request.Schema) || counted.Format != client.request.Format {
		t.Fatal("count/generate did not use identical request-scoped guidance")
	}
	if client.request.Format != "" || len(client.request.Schema) != 0 || !strings.Contains(client.request.System, `"kind":"project"`) {
		t.Fatal("Apple source transform should use plain JSON guidance with a concrete kind example")
	}
}

func TestDerivationRejectsMissingNullAndSchemaEchoSuggestions(t *testing.T) {
	input := []Evidence{{SourceID: sourceID('a'), Text: "Project Atlas has a local parser."}}
	for _, raw := range []string{`{}`, `{"suggestions":null}`, `{"suggestions":{"items":[{"kind":"project","name":"Atlas","fact":"Atlas has a local parser.","source_id":"e1"}]}}`} {
		if _, err := decodeLinkedResponse(raw, input); !errors.Is(err, model.ErrOllamaResponse) {
			t.Fatal("missing or non-array suggestions must not be marked as successfully derived")
		}
	}
	if _, err := decodeLinkedResponse(`{"suggestions":[]}`, input); err != nil {
		t.Fatal("an explicit empty array remains a valid omission", err)
	}
}

func TestAppleDerivationVersionsItsExtractionContract(t *testing.T) {
	g := newAppleGenerator(&appleCompletionStub{})
	identity, err := g.Identity(context.Background())
	if err != nil || !strings.HasSuffix(identity, ":library-json-v8") {
		t.Fatal("changed extraction instructions must use new source/model receipts", err)
	}
}
