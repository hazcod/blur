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
	got := ids(demoteAuthenticationPages(items))
	if len(got) != 2 || got[0] != "work" || got[1] != "hour" {
		t.Fatalf("kept %v, want the substantive capture and the non-source recap", got)
	}
}

func TestDemoteAuthenticationPagesKeepsAnOnlyLogin(t *testing.T) {
	items := []assistant.Evidence{{SourceID: "login-1", Text: loginChrome}, {SourceID: "login-2", Text: loginChrome}}
	if got := demoteAuthenticationPages(items); len(got) != 2 {
		t.Fatalf("a window holding only sign-in pages must keep them, got %v", ids(got))
	}
	if got := demoteAuthenticationPages(nil); len(got) != 0 {
		t.Fatalf("nil must stay empty, got %v", ids(got))
	}
}

func TestDemoteRelaxedAuthenticationPagesOnlyTouchesPartialMatches(t *testing.T) {
	items := []assistant.Evidence{
		{SourceID: "login-partial", Text: loginChrome, TermsRelaxed: true},
		{SourceID: "login-full", Text: loginChrome},
		{SourceID: "work", Text: "Pentest planning for Q4", TermsRelaxed: true},
	}
	got := ids(demoteRelaxedAuthenticationPages(items))
	if len(got) != 2 || got[0] != "login-full" || got[1] != "work" {
		t.Fatalf("kept %v, want the full-match login and the substantive partial match", got)
	}
	only := []assistant.Evidence{{SourceID: "login-partial", Text: loginChrome, TermsRelaxed: true}}
	if got := demoteRelaxedAuthenticationPages(only); len(got) != 1 {
		t.Fatalf("a lone login partial match must stay, got %v", ids(got))
	}
}
