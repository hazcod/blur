package client

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/localapi"
)

// searchScenario is one row of the search harness. Every query runs through the
// native Search page core and through HandleMCPRead(search_memory); both must
// satisfy the same expectations.
type searchScenario struct {
	name, query string
	// first is a label that must rank first.
	first string
	// present labels must appear anywhere; within names a top-N bound for them.
	present []string
	within  int
	// minInTop requires at least that many labels matching topGlob in the top topN.
	topGlob string
	topN    int
	minTop  int
	// absent labels must never appear.
	absent []string
	// summary is a summary row ID that must be present.
	summary string
	// scopeOnly marks a query with no terms (a time scope only), so rows carry
	// no matched terms.
	scopeOnly bool
	// knownGap marks a confirmed, currently failing case; gapProblem must appear
	// in the joined problems so a gap cannot hide a different failure.
	knownGap, gapProblem string
}

func searchScenarios() []searchScenario {
	return []searchScenario{
		{name: "dima-retest", query: "dima retest", first: "slack-dima-retest", absent: []string{"blur-self"}},
		{name: "venice-key", query: "venice api key", first: "terminal-venice", absent: []string{"blur-self"}},
		// The title also carries these words, but the capture's body does too, so
		// the all-terms exact pass already finds it without Task 11's titles.
		{name: "tiiny-pocket", query: "tiiny pocket", first: "orion-article"},
		{name: "ag-presentations", query: "AG presentations", topGlob: "slack-samira-*", topN: 4, minTop: 2},
		{name: "okta-sign-in", query: "okta sign in", present: []string{"okta-login"}},
		// The corpus has no preview-resume capture until Task 11 adds one with a
		// "Resume"-folded title; flip this row then.
		{name: "resume-folding", query: "resume", present: []string{"preview-resume"}, knownGap: "titles/folding not indexed yet", gapProblem: "preview-resume is missing"},
		{name: "pentest", query: "pentest", present: []string{"notion-pentest-plan"}, within: 5, topGlob: "slack-dima-*", topN: 5, minTop: 1, absent: []string{"blur-self"}},
		{name: "saturday-summary", query: "saturday", summary: "chronicle:day:2026-11-14", scopeOnly: true, present: []string{"slack-dima-2"}},
		{name: "buildwise-packet", query: "buildwise packet url", first: "note-buildwise"},
	}
}

func TestSearchScenario(t *testing.T) {
	corpus := newScenarioCorpus(t)
	seedSearchRollups(t, corpus)
	ctx := context.Background()
	for _, s := range searchScenarios() {
		t.Run(s.name, func(t *testing.T) {
			page, err := corpus.client.SearchMemoryPageContext(ctx, s.query)
			if err != nil {
				t.Fatalf("native search: %v", err)
			}
			var native []string
			for _, hit := range page.Hits {
				native = append(native, corpus.label(hit.SourceID))
			}
			var nativeSummaries []string
			for _, summary := range page.Summaries {
				nativeSummaries = append(nativeSummaries, summary.ID)
			}

			wire, err := corpus.client.HandleMCPRead(ctx, localapi.RPCSearchMemory, json.RawMessage(fmt.Sprintf(`{"query":%q,"limit":20}`, s.query)))
			if err != nil {
				t.Fatalf("search_memory: %v", err)
			}
			var response mcpSearchResponse
			if err := json.Unmarshal(wire, &response); err != nil {
				t.Fatal(err)
			}
			var mcp, mcpSummaries []string
			var problems []string
			if len(wire) > maxMCPReadOutputBytes {
				problems = append(problems, fmt.Sprintf("MCP response is %d bytes, over the %d budget", len(wire), maxMCPReadOutputBytes))
			}
			for _, item := range response.Results {
				label := corpus.label(item.SourceID)
				mcp = append(mcp, label)
				if strings.TrimSpace(item.Excerpt) == "" || item.CapturedAt.IsZero() && !item.TimeUnknown {
					problems = append(problems, fmt.Sprintf("MCP row %s lacks an excerpt or captured_at", label))
				}
				if s.scopeOnly {
					continue
				}
				if len(item.MatchedTerms) == 0 {
					problems = append(problems, fmt.Sprintf("MCP row %s reports no matched terms", label))
				} else if !excerptHasTerm(item.Excerpt, item.MatchedTerms) {
					problems = append(problems, fmt.Sprintf("MCP row %s excerpt %q shows none of %v", label, firstBytes(item.Excerpt, 60), item.MatchedTerms))
				}
			}
			for _, summary := range response.Summaries {
				mcpSummaries = append(mcpSummaries, summary.ID)
				if len(summary.SourceIDsSample) == 0 {
					problems = append(problems, "MCP summary "+summary.ID+" carries no source IDs")
				}
			}
			t.Logf("SEARCH %s: native=%v summaries=%v | mcp=%v summaries=%v relaxed=%t bytes=%d", s.name, native, nativeSummaries, mcp, mcpSummaries, response.Relaxed, len(wire))

			for surface, got := range map[string][]string{"native": native, "mcp": mcp} {
				summaries := map[string][]string{"native": nativeSummaries, "mcp": mcpSummaries}[surface]
				problems = append(problems, s.check(corpus, surface, got, summaries)...)
			}
			gap := s.knownGap != ""
			verdict := "PASS"
			matches := gap && strings.Contains(strings.Join(problems, "; "), s.gapProblem)
			switch {
			case len(problems) > 0 && matches:
				verdict = "KNOWN-GAP"
			case len(problems) > 0:
				verdict = "FAIL"
			}
			t.Logf("SEARCH %s: %s", s.name, verdict)
			switch {
			case len(problems) > 0 && matches:
			case len(problems) > 0 && gap:
				t.Errorf("search scenario %s is a knownGap but broke differently: expected %q, observed %q", s.name, s.gapProblem, strings.Join(problems, "; "))
			case len(problems) > 0:
				t.Errorf("search scenario %s failed: %s", s.name, strings.Join(problems, "; "))
			case gap:
				t.Errorf("search scenario %s now passes, remove knownGap (%s)", s.name, s.knownGap)
			}
		})
	}
}

// seedSearchRollups adds the day and week recaps the recall corpus omits, the
// way the rollup worker stores them: derived text bound to source IDs.
func seedSearchRollups(t *testing.T, corpus *scenarioCorpus) {
	t.Helper()
	seed := func(id, text string, children, labels []string) {
		t.Helper()
		putTestRollup(t, corpus, id, text, children, labels)
	}
	seed("day:2026-11-14", "- Saturday was spent on the Cresco pentest findings with Dima and the AG presentations deck with Samira.\n- Checked mail from Chloe about the consultant reporting template.",
		[]string{"hour:20261114T09Z", "hour:20261114T15Z"}, []string{"slack-dima-1", "slack-dima-2", "slack-samira-2", "mail-chloe", "mail-inbox"})
	seed("day:2026-11-15", "- Sunday: the Orfit retest scope came in from Dima and the AG presentations were reviewed with Samira.",
		[]string{"hour:20261115T13Z"}, []string{"slack-dima-retest", "slack-samira-3", "slack-samira-4"})
	seed("week:2026-W46", "- A pentest week: findings drafted, retest scheduled, AG presentations finished.",
		[]string{"day:2026-11-14", "day:2026-11-15"}, []string{"slack-dima-1", "slack-dima-retest", "slack-samira-4"})
}

func excerptHasTerm(excerpt string, terms []string) bool {
	_, hit := emphasisWords(excerpt, terms)
	return slices.Contains(hit, true)
}

func (s searchScenario) check(c *scenarioCorpus, surface string, got, summaries []string) []string {
	var problems []string
	fail := func(format string, args ...any) {
		problems = append(problems, surface+": "+fmt.Sprintf(format, args...))
	}
	if s.first != "" && (len(got) == 0 || got[0] != s.first) {
		fail("%s is not first: %v", s.first, got)
	}
	for _, label := range s.present {
		index := slices.Index(got, label)
		switch {
		case index < 0:
			fail("%s is missing: %v", label, got)
		case s.within > 0 && index >= s.within:
			fail("%s ranks %d, want within %d: %v", label, index+1, s.within, got)
		}
	}
	if s.topGlob != "" {
		count := 0
		for _, label := range got[:min(len(got), s.topN)] {
			if globMatch(s.topGlob, label) {
				count++
			}
		}
		if count < s.minTop {
			fail("only %d of the top %d match %s: %v", count, s.topN, s.topGlob, got)
		}
	}
	for _, label := range s.absent {
		if slices.Contains(got, label) {
			fail("%s must never appear: %v", label, got)
		}
	}
	if s.summary != "" && !slices.Contains(summaries, s.summary) {
		fail("summary %s is missing: %v", s.summary, summaries)
	}
	return problems
}
