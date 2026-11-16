package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/notes"
)

type entryAppleLatencyModel struct {
	configuredAskModel
	calls int
}

func (g *entryAppleLatencyModel) GenerateGeneralPrompt(ctx context.Context, system, user string, max int) (string, error) {
	g.calls++
	return g.configuredAskModel.(assistant.PreparedGeneralGenerator).GenerateGeneralPrompt(ctx, system, user, max)
}

func (g *entryAppleLatencyModel) GenerateGeneralJSONPrompt(ctx context.Context, system, user string, schema json.RawMessage, max int) (string, error) {
	g.calls++
	return g.configuredAskModel.(assistant.PreparedJSONGenerator).GenerateGeneralJSONPrompt(ctx, system, user, schema, max)
}

// Opt-in timing runs the real selected Apple adapter with only synthetic input.
// Compile this test beside the exact bundled helper. No user store is opened,
// and neither the Apple provider nor this fixture starts background work.
func TestLocalAppleEntryUnderstandingLatency(t *testing.T) {
	if os.Getenv("BLUR_APPLE_MODEL_INTEGRATION") != "1" {
		t.Skip("requires the installed Apple model and adjacent bundled helper")
	}
	controller := newLocalAIController(filepath.Join(t.TempDir(), "ai.json"), notes.NewService(nil, nil, nil))
	c := NewClient(nil).WithLocalAI(controller)
	t.Cleanup(c.Close)
	t.Cleanup(controller.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := c.ConfigureLocalAI(ctx, LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}); err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	observed := &entryAppleLatencyModel{configuredAskModel: controller.askModel}
	controller.askModel = observed
	controller.mu.Unlock()
	for _, test := range []struct{ name, input, mode string }{
		{"general", "Could you explain how hash maps work?", "general"},
		{"memory", "Could you tell me what I decided about the Aurora rollback?", "memory"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var durations []time.Duration
			for trial := 0; trial < 3; trial++ {
				observed.calls = 0
				started := time.Now()
				resolved, err := c.ResolveEntry(ctx, test.input, EntryState{}, "UTC")
				elapsed := time.Since(started)
				t.Logf("trial=%d elapsed=%s generations=%d", trial+1, elapsed.Round(time.Millisecond), observed.calls)
				if err != nil || resolved.Action.Kind != intent.Ask || resolved.Action.Text != test.input || resolved.Action.Mode != test.mode {
					t.Fatalf("synthetic entry=%+v err=%v", resolved.Action, err)
				}
				if observed.calls != 1 {
					t.Fatalf("one interpretation made %d generations; want one", observed.calls)
				}
				durations = append(durations, elapsed)
			}
			slices.Sort(durations)
			t.Logf("understanding median=%s", durations[len(durations)/2].Round(time.Millisecond))
		})
	}
}
