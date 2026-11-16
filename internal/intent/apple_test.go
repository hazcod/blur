package intent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/model/modeltest"
)

type appleCompletionStub struct {
	text    string
	texts   []string
	err     error
	request model.Request
	calls   int
	closed  bool
}

func (s *appleCompletionStub) Generate(_ context.Context, request model.Request) (model.Response, error) {
	s.request = request
	s.calls++
	text := s.text
	if s.calls <= len(s.texts) {
		text = s.texts[s.calls-1]
	}
	return model.Response{Text: text}, s.err
}
func (*appleCompletionStub) CountInputTokens(context.Context, string, string) (int, error) {
	return 40, nil
}
func (*appleCompletionStub) Identity(context.Context) (string, error) {
	return "apple-foundation-model:os-build:test", nil
}
func (s *appleCompletionStub) CloseIdleConnections() { s.closed = true }

func TestAppleParserSendsOnlyExplicitInputAndBoundTargets(t *testing.T) {
	client := &appleCompletionStub{text: `{"kind":"task_edit","target":"T1","when":"Friday at 9am"}`}
	g := &Ollama{client: client, apple: true}
	request := Request{Input: "Move it to Friday at 9am", Targets: []Target{{Ref: "T1", Title: "Review PR"}}}
	got, err := g.Parse(context.Background(), request)
	if err != nil || got.Kind != TaskEdit || got.Target != "T1" || got.When != "Friday at 9am" {
		t.Fatalf("Parse()=%+v,%v", got, err)
	}
	if client.calls != 1 {
		t.Fatalf("known edit repeated classification: requests=%d", client.calls)
	}
	if client.request.System != appleParserSystem || client.request.MaxOutputTokens != 384 || !json.Valid(client.request.Schema) || client.request.Format != "" {
		t.Fatalf("invalid parser request: %+v", client.request)
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal([]byte(client.request.User), &payload) != nil || len(payload) != 2 || payload["input"] == nil || payload["targets"] == nil {
		t.Fatalf("unexpected user context: %s", client.request.User)
	}
	g.Close()
	if !client.closed {
		t.Fatal("parser left completion resources open")
	}
}

func TestAppleParserKeepsAuthoritativeActionValidation(t *testing.T) {
	request := Request{Input: "Move it to Friday at 9am", Targets: []Target{{Ref: "T1", Title: "Review PR"}}}
	for _, text := range []string{
		`{"kind":"task_edit","target":"T2","when":"Friday at 9am"}`,
		`{"kind":"task_edit","target":"T1","when":"Friday at 10am"}`,
		`{"kind":"task_edit","target":"T1","when":"Friday at 9am","execute":true}`,
		`{"kind":"task_edit","target":"T1","when":"Friday at 9am"} {}`,
	} {
		g := &Ollama{client: &appleCompletionStub{text: text}, apple: true}
		if _, err := g.Parse(context.Background(), request); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted unsafe action: %s, %v", text, err)
		}
	}
	client := &appleCompletionStub{text: `{"kind":"ask","mode":"general"}`}
	if _, err := (&Ollama{client: client}).Parse(context.Background(), Request{}); !errors.Is(err, ErrInvalid) || client.calls != 0 {
		t.Fatalf("invalid entry reached model: %v, calls=%d", err, client.calls)
	}
	want := errors.New("foundation model unavailable")
	client.err = want
	if _, err := (&Ollama{client: client}).Parse(context.Background(), request); !errors.Is(err, want) {
		t.Fatalf("provider error=%v", err)
	}
}

func TestAppleParserUsesNativeClosedSchemaAndOnlySuppliedTargets(t *testing.T) {
	for _, targets := range [][]Target{nil, {{Ref: "T1", Title: "Review Aurora"}, {Ref: "T2", Title: "Check backups"}}} {
		client := &appleCompletionStub{text: `{"kind":"note"}`}
		request := Request{Input: "Aurora uses port 4317", Targets: targets}
		if _, err := (&Ollama{client: client, apple: true}).Parse(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Type       string          `json:"type"`
			OneOf      json.RawMessage `json:"oneOf"`
			Properties map[string]struct {
				Type        string   `json:"type"`
				Enum        []string `json:"enum"`
				Description string   `json:"description"`
				MaxLength   *int     `json:"maxLength"`
			} `json:"properties"`
			Required             []string `json:"required"`
			AdditionalProperties *bool    `json:"additionalProperties"`
		}
		if err := json.Unmarshal(client.request.Schema, &schema); err != nil {
			t.Fatal(err)
		}
		if schema.Type != "object" || len(schema.OneOf) != 0 || schema.AdditionalProperties == nil || *schema.AdditionalProperties || len(schema.Required) != 1 || schema.Required[0] != "kind" || schema.Properties["kind"].Type != "string" || len(schema.Properties["kind"].Enum) == 0 {
			t.Fatalf("Apple request cannot use native guided generation: %s", client.request.Schema)
		}
		for name, property := range schema.Properties {
			if property.Type != "string" || property.MaxLength != nil || property.Description == "" {
				t.Fatalf("unsupported or unexplained Apple field %q: %+v", name, property)
			}
		}
		if len(schema.Properties) != 1 || client.calls != 1 {
			t.Fatalf("note classification requested cross-kind details: %s, calls=%d", client.request.Schema, client.calls)
		}
		for _, kind := range schema.Properties["kind"].Enum {
			if len(targets) == 0 && (kind == "task_edit" || kind == "task_complete" || kind == "task_cancel" || kind == "task_inspect") {
				t.Fatalf("unbound task operation permitted: %q", kind)
			}
		}
	}
}

func TestAppleParserDetailsCannotChangeKindOrUseUnboundTargets(t *testing.T) {
	client := &appleCompletionStub{text: `{"kind":"task_edit","target":"T1","when":"Friday at 9am"}`}
	request := Request{Input: "Move task 1 to Friday at 9am", Targets: []Target{{Ref: "T1", Title: "Review Aurora"}, {Ref: "T2", Title: "Check backups"}}}
	if _, err := (&Ollama{client: client, apple: true}).Parse(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if json.Unmarshal(client.request.Schema, &schema) != nil || len(schema.Properties["kind"].Enum) != 1 || schema.Properties["kind"].Enum[0] != "task_edit" || len(schema.Properties["target"].Enum) != 2 || schema.Properties["target"].Enum[0] != "T1" || schema.Properties["target"].Enum[1] != "T2" {
		t.Fatalf("unbound detail schema: %s", client.request.Schema)
	}
	if _, ok := schema.Properties["mode"]; ok {
		t.Fatalf("task edit allowed ask-only mode: %s", client.request.Schema)
	}
	if _, ok := schema.Properties["text"]; ok {
		t.Fatalf("rescheduling allowed a replacement title: %s", client.request.Schema)
	}
}

func TestAppleParserRejectsFieldsFromAnotherIntentKind(t *testing.T) {
	request := Request{Input: "Aurora uses port 4317"}
	client := &appleCompletionStub{text: `{"kind":"note","mode":"memory","text":"Aurora uses port 4317"}`}
	if _, err := (&Ollama{client: client, apple: true}).Parse(context.Background(), request); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Apple note accepted ask-only mode: %v", err)
	}
}

func TestOllamaParserRetainsKindSpecificWireSchema(t *testing.T) {
	server := httptest.NewServer(modeltest.LocalOllama("fixture", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire struct {
			Format map[string]json.RawMessage `json:"format"`
		}
		if json.NewDecoder(r.Body).Decode(&wire) != nil || len(wire.Format["oneOf"]) == 0 || wire.Format["properties"] != nil {
			t.Errorf("Ollama lost its kind-specific schema: %+v", wire.Format)
		}
		_, _ = w.Write([]byte(`{"done":true,"message":{"content":"{\"kind\":\"note\"}"}}`))
	})))
	defer server.Close()
	parser, err := NewOllama(server.URL, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer parser.Close()
	if _, err := parser.Parse(context.Background(), Request{Input: "Save a note that Aurora uses port 4317"}); err != nil {
		t.Fatal(err)
	}
}

func TestAppleKindSchemaExcludesAmbiguousTaskActions(t *testing.T) {
	request := Request{Input: "Move it to Friday at 9am", Targets: []Target{{Ref: "T1", Title: "Review Aurora"}, {Ref: "T2", Title: "Check backups"}}}
	if kinds := appleKinds(request); len(kinds) != 1 || kinds[0] != "clarify" {
		t.Fatalf("ambiguous task change permits a new action instead of clarification: %v", kinds)
	}
}

func TestAppleKindSchemaRespectsExplicitNoteAndMemoryAuthority(t *testing.T) {
	kinds := appleKinds(Request{Input: "Can you save a note that Aurora uses port 4317?"})
	if len(kinds) != 1 || kinds[0] != "note" {
		t.Fatalf("explicit note request can drift to another operation: %v", kinds)
	}
	for _, kind := range appleKinds(Request{Input: "I need to review the Aurora PR"}) {
		if kind == "fact" || kind == "rule" {
			t.Fatalf("unrequested personal learning offered: %q", kind)
		}
	}
}

type appleByteBudgetCompletion struct{ *appleCompletionStub }

func (s appleByteBudgetCompletion) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	// Force the conservative byte-count admission used on older supported OS
	// versions, including JSON guidance and the full template/output reserve.
	const guidance = "\nReturn only one valid JSON object, without Markdown fences or commentary."
	if len(request.System)+len(request.User)+len(request.Schema)+len(guidance)+request.MaxOutputTokens+model.PromptTemplateReserve > 4096 {
		return model.Response{}, model.ErrAppleBudget
	}
	return s.appleCompletionStub.Generate(ctx, request)
}

func TestAppleCompactIntentRequestsFitConservative4096ByteAdmission(t *testing.T) {
	for _, test := range []struct {
		request Request
		replies []string
	}{
		{Request{Input: "Can you save a note that Aurora uses port 4317?"}, []string{`{"kind":"note"}`}},
		{Request{Input: "Write a Go function that adds two integers."}, []string{`{"kind":"ask_general"}`}},
		{Request{Input: "Move task 1 to Friday at 9am", Targets: []Target{{Ref: "T1", Title: "Review Aurora"}, {Ref: "T2", Title: "Check backups"}}}, []string{`{"kind":"task_edit","target":"T1","when":"Friday at 9am"}`}},
	} {
		client := appleByteBudgetCompletion{&appleCompletionStub{texts: test.replies}}
		if _, err := (&Ollama{client: client, apple: true}).Parse(context.Background(), test.request); err != nil {
			t.Fatalf("compact intent cannot fit 4096-byte admission: %v", err)
		}
	}
	client := appleByteBudgetCompletion{&appleCompletionStub{text: `{"kind":"note"}`}}
	if _, err := (&Ollama{client: client, apple: true}).Parse(context.Background(), Request{Input: strings.Repeat("x", 3500)}); !errors.Is(err, model.ErrAppleBudget) || client.calls != 0 {
		t.Fatalf("over-budget intent reached generation: err=%v calls=%d", err, client.calls)
	}
}

func TestAppleIntentDetailsCannotChangeClassifiedKind(t *testing.T) {
	client := &appleCompletionStub{texts: []string{`{"kind":"clarify"}`, `{"kind":"note"}`}}
	if _, err := (&Ollama{client: client, apple: true}).Parse(context.Background(), Request{Input: "Compiler examples"}); !errors.Is(err, ErrInvalid) || client.calls != 2 {
		t.Fatalf("detail reply changed selected kind: %v", err)
	}
}

func TestAppleKindSchemaUsesAuthoritativeQuestionAndTaskOperationGuards(t *testing.T) {
	for _, test := range []struct {
		request Request
		kind    string
	}{
		{Request{Input: "What did I decide about Aurora?"}, "ask"},
		{Request{Input: "Mark task 1 complete", Targets: []Target{{Ref: "T1", Title: "Review Aurora"}, {Ref: "T2", Title: "Check backups"}}}, "task_complete"},
		{Request{Input: "Move it to Friday at 9am", Targets: []Target{{Ref: "T1", Title: "Review Aurora"}}}, "task_edit"},
		{Request{Input: "Cancel it", Targets: []Target{{Ref: "T1", Title: "Review Aurora"}}}, "task_cancel"},
		{Request{Input: "When is it due?", Targets: []Target{{Ref: "T1", Title: "Review Aurora"}}}, "task_inspect"},
	} {
		if kinds := appleKinds(test.request); len(kinds) != 1 || kinds[0] != test.kind {
			t.Fatalf("proved operation kind=%v, want %q", kinds, test.kind)
		}
	}
}
