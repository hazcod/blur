package client

import (
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/assistant"
)

func TestAskDoesNotRepeatUnverifiedAnswerNoticeBelowTheAnswer(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	d := &desktop{
		app:    &quickDashboardApp{},
		client: c,
		page:   "Ask",
		askResult: &assistant.Result{
			Answer:          assistant.Answer{Text: "The model answer could not be verified.\n\nSaved passages:\n\n[1] “The launch date is Monday.”"},
			Sources:         []assistant.Evidence{{SourceID: strings.Repeat("a", 64), AppID: "com.apple.mail", Text: "The launch date is Monday."}},
			GenerationIssue: assistant.GenerationInvalidResponse,
			SentItems:       1,
		},
	}
	text := mountedLibraryText(t, d.askView())
	if count := strings.Count(text, "The model answer could not be verified"); count != 1 {
		t.Fatalf("unverified-answer warning appeared %d times:\n%s", count, text)
	}
	if !strings.Contains(text, "The launch date is Monday.") {
		t.Fatalf("saved source disappeared with the duplicate notice:\n%s", text)
	}
}

func TestAskLabelsEvidenceForAnUnansweredAttributeAsRelated(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	d := &desktop{
		app:    &quickDashboardApp{},
		client: c,
		page:   "Ask",
		askResult: &assistant.Result{
			Answer:  assistant.Answer{Text: "I found related information about Mira, but not the deployment deadline.", InsufficientEvidence: true},
			Sources: []assistant.Evidence{{SourceID: strings.Repeat("b", 64), AppID: "sh.blur.notes", AppName: "Notes", Text: "The rollback owner is Mira."}},
			Plan:    assistant.Plan{Kind: assistant.KindEntity, Entity: "Mira", Question: "What is Mira's deployment deadline?"},
			Direct:  true,
		},
	}
	text := mountedLibraryText(t, d.askView())
	if !strings.Contains(text, "not the deployment deadline") || !strings.Contains(text, "Related sources") || strings.Contains(text, "\nSources\n") {
		t.Fatalf("unanswered attribute did not explain or label its supporting context:\n%s", text)
	}
}

func TestAskUnavailableCopyDoesNotRequireOllamaForApple(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		for _, repair := range []bool{true, false} {
			got := askUnavailableCopy(LocalAISettings{Provider: ProviderApple, Enabled: enabled}, repair)
			if strings.Contains(got, "Ollama") || !strings.Contains(got, "AI settings") {
				t.Errorf("Apple enabled=%t repair=%t: %s", enabled, repair, got)
			}
		}
	}
}

func TestQuickPanelPendingLabel(t *testing.T) {
	if got := quickPanelPendingLabel("What is 2+2?"); got != "Asking… Esc to stop" {
		t.Fatalf("general label = %q", got)
	}
	if got := quickPanelPendingLabel("What was the launch decision?"); got != "Searching memories… Esc to stop" {
		t.Fatalf("memory label = %q", got)
	}
}

func TestAskProgressLabel(t *testing.T) {
	remote := LocalAISettings{Provider: ProviderOpenAICompatible, RemoteAskEnabled: true}
	cases := []struct {
		settings LocalAISettings
		question string
		want     string
	}{
		{LocalAISettings{}, "What was the launch decision?", "Searching local memory and asking Ollama…"},
		{LocalAISettings{}, "What is 2+2?", "Asking Ollama…"},
		{remote, "What was the launch decision?", "Searching local memory and asking the configured provider…"},
		{remote, "What is 2+2?", "Asking the configured provider…"},
	}
	for _, c := range cases {
		if got := askProgressLabel(c.settings, c.question); got != c.want {
			t.Errorf("askProgressLabel(%q) = %q, want %q", c.question, got, c.want)
		}
	}
}
