package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/model"
)

type appleVoiceNativeDiagnostic struct {
	model.LocalCompletion
	response  model.Response
	responses []model.Response
}

func (c *appleVoiceNativeDiagnostic) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	response, err := c.LocalCompletion.Generate(ctx, request)
	c.response = response
	c.responses = append(c.responses, response)
	return response, err
}

func TestAppleNativeSyntheticFillerTranscript(t *testing.T) {
	if os.Getenv("BLUR_TEST_APPLE_HELPER") == "" {
		t.Skip("requires the canonical helper beside the test binary")
	}
	apple, err := model.NewAppleSourceTransformer()
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := &appleVoiceNativeDiagnostic{LocalCompletion: apple}
	g := &OllamaGenerator{client: diagnostic, schema: []byte(appleAnswerSchema)}
	evidence := transcriptEvidence(50)
	for i := range evidence {
		evidence[i].Text = "Um."
	}
	result, err := NewService(nil, g, g, ContextTokenLimit).SummarizeTranscript(t.Context(), evidence)
	for i, response := range diagnostic.responses {
		var answer ModelAnswer
		decoded := json.Unmarshal([]byte(response.Text), &answer) == nil
		seen := map[string]bool{}
		duplicates := 0
		for _, id := range answer.SourceIDs {
			if seen[id] {
				duplicates++
			}
			seen[id] = true
		}
		t.Logf("synthetic filler attempt=%d valid_json=%t insufficient=%t answer_bytes=%d citations=%d duplicates=%d", i+1, decoded, answer.InsufficientEvidence, len(answer.Text), len(answer.SourceIDs), duplicates)
	}
	gap := errors.Is(err, ErrSummaryInsufficientEvidence) || errors.Is(err, ErrSummaryInvalidResponse)
	if !gap || result.SentItems != 50 || result.Answer.Text != "" || len(result.Sources) != 0 || len(diagnostic.responses) > 2 || (errors.Is(err, ErrSummaryInvalidResponse) && result.GenerationIssue != GenerationInvalidResponse) {
		t.Fatalf("filler did not preserve an honest bounded failure prefix: sent=%d error=%v", result.SentItems, err)
	}
}
func (c *appleVoiceNativeDiagnostic) CountRequestTokens(ctx context.Context, request model.Request) (int, error) {
	return model.CountRequestInputTokens(ctx, c.LocalCompletion, request)
}
func (c *appleVoiceNativeDiagnostic) ContextTokenLimit(ctx context.Context) (int, error) {
	return model.CompletionContextLimit(ctx, c.LocalCompletion)
}

// Opt-in only: run a test binary beside a copy of the signed canonical helper.
// All source text below is synthetic; no capture or user memory is accessed.
func TestAppleNativeSyntheticVoiceQuestion(t *testing.T) {
	if os.Getenv("BLUR_TEST_APPLE_HELPER") == "" {
		t.Skip("requires the canonical helper beside the test binary")
	}
	for _, count := range []int{1, 4, 8} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			generator, err := NewAppleGenerator()
			if err != nil {
				t.Fatal(err)
			}
			diagnostic := &appleVoiceNativeDiagnostic{LocalCompletion: generator.client}
			generator.client = diagnostic
			evidence := make([]Evidence, count)
			for i := range evidence {
				id := fmt.Sprintf("%064x", i+1)
				evidence[i] = Evidence{AssociationUnverified: true, SourceID: id, AppID: "sh.blur.meetings", AppName: "Voice notes", Domain: id + ".meeting.local", Text: "The video explains TCP congestion control. A sender increases its congestion window until packet loss or explicit congestion notification indicates congestion, then reduces its sending rate. Slow start grows the window quickly; congestion avoidance grows it more cautiously.", CapturedAt: time.Date(2026, 11, 14, 10, i, 0, 0, time.UTC)}
			}
			result, err := NewService(&testRetriever{evidence: evidence}, generator, generator, ContextTokenLimit).Ask(t.Context(), "What was my youtube video about?")
			var object map[string]json.RawMessage
			validJSON := json.Unmarshal([]byte(diagnostic.response.Text), &object) == nil
			keys := make([]string, 0, len(object))
			for key := range object {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			t.Logf("sources=%d json=%t keys=%v bytes=%d input_tokens=%d direct=%t issue=%s error=%v", count, validJSON, keys, len(diagnostic.response.Text), result.InputTokens, result.Direct, result.GenerationIssue, err)
			if err != nil || result.Direct || result.GenerationIssue != "" || result.Answer.InsufficientEvidence || len(result.Sources) == 0 || !strings.Contains(strings.ToLower(result.Answer.Text), "congestion") {
				t.Fatal("native grounded voice answer did not retain valid evidence attribution")
			}
		})
	}
}
