package profile

import (
	"strings"
	"testing"
	"time"
)

func TestSelectionUsesEnabledScopedRulesAndRelevantExplicitFacts(t *testing.T) {
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	records := []Record{
		{ID: strings.Repeat("a", 32), Kind: WritingRule, Text: "Be concise.", Enabled: true, UpdatedAt: at},
		{ID: strings.Repeat("b", 32), Kind: WritingRule, Text: "Use formal language.", Scope: Scope{Project: "Atlas", Audience: "Executive team"}, Enabled: true, UpdatedAt: at},
		{ID: strings.Repeat("c", 32), Kind: WritingRule, Text: "Use emoji.", Enabled: false, UpdatedAt: at},
		{ID: strings.Repeat("d", 32), Kind: Fact, Text: "My preferred language is Dutch.", Enabled: true, UpdatedAt: at},
		{ID: strings.Repeat("e", 32), Kind: Fact, Text: "My dog is named Pip.", Enabled: true, UpdatedAt: at},
	}
	got := Select(records, "What is my preferred language for Atlas?", Context{}, true)
	if len(got.Rules) != 1 || got.Rules[0].Text != "Be concise." || len(got.Facts) != 1 || got.Facts[0].Text != "My preferred language is Dutch." {
		t.Fatal(got)
	}
	got = Select(records, "Write an Atlas update for the executive team.", Context{}, false)
	if len(got.Rules) != 2 || got.Rules[0].Text != "Use formal language." || len(got.Facts) != 0 {
		t.Fatal(got)
	}
	got = Select(records, "Write an AtlasKit update for the executive team.", Context{}, false)
	if len(got.Rules) != 1 {
		t.Fatal("scope substring leaked into another project", got)
	}
}

func TestSelectionHasHonestBoundedContext(t *testing.T) {
	records := []Record{}
	for n := 0; n < 30; n++ {
		records = append(records, Record{Kind: WritingRule, Text: strings.Repeat("x", 1000), Enabled: true})
	}
	got := Select(records, "Write an update", Context{}, false)
	if len(got.Rules) != 2 || got.Omitted != 28 {
		t.Fatal("unbounded or silently omitted rules", len(got.Rules), got.Omitted)
	}
}
