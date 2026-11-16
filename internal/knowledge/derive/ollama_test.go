package derive

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hazcod/blur/internal/model/modeltest"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/knowledge"
)

func TestLibraryPromptExtractsOnlyExplicitOpenWorkAsTentative(t *testing.T) {
	for _, required := range []string{
		"open_work true",
		"unfinished request",
		"awaited response",
		"unresolved next step",
		"login",
		"Evidence may contain source code or logs",
	} {
		if !strings.Contains(systemPrompt, required) {
			t.Errorf("library extraction prompt omits %q", required)
		}
	}
}

func TestAppleOpenWorkInstructionsUseAnExplicitBoolean(t *testing.T) {
	if !strings.Contains(systemPrompt, `"open_work":false`) || !strings.Contains(systemPrompt, "unresolved next step") {
		t.Fatal("Apple's JSON instructions omit the explicit open-work classification")
	}
}

func TestLibraryPromptUsesAConcreteJSONKindExample(t *testing.T) {
	if strings.Contains(systemPrompt, `"kind":"person|project`) || !strings.Contains(systemPrompt, `"kind":"project"`) {
		t.Fatal("library prompt must show one valid kind value instead of a pipe-delimited pseudo-enum")
	}
	for _, required := range []string{"top-level object with a suggestions array", "source citation only in source_id", `"open_work":false,"source_id":"e1"`, "Subject or page titles may name a project without using the word project", "include a project proposal for pending work named by an email subject"} {
		if !strings.Contains(systemPrompt, required) {
			t.Errorf("library prompt omits response-shape rule %q", required)
		}
	}
}

func TestLibraryPromptPreservesDistinctTechnicalDetails(t *testing.T) {
	for _, required := range []string{"project's purpose", "named people and roles", "specific workflow or technical capability", "Avoid spending multiple suggestions on paraphrases"} {
		if !strings.Contains(systemPrompt, required) {
			t.Errorf("Library extraction prompt omits detail-preservation guidance %q", required)
		}
	}
}

func TestOllamaGeneratorSendsOnlyBoundedEvidenceAndValidatesCitations(t *testing.T) {
	source := sourceID('a')
	input := []Evidence{{SourceID: source, Text: "Project Atlas is owned by Mina."}}
	var requests int
	server := httptest.NewServer(modeltest.LocalOllama("qwen3:4b", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" {
			t.Fatalf("request target = %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Options struct {
				NumPredict int `json:"num_predict"`
			} `json:"options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[1].Role != "user" || !strings.Contains(body.Messages[1].Content, "Project Atlas is owned by Mina.") || !strings.Contains(body.Messages[1].Content, `"source_id":"e1"`) || strings.Contains(body.Messages[1].Content, source) || body.Options.NumPredict != MaxOutputTokens {
			t.Fatalf("unexpected bounded request: %+v", body)
		}
		content := `{"suggestions":[{"kind":"project","name":"Project Atlas","fact":"Mina owns the project.","open_work":false,"source_id":"e1"}]}`
		_, _ = fmt.Fprintf(w, `{"message":{"content":%q},"done":true,"prompt_eval_count":100,"eval_count":30}`, content)
	})))
	defer server.Close()
	generator, err := NewOllamaGenerator(server.URL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	defer generator.CloseIdleConnections()
	if tokens, err := generator.CountInputTokens(context.Background(), input); err != nil || tokens <= 0 {
		t.Fatalf("CountInputTokens() = %d, %v", tokens, err)
	}
	got, err := generator.Generate(context.Background(), input)
	if err != nil || len(got) != 1 || got[0].Kind != knowledge.Project || got[0].SourceID != source || requests != 1 {
		t.Fatalf("Generate() = %+v, %v, requests=%d", got, err, requests)
	}
}

func TestOllamaGeneratorRejectsUnknownModelFieldsAndUncitedSources(t *testing.T) {
	source := sourceID('a')
	for name, response := range map[string]string{
		"unknown state field": `{"suggestions":[{"kind":"project","name":"Atlas","fact":"A fact.","open_work":false,"source_id":"` + source + `","state":"confirmed"}]}`,
		"uncited source":      `{"suggestions":[{"kind":"project","name":"Atlas","fact":"A fact.","open_work":false,"source_id":"` + sourceID('b') + `"}]}`,
		"tentative open work": `{"suggestions":[{"kind":"project","name":"Atlas","fact":"Vendor reply is pending.","open_work":true,"source_id":"` + source + `"}]}`,
		"trailing JSON":       `{"suggestions":[]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(modeltest.LocalOllama("qwen3:4b", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"message":{"content":%q},"done":true,"prompt_eval_count":20,"eval_count":10}`, response)
			})))
			defer server.Close()
			generator, err := NewOllamaGenerator(server.URL, "qwen3:4b")
			if err != nil {
				t.Fatal(err)
			}
			defer generator.CloseIdleConnections()
			if _, err := generator.Generate(context.Background(), []Evidence{{SourceID: source, Text: "Project Atlas."}}); err == nil {
				t.Fatal("invalid suggestion response accepted")
			}
		})
	}
}

func TestAppleResponseRepairsOnlyBareEvidenceLabelValues(t *testing.T) {
	source := sourceID('a')
	input := []Evidence{{SourceID: source, Text: "Project Harbor reviews expired credentials and protects personal data with local redaction."}}
	raw := `[{"kind":"project","name":"Project Harbor","fact":"Project Harbor reviews credentials and local redaction.","open_work":false,"source_id":e1}]`
	got, err := decodeLinkedResponse(raw, input)
	if err != nil || len(got) != 1 || got[0].SourceID != source {
		t.Fatalf("decodeLinkedResponse() = %+v, %v", got, err)
	}
	if _, err := decodeResponse(strings.Replace(raw, `"source_id":e1`, `"source_id":untrusted`, 1)); err == nil {
		t.Fatal("arbitrary bare source_id value was accepted")
	}
	withQuotedText := normalizeResponseEnvelope(`[{"kind":"project","name":"Harbor","fact":"The literal source_id: e1 is mentioned.","open_work":false,"source_id":e1}]`)
	if !strings.Contains(withQuotedText, `"fact":"The literal source_id: e1 is mentioned."`) {
		t.Fatalf("normalization changed quoted fact text: %s", withQuotedText)
	}
}

func TestOpenWorkFlagAddsTentativeMarkerDeterministically(t *testing.T) {
	source := sourceID('a')
	input := []Evidence{{SourceID: source, Text: "Project Atlas is awaiting a vendor reply."}}
	got, err := decodeLinkedResponse(`{"suggestions":[{"kind":"project","name":"Atlas","fact":"Vendor reply is pending.","open_work":true,"source_id":"e1"}]}`, input)
	if err != nil || len(got) != 1 || got[0].Fact != knowledge.PossibleOpenItemPrefix+"Vendor reply is pending." {
		t.Fatalf("open work marker = %+v, %v", got, err)
	}
}

func TestOpenWorkFlagRequiresAnExplicitEvidenceCue(t *testing.T) {
	source := sourceID('a')
	input := []Evidence{{SourceID: source, Text: "Project Harbor reviews expired credentials with automated checks; Mina owns the workflow."}}
	got, err := decodeLinkedResponse(`{"suggestions":[{"kind":"project","name":"Harbor","fact":"Harbor reviews credentials.","open_work":true,"source_id":"e1"}]}`, input)
	if err != nil || len(got) != 1 || got[0].OpenWork || strings.HasPrefix(got[0].Fact, knowledge.PossibleOpenItemPrefix) {
		t.Fatalf("completed workflow was classified as open work: %+v, %v", got, err)
	}
}

func TestExplicitOpenWorkCueHandlesParaphrasesAndResolution(t *testing.T) {
	for _, test := range []struct {
		name string
		text string
		want bool
	}{
		{"awaiting signoff", "We're still awaiting the vendor's sign-off.", true},
		{"blocked until", "The release is blocked until staging clears.", true},
		{"no reply yet", "The vendor has not responded yet.", true},
		{"outstanding follow-up", "An outstanding follow-up with Sam remains.", true},
		{"negated waiting", "We're not waiting for approval anymore; it is signed off.", false},
		{"wait ended", "We were waiting for logs, but we received them this morning.", false},
		{"not outstanding", "The follow-up is not outstanding anymore.", false},
		{"resolved after pending", "The verification was pending yesterday, but completed successfully today.", false},
		{"later pending state", "The vendor replied; we are now waiting for a new build.", true},
		{"later new request", "The old request was completed; a new approval is now pending.", true},
		{"no cue", "Mina owns the release workflow.", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := hasExplicitOpenWorkCue(test.text); got != test.want {
				t.Fatalf("hasExplicitOpenWorkCue(%q) = %t, want %t", test.text, got, test.want)
			}
		})
	}
}

func TestDecodeLinkedResponseRepairsBareKnownJSONKeys(t *testing.T) {
	source := sourceID('a')
	raw := `{"suggestions":[{"kind":"project","name":"Borealis","fact":"Literal open_work:false text is not a field.", open_work":false, "source_id":"e1"}]}`
	got, err := decodeLinkedResponse(raw, []Evidence{{SourceID: source, Text: "Project Borealis validates configuration."}})
	if err != nil || len(got) != 1 || got[0].SourceID != source || got[0].OpenWork || got[0].Fact != "Literal open_work:false text is not a field." {
		t.Fatalf("repaired proposal = %+v, %v; normalized=%s", got, err, quoteKnownBareJSONKeys(raw))
	}
}

func TestDecodeLinkedResponseAcceptsAppleJSONShapeVariants(t *testing.T) {
	source := sourceID('a')
	input := []Evidence{{SourceID: source, Text: "Project Atlas is a local document indexer."}}
	for _, test := range []struct {
		name string
		json string
		want int
	}{
		{"empty array", `[]`, 0},
		{"proposal array", `[{"kind":"project","name":"Atlas","fact":"Atlas indexes local documents.","open_work":false,"source_id":"e1"}]`, 1},
		{"single proposal object", `{"kind":"project","name":"Atlas","fact":"Atlas indexes local documents.","open_work":false,"source_id":"e1"}`, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeLinkedResponse(test.json, input)
			if err != nil || len(got) != test.want {
				t.Fatalf("decoded %d proposals, want %d: %+v, %v", len(got), test.want, got, err)
			}
			if test.want == 1 && got[0].SourceID != source {
				t.Fatalf("proposal citation=%q, want %q", got[0].SourceID, source)
			}
		})
	}
}
