package chronicle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

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
func (*appleCompletionStub) Identity(context.Context) (string, error) {
	return "apple-foundation-model:os-build:test", nil
}
func (s *appleCompletionStub) CloseIdleConnections() { s.closed = true }

func TestAppleSummaryUsesBoundedSanitizedEvidenceAndSourceValidation(t *testing.T) {
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	packed, err := PackHour(Hour{Start: start, End: start.Add(time.Hour), Evidence: []Evidence{evidence(1, start.Add(time.Minute), "meeting discussed launch owners")}})
	if err != nil {
		t.Fatal(err)
	}
	client := &appleCompletionStub{count: 4500, text: fmt.Sprintf(`{"text":"Discussed launch ownership.","source_ids":[%q],"follow_up":{"source_id":%q,"text":"Who owns the launch checklist?"}}`, packed.SourceIDs[0], packed.SourceIDs[0])}
	g := &OllamaGenerator{client: client, schema: []byte(appleSummarySchema)}
	if count, err := g.CountInputTokens(context.Background(), packed); err != nil || count != 4500 {
		t.Fatalf("runtime token count=%d,%v", count, err)
	}
	counted := client.request
	got, err := g.Generate(context.Background(), packed, model.MaxOutputTokens)
	if err != nil || got.Text != "Discussed launch ownership.\n\nCoverage: saved captures only." || got.FollowUp == nil || got.FollowUp.SourceID != packed.SourceIDs[0] {
		t.Fatalf("Generate()=%+v,%v", got, err)
	}
	r := client.request
	if r.System != counted.System || r.User != counted.User || r.Format != "" || !json.Valid(r.Schema) || r.MaxOutputTokens != 192 || r.KeepAlive != 0 {
		t.Fatalf("invalid summary request: %+v", r)
	}
	var payload summaryPayload
	if json.Unmarshal([]byte(r.User), &payload) != nil || len(payload.Evidence) != 1 || payload.Evidence[0].Text != "1:meeting discussed launch owners" || payload.Evidence[0].SourceID != "S1" || strings.Contains(r.User, packed.SourceIDs[0]) {
		t.Fatalf("invalid evidence payload: %s", r.User)
	}
	for _, text := range []string{
		fmt.Sprintf(`{"text":"Recap.","source_ids":[%q]}`, strings.Repeat("a", 64)),
		fmt.Sprintf(`{"text":"Recap.","source_ids":[%q],"execute":true}`, packed.SourceIDs[0]),
		fmt.Sprintf(`{"text":"Recap.","source_ids":[%q],"follow_up":{"source_id":%q,"text":"Do this task"}}`, packed.SourceIDs[0], packed.SourceIDs[0]),
		`{"text":"Recap.","source_ids":[]} {}`,
	} {
		client.text = text
		if _, err := g.Generate(context.Background(), packed, model.MaxOutputTokens); err == nil {
			t.Fatalf("unsafe summary accepted: %s", text)
		}
	}
	want := errors.New("foundation model unavailable")
	client.err = want
	if _, err := g.Generate(context.Background(), packed, model.MaxOutputTokens); !errors.Is(err, want) {
		t.Fatalf("provider error=%v", err)
	}
	g.CloseIdleConnections()
	if !client.closed {
		t.Fatal("summary completion resources remained open")
	}
}

func TestAppleSummaryGuidanceBindsExactSourcesAndCountingSchema(t *testing.T) {
	p := workerHour(t)
	p.Evidence = append(p.Evidence, evidence(2, p.Start.Add(2*time.Minute), "reviewed a second project"))
	packed, err := PackHour(p)
	if err != nil {
		t.Fatal(err)
	}
	stub := &appleCompletionStub{count: 100, text: `{"text":"Reviewed two projects.","source_ids":["S2","S1"]}`}
	g := &OllamaGenerator{client: stub, schema: []byte(appleSummarySchema)}
	if _, err := g.CountInputTokens(context.Background(), packed); err != nil {
		t.Fatal(err)
	}
	counted := stub.request
	summary, err := g.Generate(context.Background(), packed, MaxChronicleOutputTokens)
	if err != nil {
		t.Fatal("valid citations in time order must be canonicalized before persistence", err)
	}
	if !reflect.DeepEqual(summary.SourceIDs, packed.SourceIDs) {
		t.Fatal("canonical source order changed source authority")
	}
	if !bytes.Equal(counted.Schema, stub.request.Schema) {
		t.Fatal("count/generate did not use identical dynamic guidance")
	}
	var schema struct {
		Properties map[string]struct {
			Items struct {
				Enum []string `json:"enum"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(stub.request.Schema, &schema); err != nil || !reflect.DeepEqual(schema.Properties["source_ids"].Items.Enum, []string{"S1", "S2"}) {
		t.Fatal("native guidance left citation identifiers unconstrained", err)
	}
	if _, exists := schema.Properties["follow_up"]; exists {
		t.Fatal("native guidance offered a follow-up for nonmeeting evidence")
	}
	for _, ids := range [][]string{{"S1", "S1"}, {"S1", "S3"}, {packed.SourceIDs[0], strings.Repeat("f", 64)}} {
		encoded, err := json.Marshal(ids)
		if err != nil {
			t.Fatal(err)
		}
		stub.text = fmt.Sprintf(`{"text":"Unsupported recap.","source_ids":%s}`, encoded)
		if _, err := g.Generate(context.Background(), packed, MaxChronicleOutputTokens); err == nil {
			t.Fatal("canonicalization accepted duplicate or forged citation")
		}
	}
}
