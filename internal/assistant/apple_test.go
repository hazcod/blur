package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/model"
)

type appleCompletionStub struct {
	text     string
	err      error
	count    int
	requests []model.Request
	closed   bool
}

func (s *appleCompletionStub) Generate(_ context.Context, request model.Request) (model.Response, error) {
	s.requests = append(s.requests, request)
	return model.Response{Text: s.text}, s.err
}
func (s *appleCompletionStub) CountInputTokens(context.Context, string, string) (int, error) {
	return s.count, s.err
}
func (*appleCompletionStub) Identity(context.Context) (string, error) {
	return "apple-foundation-model:os-build:test", nil
}
func (s *appleCompletionStub) CloseIdleConnections() { s.closed = true }

func TestAppleAnswerPreservesCitationMappingAndValidation(t *testing.T) {
	client := &appleCompletionStub{text: `{"answer":"Thursday","source_labels":["S1"],"insufficient_evidence":false}`}
	g := &OllamaGenerator{client: client, schema: []byte(appleAnswerSchema)}
	packed := PackedContext{System: "Use evidence.", User: `{"question":"when?","evidence":[{"text":"Thursday"}]}`, SourceIDs: []string{strings.Repeat("a", 64)}}
	packed.InputBytes = len(packed.System) + len(packed.User)
	got, err := g.Generate(context.Background(), packed, MaxAnswerTokens)
	if err != nil || got.Text != "Thursday" || len(got.SourceIDs) != 1 || got.SourceIDs[0] != packed.SourceIDs[0] {
		t.Fatalf("Generate()=%+v,%v", got, err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("requests=%d", len(client.requests))
	}
	request := client.requests[0]
	if request.System != packed.System || request.User != packed.User || !json.Valid(request.Schema) || request.Format != "" || request.MaxOutputTokens != MaxAnswerTokens || request.KeepAlive != answerModelKeepAlive {
		t.Fatalf("invalid bounded answer request: %+v", request)
	}
	for _, text := range []string{
		`{"answer":"Thursday","source_labels":["S1"],"insufficient_evidence":false,"execute":true}`,
		`{"answer":"Thursday","source_labels":["S1"],"insufficient_evidence":false} {}`,
	} {
		client.text = text
		if _, err := g.Generate(context.Background(), packed, MaxAnswerTokens); err == nil {
			t.Fatalf("accepted unsafe answer %s", text)
		}
	}
	client.text = `{"answer":"Thursday","source_labels":["S1"],"insufficient_evidence":false}`
	got, err = g.Generate(context.Background(), packed, MaxAnswerTokens)
	if err != nil || !reflect.DeepEqual(got.SourceIDs, packed.SourceIDs[:1]) {
		t.Fatalf("model evidence labels were not resolved to the current app mapping: %+v %v", got, err)
	}
}

func TestAppleAnswerSupportsGeneralPreparedAndWritingRulePaths(t *testing.T) {
	for _, kind := range []string{"general", "prepared", "rules"} {
		t.Run(kind, func(t *testing.T) {
			client := &appleCompletionStub{text: "  SQLite is an embedded database.  ", count: 40}
			g := &OllamaGenerator{client: client, schema: []byte(appleAnswerSchema)}
			var got string
			var err error
			switch kind {
			case "general":
				got, err = g.GenerateGeneral(context.Background(), "What is SQLite?", generalAnswerTokens)
			case "prepared":
				got, err = g.GenerateGeneralPrompt(context.Background(), "Answer with supplied rules.", `{"question":"What is SQLite?","writing_rules":["Use concise wording."]}`, generalAnswerTokens)
			case "rules":
				got, err = g.GenerateGeneralWithRules(context.Background(), "What is SQLite?", []string{"Use concise wording."}, generalAnswerTokens)
			}
			if err != nil || got != "SQLite is an embedded database." || len(client.requests) != 1 {
				t.Fatalf("general path=%q, %v; requests=%d", got, err, len(client.requests))
			}
			request := client.requests[0]
			if request.MaxOutputTokens != 1024 || request.KeepAlive != answerModelKeepAlive || request.Format != "" || len(request.Schema) != 0 || !strings.Contains(request.User, "What is SQLite?") {
				t.Fatalf("invalid general request: %+v", request)
			}
			if kind != "general" && !strings.Contains(request.User, "Use concise wording.") {
				t.Fatal("writing rules lost")
			}
			g.CloseIdleConnections()
			if !client.closed {
				t.Fatal("completion resources remained open")
			}
		})
	}
}

func TestAppleAnswerUsesRuntimeTokenAdmissionAndPreservesProviderErrors(t *testing.T) {
	client := &appleCompletionStub{text: "unused", count: ContextTokenLimit}
	g := &OllamaGenerator{client: client}
	if _, err := g.GenerateGeneral(context.Background(), "What is SQLite?", generalAnswerTokens); !errors.Is(err, ErrTokenBudget) || len(client.requests) != 0 {
		t.Fatalf("over-budget request sent: %v, requests=%d", err, len(client.requests))
	}
	want := errors.New("foundation model unavailable")
	client.err = want
	if _, err := g.GenerateGeneralPrompt(context.Background(), "system", "question", generalAnswerTokens); !errors.Is(err, want) {
		t.Fatalf("provider error=%v", err)
	}
}

var (
	_ GeneralGenerator         = (*OllamaGenerator)(nil)
	_ PreparedGeneralGenerator = (*OllamaGenerator)(nil)
	_ RuleGeneralGenerator     = (*OllamaGenerator)(nil)
)
