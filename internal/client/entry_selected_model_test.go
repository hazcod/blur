package client

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/notes"
)

func selectedEntryClient(t *testing.T, provider string, consent bool, model *singleSearchModel) *Client {
	t.Helper()
	c := NewClient(nil)
	c.localAI = &localAIController{path: filepath.Join(t.TempDir(), "ai.json"), settings: LocalAISettings{Enabled: true, Provider: provider, RemoteAskEnabled: consent}, askModel: model, service: notes.NewService(nil, nil, nil)}
	t.Cleanup(c.Close)
	return c
}

func TestEntryUsesTheSelectedCloudModelWithConsent(t *testing.T) {
	input := "Add reviewing the Aurora PR to my tasks"
	g := &singleSearchModel{respond: func(_ context.Context, _, payload string) (string, error) {
		var request intent.Request
		if err := json.Unmarshal([]byte(payload), &request); err != nil || request.Input != input || len(request.Targets) != 0 || request.Pending != nil {
			t.Errorf("entry generation received unrelated memory: %s, %v", payload, err)
		}
		return `{"kind":"task","text":"reviewing the Aurora PR"}`, nil
	}}
	c := selectedEntryClient(t, ProviderClaude, true, g)
	resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "UTC")
	if err != nil || resolved.Action.Kind != intent.Task || resolved.Action.Text != "reviewing the Aurora PR" || g.calls != 1 {
		t.Fatalf("cloud selection bypassed natural interpretation: %+v calls=%d err=%v", resolved, g.calls, err)
	}
}

func TestEntryDoesNotSendInputOrSavedTargetsToCloudWithoutConsent(t *testing.T) {
	g := &singleSearchModel{}
	c := selectedEntryClient(t, ProviderClaude, false, g)
	resolved, err := c.ResolveEntry(context.Background(), "The Aurora service uses port 4317", EntryState{}, "UTC")
	if err != nil || resolved.Action.Kind != intent.Note || g.calls != 0 {
		t.Fatalf("unconsented entry reached cloud: %+v calls=%d err=%v", resolved, g.calls, err)
	}
}

func TestEntryRejectsLateSelectedModelActionAfterPolicyChange(t *testing.T) {
	started, release := make(chan context.Context, 1), make(chan struct{})
	g := &singleSearchModel{respond: func(ctx context.Context, _, _ string) (string, error) {
		started <- ctx
		<-release
		return `{"kind":"task","text":"reviewing the Aurora PR"}`, nil
	}}
	c := selectedEntryClient(t, ProviderClaude, true, g)
	done := make(chan error, 1)
	go func() {
		_, err := c.ResolveEntry(context.Background(), "Add reviewing the Aurora PR to my tasks", EntryState{}, "UTC")
		done <- err
	}()
	select {
	case ctx := <-started:
		if err := c.ConfigureLocalAI(context.Background(), LocalAISettings{}); err != nil {
			close(release)
			t.Fatal(err)
		}
		if ctx.Err() == nil {
			close(release)
			t.Fatal("retired provider context was not canceled")
		}
		close(release)
	case <-time.After(time.Second):
		close(release)
		t.Fatal("natural intent never consulted the selected model")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("late task action escaped policy cancellation: %v", err)
	}
}
