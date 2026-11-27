package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

// compoundFixture is a real encrypted store holding one target capture that
// spells the product as stored, and one unrelated capture that shares the
// surrounding words.
type compoundFixture struct {
	client     *Client
	store      notes.Repository
	target     string
	distractor string
	put        func(app, text string, at time.Time) string
	index      func()
}

func newCompoundFixture(t *testing.T, stored string) *compoundFixture {
	t.Helper()
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	f := &compoundFixture{store: s}
	f.put = func(app, text string, at time.Time) string {
		o, err := capture.SanitizeObservation(app, "", text, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(ctx, o, at); err != nil {
			t.Fatal(err)
		}
		return o.ContentID()
	}
	f.index = func() {
		for i := 0; i < 50; i++ {
			more, err := s.IndexPendingExact(ctx, 32)
			if err != nil {
				t.Fatal(err)
			}
			if !more {
				return
			}
		}
	}
	now := time.Now()
	f.target = f.put("com.apple.terminal", fmt.Sprintf("Terminal output: %s is running in production. %s version 4.8.2.", stored, stored), now.Add(-2*time.Hour))
	f.distractor = f.put("editor.app", "The build pipeline is running in production. Pipeline version 2.1.0 deployed.", now.Add(-time.Hour))
	f.index()
	f.client = NewClient(s)
	t.Cleanup(f.client.Close)
	return f
}

func hitFor(hits []SearchHit, id string) (SearchHit, int, bool) {
	for i, hit := range hits {
		if hit.SourceID == id {
			return hit, i, true
		}
	}
	return SearchHit{}, -1, false
}

var compoundAskQuestions = []string{
	"Tell me about %s", "Find %s", "%s", "What %s?", "What version %s production running?",
}

// askFinds runs one Ask question and reports whether the target came back, its
// position among the sources, and whether it was flagged as a closest match.
func askFinds(t *testing.T, c *Client, question, target string) (found bool, rank int, relaxed bool) {
	t.Helper()
	result, err := c.AskMemory(context.Background(), question)
	if err != nil {
		t.Fatalf("AskMemory(%q): %v", question, err)
	}
	for i, source := range result.Sources {
		if source.SourceID == target {
			return true, i, source.TermsRelaxed
		}
	}
	return false, -1, false
}

func TestCompoundSplitFindsSeparatedSpellings(t *testing.T) {
	for _, stored := range []string{"threat-bot", "Threat Bot", "threat_bot"} {
		t.Run(stored, func(t *testing.T) {
			f := newCompoundFixture(t, stored)
			for _, query := range []string{"threatbot", "ThreatBot"} {
				hits, err := f.client.SearchMemoryContext(context.Background(), query)
				if err != nil {
					t.Fatal(err)
				}
				hit, rank, ok := hitFor(hits, f.target)
				if !ok || rank != 0 || hit.Relaxed {
					t.Errorf("search %q: found=%t rank=%d relaxed=%t", query, ok, rank, hit.Relaxed)
				}
			}
			for _, tpl := range compoundAskQuestions {
				question := fmt.Sprintf(tpl, "threatbot")
				found, rank, relaxed := askFinds(t, f.client, question, f.target)
				if !found || rank != 0 || relaxed {
					t.Errorf("ask %q: found=%t rank=%d relaxed=%t", question, found, rank, relaxed)
				}
			}
		})
	}
}

func TestCompoundJoinFindsFusedSpelling(t *testing.T) {
	f := newCompoundFixture(t, "ThreatBot")
	for _, query := range []string{"threat bot", "threat-bot"} {
		hits, err := f.client.SearchMemoryContext(context.Background(), query)
		if err != nil {
			t.Fatal(err)
		}
		hit, rank, ok := hitFor(hits, f.target)
		if !ok || rank != 0 || hit.Relaxed {
			t.Errorf("search %q: found=%t rank=%d relaxed=%t", query, ok, rank, hit.Relaxed)
		}
		for _, tpl := range compoundAskQuestions {
			question := fmt.Sprintf(tpl, query)
			found, rank, relaxed := askFinds(t, f.client, question, f.target)
			if !found || rank != 0 || relaxed {
				t.Errorf("ask %q: found=%t rank=%d relaxed=%t", question, found, rank, relaxed)
			}
		}
	}
}

func TestCompoundSplitNeedsAdjacentHalves(t *testing.T) {
	// Both halves are present, as separate words, but never side by side: the
	// index is a bag of words, so only the source text can tell.
	ctx := context.Background()
	f := newCompoundFixture(t, "unrelated")
	scattered := f.put("editor.app", "The threat was real. A bot posted it.", time.Now().Add(-time.Minute))
	f.index()
	hits, err := f.client.SearchMemoryContext(ctx, "threatbot")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := hitFor(hits, scattered); ok {
		t.Error("search returned a capture whose halves are not adjacent")
	}
	if found, _, _ := askFinds(t, f.client, "Tell me about threatbot", scattered); found {
		t.Error("ask returned a capture whose halves are not adjacent")
	}

	// A genuine adjacent capture next to the scattered one is found alone.
	real := f.put("com.apple.terminal", "threat-bot is deployed.", time.Now().Add(-2*time.Minute))
	f.index()
	hits, err = f.client.SearchMemoryContext(ctx, "threatbot")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := hitFor(hits, real); !ok {
		t.Error("search missed the adjacent capture")
	}
	if _, _, ok := hitFor(hits, scattered); ok {
		t.Error("search kept the scattered capture beside a verified one")
	}
}

func TestCompoundLeavesOriginalSpellingHitsUntouched(t *testing.T) {
	// The question's own spelling is in the capture: no substitution runs, so a
	// split-spelled capture is not pulled in.
	f := newCompoundFixture(t, "threatbot")
	other := f.put("com.apple.terminal", "threat-bot notes.", time.Now().Add(-time.Hour))
	f.index()
	hits, err := f.client.SearchMemoryContext(context.Background(), "threatbot")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := hitFor(hits, f.target); !ok {
		t.Fatal("original spelling missed")
	}
	if _, _, ok := hitFor(hits, other); ok {
		t.Error("variant capture returned although the original spelling has a hit")
	}
}

func TestCompoundJoinsUnquotedIdentifierOnly(t *testing.T) {
	// An identifier's separators are what a fused spelling drops, so the search
	// page finds "ThreatBot" for threat_bot. Quoting it asks for that string.
	f := newCompoundFixture(t, "ThreatBot")
	for query, want := range map[string]bool{"threat_bot": true, `"threat_bot"`: false, "`threat_bot`": false, "'threat_bot'": false} {
		hits, err := f.client.SearchMemoryContext(context.Background(), query)
		if err != nil {
			t.Fatal(query, err)
		}
		if _, _, ok := hitFor(hits, f.target); ok != want {
			t.Errorf("search %s: found=%t, want %t", query, ok, want)
		}
	}
}

func TestCompoundSkipsLiteralSearch(t *testing.T) {
	f := newCompoundFixture(t, "threat-bot")
	query := `"threatbot"`
	plan, err := assistant.PlanSearch(query, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Literal {
		t.Fatalf("quoted search is not literal: %+v", plan)
	}
	hits, err := f.client.SearchMemoryContext(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := hitFor(hits, f.target); ok {
		t.Error("literal search was widened to a compound variant")
	}
}

// compoundScripted rewrites to fixed queries and scores a passage relevant only
// when it holds the target fact, so only exact retrieval can reach the target.
type compoundScripted struct {
	target  string
	queries []string
}

func (*compoundScripted) CloseIdleConnections() {}
func (*compoundScripted) CountInputTokens(context.Context, string, string) (int, error) {
	return 100, nil
}
func (m *compoundScripted) Generate(_ context.Context, packed assistant.PackedContext, _ int) (assistant.ModelAnswer, error) {
	if slices.Contains(packed.SourceIDs, m.target) {
		return assistant.ModelAnswer{Text: "Version 4.8.2 is running in production.", SourceIDs: []string{m.target}}, nil
	}
	return assistant.ModelAnswer{InsufficientEvidence: true}, nil
}
func (m *compoundScripted) GenerateGeneralPrompt(_ context.Context, system, user string, _ int) (string, error) {
	var payload struct {
		Passages []struct {
			Ref  string `json:"ref"`
			Text string `json:"text"`
		} `json:"passages"`
	}
	_ = json.Unmarshal([]byte(user), &payload)
	var matches []string
	for _, p := range payload.Passages {
		if strings.Contains(p.Text, "4.8.2") {
			matches = append(matches, fmt.Sprintf(`{"ref":%q,"score":0.95}`, p.Ref))
		}
	}
	queries, _ := json.Marshal(m.queries)
	switch {
	case strings.Contains(system, "do both tasks"):
		return `{"queries":` + string(queries) + `,"matches":[` + strings.Join(matches, ",") + `]}`, nil
	case strings.Contains(system, "alternate search queries"):
		return `{"queries":` + string(queries) + `}`, nil
	case strings.Contains(system, "Rank saved evidence"):
		return `{"matches":[` + strings.Join(matches, ",") + `]}`, nil
	}
	return "", errors.New("unscripted prompt")
}

func TestCompoundSplitReachesOldCaptureWithUselessModelRewrites(t *testing.T) {
	ctx := context.Background()
	for _, question := range []string{"What is threatbot?", "Tell me about threatbot"} {
		f := newCompoundFixture(t, "unrelated")
		now := time.Now()
		old := f.put("com.apple.terminal", "Terminal output: threat-bot is running in production. threat-bot version 4.8.2.", now.Add(-20*24*time.Hour))
		for i := 0; i < 20; i++ {
			f.put("editor.app", fmt.Sprintf("Meeting note %d: the quarterly budget review moved to room %d.", i, 100+i), now.Add(-time.Duration(i)*time.Minute))
		}
		f.index()
		model := &compoundScripted{target: old, queries: []string{"security bot status", "bot version", "production release"}}
		f.client.localAI = &localAIController{settings: LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}, askModel: model, service: notes.NewService(f.store, nil, nil)}
		result, err := f.client.AskMemory(ctx, question)
		if err != nil {
			t.Fatal(err)
		}
		found := slices.ContainsFunc(result.Sources, func(e assistant.Evidence) bool { return e.SourceID == old })
		if !found || result.General {
			t.Errorf("%q: found=%t general=%t", question, found, result.General)
		}
	}
}

func TestCompoundCandidates(t *testing.T) {
	for _, tc := range []struct {
		name     string
		question string
		terms    []string
		joins    [][]string // substituted term sets of the join candidates, in order
	}{
		{
			name:     "adjacent words join",
			question: "Tell me about threat bot",
			terms:    []string{"bot", "threat"},
			joins:    [][]string{{"threatbot"}},
		},
		{
			name:     "separators join, in question order",
			question: "threat-bot and node.js",
			terms:    []string{"bot", "js", "node", "threat"},
			joins:    [][]string{{"js", "node", "threatbot"}, {"bot", "nodejs", "threat"}},
		},
		{
			name:     "only neighbouring pairs join, never words with one between",
			question: "threat was bot",
			terms:    []string{"bot", "threat", "was"},
			joins:    [][]string{{"bot", "threatwas"}, {"threat", "wasbot"}},
		},

		{
			name:     "a newline blocks a join",
			question: "threat\nbot",
			terms:    []string{"bot", "threat"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var joins [][]string
			for _, cand := range compoundCandidates(tc.question, tc.terms) {
				if cand.joined != "" {
					joins = append(joins, cand.terms)
				}
			}
			if !reflect.DeepEqual(joins, tc.joins) {
				t.Fatalf("joins %v, want %v", joins, tc.joins)
			}
		})
	}
	for _, cand := range compoundCandidates("go app", []string{"app", "go"}) {
		if cand.left != "" {
			t.Fatalf("terms under four runes were split: %v", cand)
		}
	}
	for _, cand := range compoundCandidates("It was real. Bot posted", []string{"bot", "it", "posted", "real", "was"}) {
		if slices.Contains(cand.terms, "realbot") {
			t.Fatalf("a sentence boundary was joined: %v", cand)
		}
	}
}

func TestCompoundSplitCandidateOrderAndLimits(t *testing.T) {
	var splits [][2]string
	var sets [][]string
	for _, cand := range compoundCandidates("threatbot", []string{"threatbot"}) {
		if cand.joined != "" {
			t.Fatalf("single word produced a join: %v", cand)
		}
		splits = append(splits, [2]string{cand.left, cand.right})
		sets = append(sets, cand.terms)
	}
	if len(splits) == 0 || splits[0] != [2]string{"thr", "eatbot"} {
		t.Fatalf("first split %v", splits)
	}
	if !slices.Equal(sets[slices.Index(splits, [2]string{"threat", "bot"})], []string{"bot", "threat"}) {
		t.Fatalf("substituted set of threat+bot: %v", sets)
	}
	firstShort := slices.IndexFunc(splits, func(s [2]string) bool { return len([]rune(s[0])) < 3 || len([]rune(s[1])) < 3 })
	lastLong := slices.IndexFunc(splits, func(s [2]string) bool { return s == [2]string{"threa", "tbot"} })
	if firstShort < 0 || lastLong < 0 || lastLong > firstShort {
		t.Fatalf("halves under three runes must come after the longer ones: %v", splits)
	}
	for _, s := range splits {
		a, b := len([]rune(s[0])), len([]rune(s[1]))
		if a < 1 || b < 1 || a+b != 9 || a == 1 && b < 3 || b == 1 && a < 3 {
			t.Errorf("invalid split %v", s)
		}
	}
	// A single-rune half is allowed beside a long half: "obrien" -> "o" + "brien".
	obrien := compoundCandidates("obrien", []string{"obrien"})
	if !slices.ContainsFunc(obrien, func(c compoundCandidate) bool { return c.left == "o" && c.right == "brien" }) {
		t.Error("o+brien missing")
	}
	// Terms over the length ceiling are left alone.
	if got := compoundCandidates(strings.Repeat("a", 40), []string{strings.Repeat("a", 40)}); len(got) != 0 {
		t.Errorf("overlong term produced %d candidates", len(got))
	}
	// A full term set cannot grow past the index's term limit.
	var full []string
	for i := 0; i < 16; i++ {
		full = append(full, fmt.Sprintf("word%02d", i))
	}
	for _, c := range compoundCandidates("word00", full) {
		if c.left != "" {
			t.Fatalf("split would exceed the term limit: %v", c)
		}
	}
}

func TestCompoundAdjacentWords(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"threat-bot is up", true},
		{"Threat Bot", true},
		{"threat_bot", true},
		{"threat.bot", true},
		{"THREAT - BOT", true},
		{"the threat was real. a bot posted it", false},
		{"threat\nbot", false},
		{"bot threat", false},
		{"threats bot", false},
		{"threatbot", false},
	} {
		if got := adjacentWords(tc.text, "threat", "bot"); got != tc.want {
			t.Errorf("%q: got %t", tc.text, got)
		}
	}
}

// countingSearch counts the index queries a client issues.
type countingSearch struct {
	*durable.Store
	queries atomic.Int64
}

func (s *countingSearch) SearchExact(ctx context.Context, query search.ExactQuery) (search.ExactPage, error) {
	s.queries.Add(1)
	return s.Store.SearchExact(ctx, query)
}

func TestCompoundPreviewShowsSourceSpelling(t *testing.T) {
	// The only match sits deep in a long capture and is spelled the other way:
	// the preview must be cut around the words the capture really contains.
	f := newCompoundFixture(t, "unrelated")
	long := strings.Repeat("Routine deployment notes without the product name. ", 400) + "Finally threat-bot was restarted by the operator." + strings.Repeat(" Trailing log line.", 200)
	observation, err := capture.SanitizeObservation("com.apple.terminal", "", long, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.(*durable.Store).PutObservationAt(context.Background(), observation, time.Now().Add(-3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	deep := observation.ContentID()
	half := f.put("editor.app", "A bot summarised the meeting.", time.Now().Add(-4*time.Minute))
	f.index()
	page, err := f.client.SearchMemoryPageContext(context.Background(), "threatbot")
	if err != nil {
		t.Fatal(err)
	}
	hit, _, ok := hitFor(page.Hits, deep)
	if !ok || hit.Relaxed || !strings.Contains(hit.Excerpt, "threat-bot") {
		t.Fatalf("deep respelled match: found=%t relaxed=%t excerpt=%q", ok, hit.Relaxed, hit.Excerpt)
	}
	for _, want := range []string{"threatbot", "threat", "bot"} {
		if !slices.Contains(hit.MatchedTerms, want) {
			t.Errorf("matched terms %v lack %q", hit.MatchedTerms, want)
		}
	}
	if _, _, ok := hitFor(page.Hits, half); ok {
		t.Error("a capture holding one half of the compound was returned")
	}
	// Ask cuts its evidence passage around the same words.
	plan, err := assistant.PlanQuestion("Find threatbot", time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := f.client.RetrievePlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	located := false
	for _, item := range retrieval.Evidence {
		if item.SourceID == deep {
			located = item.MatchEnd > item.MatchStart && strings.Contains(item.Text[item.MatchStart:item.MatchEnd], "threat-bot") && !item.TermsRelaxed
		}
	}
	if !located {
		t.Error("ask evidence for the deep respelled match has no passage around it")
	}
}

func TestCompoundKeepsOriginalSpellingHitsAndAddsFusedOnes(t *testing.T) {
	// Both spellings are saved in different captures. The question's own
	// spelling still matches, and the fused one is a full match beside it
	// instead of being hidden behind an incidental all-terms hit.
	f := newCompoundFixture(t, "ThreatBot")
	spaced := f.put("editor.app", "A threat was reported after the bot posted.", time.Now().Add(-5*time.Minute))
	f.index()
	hits, err := f.client.SearchMemoryContext(context.Background(), "threat bot")
	if err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]string{"fused": f.target, "as typed": spaced} {
		if hit, _, ok := hitFor(hits, id); !ok || hit.Relaxed {
			t.Errorf("%s capture: found=%t relaxed=%t", name, ok, hit.Relaxed)
		}
	}
}

func TestCompoundResolutionCostForOrdinaryQuestion(t *testing.T) {
	// A question whose words all occur as typed pays one existence probe per
	// adjacent word pair and nothing else: no split is attempted and no source
	// is read.
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	o, err := capture.SanitizeObservation("com.apple.terminal", "", "The deploy pipeline version 4.8.2 is running in production.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(ctx, o, time.Now()); err != nil {
		t.Fatal(err)
	}
	if more, err := s.IndexPendingExact(ctx, 32); err != nil || more {
		t.Fatal(more, err)
	}
	counted := &countingSearch{Store: s}
	c := NewClient(counted)
	t.Cleanup(c.Close)
	question := "pipeline version production"
	plan := assistant.Plan{Kind: assistant.KindLookup, Question: question, Terms: []string{"pipeline", "production", "version"}}
	filters, err := memoryFilters(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := compoundScope{filters: filters, windows: singleSearchWindow(searchScopeWindow{})}
	full := map[string]search.IndexHit{o.ContentID(): {SourceID: o.ContentID(), MatchedTerms: 0b111}}
	variant, limited, err := c.compoundVariantFor(ctx, plan, question, scope, full)
	if err != nil || variant != nil || limited {
		t.Fatalf("ordinary question was respelled: variant=%v limited=%t err=%v", variant != nil, limited, err)
	}
	if counted.queries.Load() != 2 { // "pipelineversion", "versionproduction"
		t.Fatalf("index queries = %d, want one per adjacent word pair (2)", counted.queries.Load())
	}
	// With no hit to learn from, each word costs one more existence probe.
	counted.queries.Store(0)
	if _, _, err := c.compoundVariantFor(ctx, plan, question, scope); err != nil {
		t.Fatal(err)
	}
	if counted.queries.Load() != 5 {
		t.Fatalf("index queries without prior hits = %d, want 5", counted.queries.Load())
	}
}

func TestCompoundSplitIsBoundedOnCrowdedStore(t *testing.T) {
	// Hundreds of captures hold both halves apart. The index cannot tell them
	// from the one that holds the compound, so the split is best effort: it
	// must stay inside its budgets, never fail the question, never present a
	// halves-apart capture as a full match, and report limited coverage
	// whenever it gives up with unread candidates.
	if testing.Short() {
		t.Skip("builds a store of a thousand captures")
	}
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	put := func(text string, at time.Time) string {
		o, err := capture.SanitizeObservation("com.apple.terminal", "", text, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(ctx, o, at); err != nil {
			t.Fatal(err)
		}
		return o.ContentID()
	}
	now := time.Now()
	apart := map[string]bool{}
	for i := 0; i < 300; i++ {
		apart[put(fmt.Sprintf("Incident %d: the threat model review finished. Later a bot posted the summary.", i), now.Add(-time.Duration(i+10)*time.Minute))] = true
	}
	for i := 0; i < 700; i++ {
		word := "version"
		if i%2 == 1 {
			word = "production"
		}
		put(fmt.Sprintf("Note %d mentions the %s only.", i, word), now.Add(-time.Duration(i+400)*time.Minute))
	}
	target := put("Terminal output: threat-bot is running in production. threat-bot version 4.8.2.", now.Add(-5*time.Minute))
	for {
		more, err := s.IndexPendingExact(ctx, 100)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	counted := &countingSearch{Store: s}
	c := NewClient(counted)
	t.Cleanup(c.Close)

	page, err := c.SearchMemoryPageContext(ctx, "threatbot")
	if err != nil {
		t.Fatalf("search failed on a crowded store: %v", err)
	}
	_, _, found := hitFor(page.Hits, target)
	for _, hit := range page.Hits {
		if apart[hit.SourceID] && !hit.Relaxed {
			t.Fatal("search presented a halves-apart capture as a full match")
		}
	}
	if !found && !page.CoverageLimited {
		t.Fatal("search gave up with unread candidates without reporting limited coverage")
	}
	t.Logf("search threatbot: target found=%t limited=%t hits=%d index queries=%d", found, page.CoverageLimited, len(page.Hits), counted.queries.Load())
	if counted.queries.Load() > 2*compoundMaxProbes+16 {
		t.Fatalf("search issued %d index queries", counted.queries.Load())
	}

	find, err := assistant.PlanQuestion("Find threatbot", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The plan Ask's memory probe builds for a factual question: its other
	// words narrow the candidates to the capture that holds them all.
	narrowed := assistant.Plan{Kind: assistant.KindLookup, Question: "What version of threatbot is production running?", Terms: []string{"production", "running", "threatbot", "version"}}
	for _, plan := range []assistant.Plan{find, narrowed} {
		question := plan.Question
		counted.queries.Store(0)
		retrieval, err := c.RetrievePlan(ctx, plan)
		if err != nil {
			t.Fatalf("ask %q failed on a crowded store: %v", question, err)
		}
		found := false
		for _, item := range retrieval.Evidence {
			found = found || item.SourceID == target
			if apart[item.SourceID] && !item.TermsRelaxed {
				t.Fatalf("ask %q presented a halves-apart capture as a full match", question)
			}
		}
		limited := retrieval.Diagnosis.SemanticIssue == "limited"
		if !found && !limited {
			t.Fatalf("ask %q gave up with unread candidates without reporting limited coverage", question)
		}
		t.Logf("ask %q: target found=%t limited=%t evidence=%d index queries=%d", question, found, limited, len(retrieval.Evidence), counted.queries.Load())
		if plan.Question == narrowed.Question && !found {
			t.Fatal("the question's other words identify the capture, but the split did not find it")
		}
	}

	// The joined direction needs no source read and stays exact at this size.
	fused := put("ThreatBot dashboard shows green.", now.Add(-2*time.Minute))
	if more, err := s.IndexPendingExact(ctx, 100); err != nil || more {
		t.Fatal(more, err)
	}
	hits, err := c.SearchMemoryContext(ctx, "threat bot")
	if err != nil {
		t.Fatal(err)
	}
	if hit, _, ok := hitFor(hits, fused); !ok || hit.Relaxed {
		t.Fatalf("joined spelling on a crowded store: found=%t relaxed=%t", ok, hit.Relaxed)
	}
}
