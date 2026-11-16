package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/model"
)

type appleSchemaPorts struct{ counted, generated model.Request }

func (c *appleSchemaPorts) Generate(_ context.Context, r model.Request) (model.Response, error) {
	c.generated = r
	return model.Response{Text: `{"answer":"The integrity discussion concerned audit evidence.","source_labels":["S1"],"insufficient_evidence":false}`}, nil
}
func (*appleSchemaPorts) CountInputTokens(context.Context, string, string) (int, error) {
	return 100, nil
}
func (c *appleSchemaPorts) CountRequestTokens(_ context.Context, r model.Request) (int, error) {
	c.counted = r
	return 100, nil
}
func (*appleSchemaPorts) CloseIdleConnections()                    {}
func (*appleSchemaPorts) Identity(context.Context) (string, error) { return "synthetic-apple", nil }

func TestAppleGroundedSchemaLeavesCitationMappingToTheApp(t *testing.T) {
	evidence := []Evidence{{SourceID: strings.Repeat("a", 64), Text: "Synthetic integrity email."}, {SourceID: strings.Repeat("b", 64), Text: "Integrity follow-up."}}
	packed, err := PackContext(integrityQuestion, evidence)
	if err != nil {
		t.Fatal(err)
	}
	ports := &appleSchemaPorts{}
	g := &OllamaGenerator{client: ports, schema: []byte(appleAnswerSchema)}
	if _, err := g.CountMemoryInputTokens(context.Background(), packed.System, packed.User); err != nil {
		t.Fatal(err)
	}
	got, err := g.Generate(context.Background(), packed, MaxAnswerTokens)
	if err != nil || got.Text != "The integrity discussion concerned audit evidence." || !reflect.DeepEqual(got.SourceIDs, packed.SourceIDs[:1]) {
		t.Fatalf("app-assigned evidence=%+v error=%v", got, err)
	}
	if !reflect.DeepEqual(ports.counted.Schema, ports.generated.Schema) || ports.counted.Format != ports.generated.Format || ports.counted.System != ports.generated.System || ports.counted.User != ports.generated.User {
		t.Fatal("count and generation used different grounded guidance")
	}
	requireAppleNativeSchema(t, ports.generated.Schema)
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(ports.generated.Schema, &schema) != nil || len(schema.Properties) != 3 || schema.Properties["answer"] == nil || schema.Properties["source_labels"] == nil || schema.Properties["insufficient_evidence"] == nil || schema.Properties["source_ids"] != nil {
		t.Fatalf("model schema still delegates source selection: %s", ports.generated.Schema)
	}
	if strings.Contains(ports.generated.User, "source_id") || !strings.Contains(ports.generated.System, "source_labels") {
		t.Fatal("model prompt does not request compact evidence labels")
	}
}

func TestAppleGroundedSchemaCountMatchesGenerationForLongEvidence(t *testing.T) {
	for _, count := range []int{1, 2, 12} {
		evidence := make([]Evidence, count)
		for i := range evidence {
			evidence[i] = Evidence{SourceID: fmt.Sprintf("%064x", i+1), Text: "Synthetic evidence."}
		}
		packed, err := PackContext(integrityQuestion, evidence)
		if err != nil {
			t.Fatal(err)
		}
		ports := &appleSchemaPorts{}
		g := &OllamaGenerator{client: ports, schema: []byte(appleAnswerSchema)}
		if _, err := g.CountMemoryInputTokens(context.Background(), packed.System, packed.User); err != nil {
			t.Fatal(err)
		}
		if _, err := g.Generate(context.Background(), packed, MaxAnswerTokens); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ports.counted.Schema, ports.generated.Schema) {
			t.Fatalf("evidence count %d changed schema between count and generation", count)
		}
	}
}
