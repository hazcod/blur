package client

import "github.com/hazcod/blur/internal/assistant"

// demoteAuthenticationPages drops sign-in and verification screens from raw
// captures chosen only by time. They carry chrome, not work. They stay when
// every item in the window is such a screen, so a window holding nothing else is
// still answerable, and when the question itself is about signing in (see
// questionHasLoginCue), where those screens are the substance.
func demoteAuthenticationPages(question string, items []assistant.Evidence) []assistant.Evidence {
	if questionHasLoginCue(question) {
		return items
	}
	kept := make([]assistant.Evidence, 0, len(items))
	for _, item := range items {
		if item.Layer != assistant.LayerSource || !isAuthenticationPageEvidence(item) {
			kept = append(kept, item)
		}
	}
	if len(kept) == 0 || len(kept) == len(items) {
		return items
	}
	return kept
}
