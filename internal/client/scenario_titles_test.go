package client

import (
	"context"
	"slices"
	"testing"
)

// Window titles and token folding over the shared recall corpus. The recall
// scenarios themselves live in scenario_recall_test.go.
func TestScenarioCorpusTitlesAreSearchableAndFolded(t *testing.T) {
	corpus := newScenarioCorpus(t)

	labelsFor := func(query string) []string {
		t.Helper()
		page, err := corpus.client.Search(query)
		if err != nil {
			t.Fatalf("Search(%q): %v", query, err)
		}
		var labels []string
		for _, hit := range page.Hits {
			labels = append(labels, corpus.label(hit.SourceID))
		}
		slices.Sort(labels)
		return labels
	}

	// "resume" has no accent in the query; the capture has "résumé" in its
	// title and body.
	for _, query := range []string{"resume", "résumé", "RÉSUMÉ"} {
		if got := labelsFor(query); !slices.Equal(got, []string{"preview-resume"}) {
			t.Errorf("Search(%q) = %v, want [preview-resume]", query, got)
		}
	}
	// A title-only word: the Orion page title says "Infrastructure" and the
	// article body is generated text that never uses it.
	if got := labelsFor("infrastructure"); !slices.Equal(got, []string{"orion-article"}) {
		t.Errorf("Search(infrastructure) = %v, want [orion-article]", got)
	}

	// Every seeded capture reads back with the title it was recorded with.
	for _, seed := range scenarioCaptures() {
		hit, err := corpus.client.ChronicleSource(context.Background(), corpus.ids[seed.label])
		if err != nil {
			t.Fatalf("%s: %v", seed.label, err)
		}
		if hit.WindowTitle != seed.title {
			t.Errorf("%s: WindowTitle = %q, want %q", seed.label, hit.WindowTitle, seed.title)
		}
	}
}
