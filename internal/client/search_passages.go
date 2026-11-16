package client

import (
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/search"
)

func locateExactPassages(hits []SearchHit, terms []string) {
	for n := range hits {
		if hits[n].MatchEnd != 0 || len(hits[n].Text) <= assistant.MaxEvidenceBytes {
			continue
		} // semantic retrieval already selected a passage
		hits[n].MatchStart, hits[n].MatchEnd = search.MatchPassage(hits[n].Text, terms, assistant.MaxEvidenceBytes)
	}
}

// Budgeting source hydration is separate from accepting/indexing a document.
// Return the ranked prefix that fits instead of rejecting the entire query.
func boundSearchHydration(page search.ExactPage) search.ExactPage {
	used := 0
	for n, hit := range page.Hits {
		if hit.Bytes > MaxSearchBytes-used {
			page.Hits = page.Hits[:n]
			page.HasMore = true
			break
		}
		if hit.Bytes > 0 {
			used += hit.Bytes
		}
	}
	return page
}
