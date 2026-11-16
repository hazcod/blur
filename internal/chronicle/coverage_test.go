package chronicle

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/power"
)

func TestLargeSourceRequestKeepsOriginalIdentityAndLabelsHeadTailExcerpts(t *testing.T) {
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	text := "HEAD visible heading\n" + strings.Repeat("日本語🙂", 900) + "\nMIDDLE excluded detail\n" + strings.Repeat("é世🙂", 900) + "\nTAIL decision recorded"
	source, err := capture.SanitizeObservation("editor.app", "example.com", text, capture.MaxObservationTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	packed, err := PackHour(Hour{Start: start, End: start.Add(time.Hour), Evidence: []Evidence{{Observation: source, At: start.Add(time.Minute)}}})
	if err != nil {
		t.Fatalf("large saved source rejected: %v", err)
	}
	g := &OllamaGenerator{client: &chronicleCountPorts{}}
	_, request, err := g.request(packed)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Coverage struct {
			Excerpted int `json:"excerpted_sources"`
		} `json:"coverage"`
		Evidence []struct {
			SourceID      string `json:"source_id"`
			AppID         string `json:"app_id"`
			Domain        string `json:"domain"`
			Text          string `json:"text"`
			Excerpted     bool   `json:"excerpted"`
			OriginalBytes int    `json:"original_bytes"`
			Ranges        []struct {
				Start int `json:"start_byte"`
				End   int `json:"end_byte"`
			} `json:"excerpt_ranges"`
		} `json:"evidence"`
	}
	if json.Unmarshal([]byte(request), &payload) != nil || len(payload.Evidence) != 1 {
		t.Fatal("invalid model payload")
	}
	item := payload.Evidence[0]
	if item.SourceID != source.ContentID() || item.AppID != "editor.app" || item.Domain != "example.com" {
		t.Fatalf("provenance lost: %+v", item)
	}
	if !utf8.ValidString(item.Text) || len(item.Text) > MaxEvidenceItemBytes || !strings.Contains(item.Text, "HEAD visible heading") || !strings.Contains(item.Text, "TAIL decision recorded") || strings.Contains(item.Text, "MIDDLE excluded detail") {
		t.Fatalf("excerpt content or bounds wrong: bytes=%d", len(item.Text))
	}
	if !item.Excerpted || item.OriginalBytes != len(source.Text()) || payload.Coverage.Excerpted != 1 {
		t.Fatalf("model was not told the source is excerpted: %s", request)
	}
	if packed.Evidence[0].Observation.Validate() == nil {
		t.Fatal("excerpt pretends to be an original sanitized observation")
	}
	if source.Text() != text {
		t.Fatal("saved original was changed")
	}
	if len(item.Ranges) != 2 || item.Ranges[0].Start != 0 || item.Ranges[1].End != len(text) || item.Ranges[0].End >= item.Ranges[1].Start {
		t.Fatal("excerpt boundaries missing or overlapping")
	}
	wantHead := text[:item.Ranges[0].End]
	wantTail := text[item.Ranges[1].Start:]
	if !utf8.ValidString(wantHead) || !utf8.ValidString(wantTail) || !strings.HasPrefix(item.Text, wantHead) || !strings.HasSuffix(item.Text, wantTail) {
		t.Fatal("provider excerpt ranges do not describe the original source")
	}
}

func TestCoverageFooterFitsMaxSummaryWithoutSplittingUnicodeAndIsIdempotent(t *testing.T) {
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	partialText := "password: x\n" + strings.Repeat("safe content ", 800)
	partial, err := capture.SanitizeObservation("example.app", "", partialText, len(partialText))
	if err != nil || !partial.Truncated() {
		t.Fatalf("partial fixture: %v", err)
	}
	packed, err := PackHour(Hour{Start: start, End: start.Add(time.Hour), Evidence: []Evidence{{Observation: partial, At: start}}, SourceLimitReached: true})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("界", MaxSummaryBytes/3)
	summary, err := decodeSummaryResponse(fmt.Sprintf(`{"text":%q,"source_ids":[%q]}`, text, partial.ContentID()), packed)
	if err != nil || len(summary.Text) > MaxSummaryBytes || !utf8.ValidString(summary.Text) || !strings.Contains(summary.Text, "1 partial capture") || !strings.Contains(summary.Text, "1 source(s) excerpted") || !strings.Contains(summary.Text, "additional captures may be absent") {
		t.Fatalf("bounded coverage summary invalid: bytes=%d err=%v", len(summary.Text), err)
	}
	again, err := applyCoverage(packed, summary)
	if err != nil || again.Text != summary.Text || strings.Count(again.Text, "Coverage:") != 1 {
		t.Fatal("worker and provider appended duplicate coverage")
	}
}

func TestSummaryRequestAndStoredTextDistinguishPartialCapturesAndOmittedSources(t *testing.T) {
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	// Redaction expands this bounded input, producing a real partial observation.
	partial, err := capture.SanitizeObservation("example.app", "example.com", "password: x\n"+strings.Repeat("x", 52), 64)
	if err != nil || !partial.Truncated() {
		t.Fatalf("partial fixture: %v", err)
	}
	items := []Evidence{{Observation: partial, At: start, Salience: 255}}
	for i := 0; i < 5; i++ {
		items = append(items, evidence(i+1, start.Add(time.Duration(i+1)*time.Minute), strings.Repeat("word ", 700)))
	}
	packed, err := PackHour(Hour{Start: start, End: start.Add(time.Hour), Evidence: items})
	if err != nil || packed.Omitted == 0 {
		t.Fatalf("pack: %+v %v", packed, err)
	}
	g := &OllamaGenerator{client: &chronicleCountPorts{}}
	_, request, err := g.request(packed)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Coverage struct {
			Partial int `json:"captured_partial_sources"`
			Omitted int `json:"omitted_sources"`
		} `json:"coverage"`
		Evidence []struct {
			SourceID string `json:"source_id"`
			Partial  bool   `json:"captured_partial"`
		} `json:"evidence"`
	}
	if json.Unmarshal([]byte(request), &payload) != nil || payload.Coverage.Partial != 1 || payload.Coverage.Omitted != packed.Omitted {
		t.Fatalf("coverage lost at model boundary: %s", request)
	}
	if payload.Evidence[0].SourceID != partial.ContentID() || !payload.Evidence[0].Partial {
		t.Fatal("partial capture flag lost")
	}
	summary, err := decodeSummaryResponse(fmt.Sprintf(`{"text":"Reviewed saved notes.","source_ids":[%q]}`, partial.ContentID()), packed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary.Text, "1 partial capture") || !strings.Contains(summary.Text, fmt.Sprintf("%d source(s) omitted", packed.Omitted)) || !strings.Contains(summary.Text, "saved captures only") {
		t.Fatalf("persisted recap hides coverage: %q", summary.Text)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if json.Unmarshal(encoded, &persisted) != nil || persisted["coverage"] != nil || persisted["Coverage"] != nil {
		t.Fatal("transient coverage changed the persisted schema")
	}
}

func TestWorkerAttachesCoverageEvenWhenGeneratorOmitsIt(t *testing.T) {
	hour := workerHour(t)
	hour.Evidence[0] = evidence(8, hour.Start, strings.Repeat("large saved page ", 1000))
	repo := &workerRepo{rows: map[string]Summary{}}
	worker := NewWorker(repo, &workerPolicy{value: performance()}, &workerModel{count: 10}, WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	defer worker.Close()
	if err := worker.Process(context.Background(), hour); err != nil {
		t.Fatal(err)
	}
	row, found, err := repo.GetChronicle(context.Background(), "hour:"+hour.Start.Format("20060102T15Z"))
	if err != nil || !found || !strings.Contains(row.Text, "1 source(s) excerpted") {
		t.Fatalf("worker persisted misleading coverage: %q %t %v", row.Text, found, err)
	}
}

func TestFollowUpCannotInferMissingDetailsFromPartialOrExcerptedSource(t *testing.T) {
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	partial, err := capture.SanitizeObservation("example.app", "", "meeting\npassword: x\n"+strings.Repeat("x", 44), 64)
	if err != nil || !partial.Truncated() {
		t.Fatalf("partial fixture: %v", err)
	}
	large := evidence(7, start, "meeting discussed project\n"+strings.Repeat("safe material ", 700))
	for name, item := range map[string]Evidence{"capture partial": {Observation: partial, At: start}, "model excerpt": large} {
		t.Run(name, func(t *testing.T) {
			packed, err := PackHour(Hour{Start: start, End: start.Add(time.Hour), Evidence: []Evidence{item}})
			if err != nil {
				t.Fatal(err)
			}
			id := packed.SourceIDs[0]
			response := fmt.Sprintf(`{"text":"Reviewed saved meeting notes.","source_ids":[%q],"follow_up":{"source_id":%q,"text":"Who owns the project follow-up?"}}`, id, id)
			if _, err := decodeSummaryResponse(response, packed); err == nil {
				t.Fatal("incomplete source was treated as proof of a missing meeting detail")
			}
		})
	}
}
