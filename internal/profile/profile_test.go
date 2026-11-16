package profile

import "testing"

func TestScopedRulesRequireEverySelectedContext(t *testing.T) {
	s := Scope{AppID: "com.tinyspeck.slackmacgap", Domain: "work.example", Project: "Atlas", Audience: "Executive team"}
	c := Context{AppIDs: []string{s.AppID}, Domains: []string{s.Domain}, Projects: []string{"ATLAS"}, Audiences: []string{"executive team"}}
	if s.Validate() != nil || !s.Applies(c) {
		t.Fatal("explicit matching scope was rejected")
	}
	c.Audiences = nil
	if s.Applies(c) {
		t.Fatal("audience-scoped rule applied without its audience")
	}
	if !MentionsScope("Draft the Atlas update for the executive team.", "Atlas") || MentionsScope("Explain AtlasKit.", "Atlas") {
		t.Fatal("scope did not match whole words")
	}
}
