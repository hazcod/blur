package client

import "testing"

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
