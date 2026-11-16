package client

import (
	"testing"

	"github.com/hazcod/blur/internal/assistant"
)

const loginChrome = "Sign in to Okta\nUsername\nPassword\nRemember me\nVerification code"

func ids(items []assistant.Evidence) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.SourceID
	}
	return out
}

func TestDemoteAuthenticationPagesDropsLoginWhenSubstantiveExists(t *testing.T) {
	items := []assistant.Evidence{
		{SourceID: "login", Text: loginChrome},
		{SourceID: "work", Text: "Pentest planning for Q4"},
		{SourceID: "hour", Text: loginChrome, Layer: assistant.LayerHour},
	}
	got := ids(demoteAuthenticationPages("what did I do yesterday", items))
	if len(got) != 2 || got[0] != "work" || got[1] != "hour" {
		t.Fatalf("kept %v, want the substantive capture and the non-source recap", got)
	}
}

func TestDemoteAuthenticationPagesKeepsAnOnlyLogin(t *testing.T) {
	items := []assistant.Evidence{{SourceID: "login-1", Text: loginChrome}, {SourceID: "login-2", Text: loginChrome}}
	if got := demoteAuthenticationPages("what did I do", items); len(got) != 2 {
		t.Fatalf("a window holding only sign-in pages must keep them, got %v", ids(got))
	}
	if got := demoteAuthenticationPages("what did I do", nil); len(got) != 0 {
		t.Fatalf("nil must stay empty, got %v", ids(got))
	}
}

func TestDemoteAuthenticationPagesSkippedForExplicitLoginQuestions(t *testing.T) {
	items := []assistant.Evidence{{SourceID: "login", Text: loginChrome}, {SourceID: "work", Text: "Pentest planning for Q4"}}
	for _, question := range []string{
		"what did I capture on the Okta sign-in page yesterday",
		"which login page did I open",
		"show the sign in screen",
		"when did I log in to the portal",
		"was there a password page today",
		"what SSO prompts did I see",
		"any authentication screens yesterday",
	} {
		if got := demoteAuthenticationPages(question, items); len(got) != 2 {
			t.Errorf("%q: demotion must be skipped, kept %v", question, ids(got))
		}
	}
	for _, question := range []string{"what did I do yesterday", "what did I log about the signing", "recap my day"} {
		if questionHasLoginCue(question) {
			t.Errorf("%q must not count as a login question", question)
		}
	}
}
