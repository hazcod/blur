package client

import "github.com/hazcod/blur/internal/assistant"

// demoteAuthenticationPages drops sign-in and verification screens from raw
// captures chosen only by time. They carry chrome, not work. When every item in
// the window is such a screen they stay, so a window that holds nothing else is
// still answerable and a question about a login is not emptied.
func demoteAuthenticationPages(items []assistant.Evidence) []assistant.Evidence {
	return demoteEvidence(items, func(item assistant.Evidence) bool {
		return item.Layer == assistant.LayerSource && isAuthenticationPageEvidence(item)
	})
}

// demoteRelaxedAuthenticationPages is the lookup form. A sign-in page that
// satisfies every term of a question is a real match, but one that only
// shares a few words with it is chrome when anything substantive was found.
func demoteRelaxedAuthenticationPages(items []assistant.Evidence) []assistant.Evidence {
	return demoteEvidence(items, func(item assistant.Evidence) bool {
		return item.TermsRelaxed && item.Layer == assistant.LayerSource && isAuthenticationPageEvidence(item)
	})
}

func demoteEvidence(items []assistant.Evidence, drop func(assistant.Evidence) bool) []assistant.Evidence {
	kept := make([]assistant.Evidence, 0, len(items))
	for _, item := range items {
		if !drop(item) {
			kept = append(kept, item)
		}
	}
	if len(kept) == 0 || len(kept) == len(items) {
		return items
	}
	return kept
}
