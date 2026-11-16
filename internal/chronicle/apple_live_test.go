package chronicle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/model"
)

func TestAppleRecapSyntheticVisibleToolIsNotUserActivity(t *testing.T) {
	if os.Getenv("BLUR_SYNTHETIC_APPLE_BACKGROUND") != "1" {
		t.Skip("explicit synthetic on-device model check only")
	}
	client, err := model.NewAppleSourceTransformer()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	o, err := capture.SanitizeObservation("com.apple.Safari", "youtube.com", "Video title: Agentic Chat Studio walkthrough. The presenter describes a fictional tool called Agentic Chat Studio and demonstrates how its chat canvas can arrange a conversation. The visible page contains a video description and captions about the tool. It contains no evidence that this computer's user has operated that tool or created a project with it.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	packed, err := PackHour(Hour{Start: start, End: start.Add(time.Hour), Evidence: []Evidence{{Observation: o, At: start.Add(time.Minute), Salience: 10}}})
	if err != nil {
		t.Fatal(err)
	}
	g := &OllamaGenerator{client: client, schema: []byte(appleSummarySchema)}
	summary, err := g.Generate(t.Context(), packed, MaxChronicleOutputTokens)
	if err != nil {
		t.Fatal("synthetic attribution recap failed", err)
	}
	text := strings.ToLower(summary.Text)
	for _, forbidden := range []string{"studio was used", "user used", "you used", "user built", "user created"} {
		if strings.Contains(text, forbidden) {
			t.Fatal("visible demonstration became an unsupported user action")
		}
	}
	if !strings.Contains(text, "video") && !strings.Contains(text, "page") && !strings.Contains(text, "captur") && !strings.Contains(text, "presenter") {
		t.Fatal("recap lost visible-source attribution")
	}
	if err := ValidateSummary(packed, summary); err != nil {
		t.Fatal("source authority was lost", err)
	}
}

type observedAppleCompletion struct {
	model.LocalCompletion
	response model.Response
}

func (g *observedAppleCompletion) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	response, err := g.LocalCompletion.Generate(ctx, request)
	g.response = response
	return response, err
}

// The diagnostic fields below are content-free structural counts only. Raw
// generated text, prompts and identifiers remain in memory and are not logged.
func TestAppleRecapSyntheticCitationContract(t *testing.T) {
	if os.Getenv("BLUR_SYNTHETIC_APPLE_BACKGROUND") != "1" {
		t.Skip("explicit synthetic on-device model check only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := model.NewAppleSourceTransformer()
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	var observations []capture.SanitizedObservation
	for _, text := range []string{
		"Project Atlas is a local writing tool. Mina leads Project Atlas. Atlas release review covered offline export.",
		"Project Borealis is an encrypted calendar application. Theo leads Borealis. The review covered timezone support.",
		"Project Cedar is a desktop accessibility tool. Alice leads Cedar. Cedar review covered keyboard navigation.",
	} {
		observation, err := capture.SanitizeObservation("editor.app", "synthetic.example", text, 4096)
		if err != nil {
			t.Fatal(err)
		}
		observations = append(observations, observation)
	}
	sort.Slice(observations, func(i, j int) bool { return observations[i].ContentID() > observations[j].ContentID() })
	start := time.Date(2026, 11, 13, 11, 0, 0, 0, time.UTC)
	hour := Hour{Start: start, End: start.Add(time.Hour)}
	for i, observation := range observations {
		hour.Evidence = append(hour.Evidence, Evidence{Observation: observation, At: start.Add(time.Duration(i+1) * time.Minute), Salience: 10})
	}
	packed, err := PackHour(hour)
	if err != nil {
		t.Fatal(err)
	}
	observed := &observedAppleCompletion{LocalCompletion: client}
	generator := &OllamaGenerator{client: observed, schema: []byte(appleSummarySchema)}
	if _, err := generator.Generate(ctx, packed, MaxChronicleOutputTokens); err != nil {
		var decoded summaryResponse
		decodeErr := json.Unmarshal([]byte(observed.response.Text), &decoded)
		allowed, duplicates := 0, 0
		seen := map[string]bool{}
		for _, id := range decoded.SourceIDs {
			if containsID(packed.SourceIDs, id) {
				allowed++
			}
			if seen[id] {
				duplicates++
			}
			seen[id] = true
		}
		t.Logf("closed structure: parsed=%t text_bytes=%d cited=%d allowed=%d sorted=%t duplicates=%d follow_up=%t", decodeErr == nil, len(decoded.Text), len(decoded.SourceIDs), allowed, sort.StringsAreSorted(decoded.SourceIDs), duplicates, decoded.FollowUp != nil)
		t.Fatal("synthetic recap contract failed:", err)
	}
}

// A bounded recap must summarize a dense hour rather than expand one paragraph
// per capture until the native response limit cuts off its JSON. All sources
// here are invented; diagnostics never print source or generated text.
func TestAppleRecapSyntheticDenseHourRemainsBounded(t *testing.T) {
	if os.Getenv("BLUR_SYNTHETIC_APPLE_BACKGROUND") != "1" {
		t.Skip("explicit synthetic on-device model check only")
	}
	for _, count := range []int{16, 48} {
		t.Run(fmt.Sprintf("sources-%d", count), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			client, err := model.NewAppleSourceTransformer()
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			start := time.Date(2026, 11, 13, 11, 0, 0, 0, time.UTC)
			hour := Hour{Start: start, End: start.Add(time.Hour)}
			for i := range count {
				text := fmt.Sprintf("Project Constellation-%d is a local desktop tool. Its project lead is Engineer-%d. The release review covered the encrypted export workflow and keyboard navigation.", i+1, i+1)
				observation, err := capture.SanitizeObservation("editor.app", "synthetic.example", text, 4096)
				if err != nil {
					t.Fatal(err)
				}
				hour.Evidence = append(hour.Evidence, Evidence{Observation: observation, At: start.Add(time.Duration(i+1) * time.Minute), Salience: 10})
			}
			packed, err := PackHour(hour)
			if err != nil {
				t.Fatal(err)
			}
			observed := &observedAppleCompletion{LocalCompletion: client}
			generator := &OllamaGenerator{client: observed, schema: []byte(appleSummarySchema)}
			summary, err := generator.Generate(ctx, packed, MaxChronicleOutputTokens)
			if err != nil {
				var decoded summaryResponse
				decodeErr := json.Unmarshal([]byte(observed.response.Text), &decoded)
				allowed, duplicates := 0, 0
				seen := map[string]bool{}
				for _, id := range decoded.SourceIDs {
					for i := range packed.Evidence {
						if id == summarySourceLabel(i) {
							allowed++
						}
					}
					if seen[id] {
						duplicates++
					}
					seen[id] = true
				}
				var fields map[string]json.RawMessage
				_ = json.Unmarshal([]byte(observed.response.Text), &fields)
				unknown := 0
				for key := range fields {
					if key != "text" && key != "source_ids" && key != "follow_up" {
						unknown++
					}
				}
				t.Logf("closed structure: parsed=%t text_bytes=%d cited=%d allowed=%d duplicates=%d follow_up=%t unknown_fields=%d", decodeErr == nil, len(decoded.Text), len(decoded.SourceIDs), allowed, duplicates, decoded.FollowUp != nil, unknown)
				t.Fatal("dense synthetic recap failed:", err)
			}
			if ValidateSummary(packed, summary) != nil {
				t.Fatal("dense synthetic recap violated source or coverage validation")
			}
			t.Logf("closed outcome: validated; sources=%d citations=%d text_bytes=%d", count, len(summary.SourceIDs), len(summary.Text))
		})
	}
}
