package client

import (
	"context"
	"math"
	"math/bits"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage"
)

// meetingsAppID is the app identity of saved meeting and voice transcripts.
// Spoken words answer "what did X say" like a chat message does.
const meetingsAppID = "sh.blur.meetings"

// lookupRarityTotal is the source count rarity weights are measured against.
// Document frequencies saturate at the index row cap, so the weights stay
// monotonic in rarity without a global source count.
const lookupRarityTotal = search.MaxIndexRows

// speechVerbWords are the verbs that make a question about what somebody
// communicated, so chat, mail and spoken-word captures outrank documents that
// merely mention the same words.
var speechVerbWords = map[string]struct{}{
	"say": {}, "says": {}, "said": {}, "saying": {},
	"tell": {}, "tells": {}, "told": {}, "telling": {},
	"write": {}, "writes": {}, "wrote": {}, "written": {}, "writing": {},
	"send": {}, "sends": {}, "sent": {}, "sending": {},
	"message": {}, "messages": {}, "messaged": {},
	"reply": {}, "replies": {}, "replied": {},
	"mention": {}, "mentions": {}, "mentioned": {},
	"ask": {}, "asks": {}, "asked": {},
	"promise": {}, "promises": {}, "promised": {},
}

func questionHasSpeechVerb(question string) bool {
	words, err := search.NormalizeTokens(question, 64)
	if err != nil {
		return false
	}
	for _, word := range words {
		if _, ok := speechVerbWords[word]; ok {
			return true
		}
	}
	return false
}

// lookupMajorityTerms is how many of n planned terms a source must contain to
// join a lookup's candidate pool: max(1, ceil(n/2)).
func lookupMajorityTerms(n int) int { return max(1, (n+1)/2) }

// lookupTermRarity is log(1 + total/df) for a term indexed in df of total
// sources. A term that no source contains carries no rarity.
func lookupTermRarity(df, total int) float64 {
	if df < 1 || total < 1 {
		return 0
	}
	return math.Log(1 + float64(total)/float64(df))
}

// filterLookupEntity keeps only hits whose indexed terms include every entity
// term. A zero mask means the plan names no entity and keeps every hit. Hits
// that report no matched terms cannot prove the entity and are dropped.
func filterLookupEntity(hits []search.IndexHit, entityMask uint32) []search.IndexHit {
	if entityMask == 0 {
		return hits
	}
	kept := make([]search.IndexHit, 0, len(hits))
	for _, hit := range hits {
		if hit.MatchedTerms&entityMask == entityMask {
			kept = append(kept, hit)
		}
	}
	return kept
}

type lookupRankInput struct {
	entityMask uint32
	speech     bool
	// isMessaging reports whether a source was captured from a chat, mail or
	// spoken-word app. It is only consulted when speech is set.
	isMessaging func(sourceID string) bool
	// rarity holds one weight per query term, indexed like the query's terms.
	rarity []float64
	// capturedAt resolves a capture time for hits the index did not date.
	capturedAt func(search.IndexHit) time.Time
	// closeness, when set, scores how tightly a hit's matched terms sit
	// together (smaller is tighter; zero or less is unknown and ranks last). It
	// breaks ties after term rarity and before capture time. Ask leaves it nil.
	closeness func(search.IndexHit) int
	// demote marks hits that are chrome rather than substance (sign-in pages).
	// Marked hits are dropped before the pool is cut, unless nothing else is left.
	demote func(search.IndexHit) bool
	// limit caps the ranked pool; zero keeps every hit.
	limit int
}

// rankLookupHits orders a lookup candidate pool: entity matched, messaging-app
// boost when the question has a speech verb, matched-term count, term rarity
// sum, optional term closeness, then capture time with the newest first. Source ID is the final
// deterministic tie-break.
func rankLookupHits(hits []search.IndexHit, in lookupRankInput) []search.IndexHit {
	type key struct {
		entity, messaging bool
		matched           int
		rarity            float64
		closeness         int
		at                time.Time
	}
	keys := make(map[string]key, len(hits))
	for _, hit := range hits {
		k := key{entity: in.entityMask != 0 && hit.MatchedTerms&in.entityMask == in.entityMask, matched: bits.OnesCount32(hit.MatchedTerms), at: hit.CapturedAt}
		if in.speech && in.isMessaging != nil {
			k.messaging = in.isMessaging(hit.SourceID)
		}
		for i, weight := range in.rarity {
			if hit.MatchedTerms&(1<<uint(i)) != 0 {
				k.rarity += weight
			}
		}
		k.closeness = math.MaxInt
		if in.closeness != nil {
			if value := in.closeness(hit); value > 0 {
				k.closeness = value
			}
		}
		if k.at.IsZero() && in.capturedAt != nil {
			k.at = in.capturedAt(hit)
		}
		keys[hit.SourceID] = k
	}
	ranked := append([]search.IndexHit(nil), hits...)
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := keys[ranked[i].SourceID], keys[ranked[j].SourceID]
		if a.entity != b.entity {
			return a.entity
		}
		if a.messaging != b.messaging {
			return a.messaging
		}
		if a.matched != b.matched {
			return a.matched > b.matched
		}
		if a.rarity != b.rarity {
			return a.rarity > b.rarity
		}
		if a.closeness != b.closeness {
			return a.closeness < b.closeness
		}
		if !a.at.Equal(b.at) {
			return a.at.After(b.at)
		}
		return ranked[i].SourceID < ranked[j].SourceID
	})
	if in.demote != nil {
		kept := make([]search.IndexHit, 0, len(ranked))
		for _, hit := range ranked {
			if !in.demote(hit) {
				kept = append(kept, hit)
			}
		}
		if len(kept) > 0 {
			ranked = kept
		}
	}
	if in.limit > 0 && len(ranked) > in.limit {
		ranked = ranked[:in.limit]
	}
	return ranked
}

// termsRelaxed reports a hit that matched some but not all of the planned
// terms. A zero mask means the index did not report matches.
func termsRelaxed(mask uint32, termCount int) bool {
	return mask != 0 && bits.OnesCount32(mask) < min(termCount, search.MaxTerms)
}

// isConversationalSource reports a capture of people talking: a chat or mail
// app, or a spoken transcript. A user's own meeting note is not speech.
func isConversationalSource(appID, domain string, spokenTranscript bool) bool {
	if appID == meetingsAppID {
		return spokenTranscript
	}
	return appidentity.IsMessagingApp(appID) || appidentity.IsMessagingApp(domain)
}

var loginCueWords = map[string]bool{"login": true, "logins": true, "signin": true, "okta": true, "sso": true, "authentication": true}

// questionHasLoginCue reports a question that is about signing in, where
// sign-in screens are the substance and must not be demoted.
func questionHasLoginCue(question string) bool {
	var ordered []string
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			ordered = append(ordered, word.String())
			word.Reset()
		}
	}
	for _, r := range strings.ToLower(question) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	for i, w := range ordered {
		if loginCueWords[w] {
			return true
		}
		if i+1 < len(ordered) {
			switch w + " " + ordered[i+1] {
			case "sign in", "log in", "password page", "sign on":
				return true
			}
		}
	}
	return false
}

// lookupEntityMask is the bit set of query terms that spell the plan's entity.
// Entity words the planner did not keep as terms cannot be required.
func lookupEntityMask(entity string, terms []string) uint32 {
	words, err := search.NormalizeTokens(entity, search.MaxTerms)
	if err != nil {
		return 0
	}
	var mask uint32
	for _, word := range words {
		if i := slices.Index(terms, word); i >= 0 {
			mask |= 1 << uint(i)
		}
	}
	return mask
}

// termDocumentFrequency is how many indexed sources contain the term, capped
// by the index scan budget.
func (c *Client) termDocumentFrequency(ctx context.Context, term string) (int, error) {
	page, err := c.data.SearchExact(ctx, search.ExactQuery{Terms: []string{term}, ExcludeAppIDs: []string{appidentity.BundleID}, TopK: search.MaxResults, MaxRows: search.MaxIndexRows, MaxBytes: search.MaxIndexBytes})
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return search.MaxIndexRows, nil // unknown frequency: treat as common
	}
	return max(page.Rows, len(page.Hits)), nil
}

// lookupSourceFacts is what ranking and demotion read from a candidate source.
type lookupSourceFacts struct {
	conversational bool
	authentication bool
}

type meetingRecordKindReader interface {
	MeetingRecordKindForSource(context.Context, string) (meetings.RecordKind, bool, error)
}

// readLookupFacts reads each candidate once, bounded by the same aggregate byte
// ceiling that hydration uses. Candidates past the budget carry no facts and are
// treated as ordinary captures. No metadata-only reader exists, so the read
// that supplies the app identity also supplies the page text for demotion.
func (c *Client) readLookupFacts(ctx context.Context, hits []search.IndexHit) (map[string]lookupSourceFacts, error) {
	facts := make(map[string]lookupSourceFacts, len(hits))
	used := 0
	kinds, _ := c.data.(meetingRecordKindReader)
	for _, hit := range hits {
		if hit.Bytes < 1 || hit.Bytes > MaxSearchBytes-used {
			continue
		}
		source, found, err := storage.ReadSourceEvidence(ctx, c.data, hit.SourceID)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		used += hit.Bytes
		spoken := false
		if source.AppID() == meetingsAppID && kinds != nil {
			kind, live, err := kinds.MeetingRecordKindForSource(ctx, hit.SourceID)
			if err != nil {
				return nil, err
			}
			spoken = live && kind == meetings.TranscriptFinal
		}
		facts[hit.SourceID] = lookupSourceFacts{
			conversational: isConversationalSource(source.AppID(), source.Domain(), spoken),
			authentication: looksLikeAuthenticationPage(source.Text()),
		}
	}
	return facts, nil
}

// rankLookupPool merges all-terms and majority-term candidates into one pool,
// requires the plan's entity, ranks it with rankLookupHits, drops sign-in pages
// that only partly match (unless the question is about signing in) and only
// then cuts the pool, so demoted chrome cannot crowd out substantive candidates.
func (c *Client) rankLookupPool(ctx context.Context, plan assistant.Plan, terms []string, exact, partial []search.IndexHit, pool int) ([]search.IndexHit, error) {
	entityMask := lookupEntityMask(plan.Entity, terms)
	hits := filterLookupEntity(append(append([]search.IndexHit(nil), exact...), partial...), entityMask)
	in := lookupRankInput{entityMask: entityMask, speech: questionHasSpeechVerb(plan.Question), limit: pool}
	if len(partial) > 0 && len(hits) > 1 {
		in.rarity = make([]float64, len(terms))
		for i, term := range terms {
			df, err := c.termDocumentFrequency(ctx, term)
			if err != nil {
				return nil, err
			}
			in.rarity[i] = lookupTermRarity(df, lookupRarityTotal)
		}
	}
	demoting := !questionHasLoginCue(plan.Question)
	relaxed := func(hit search.IndexHit) bool { return termsRelaxed(hit.MatchedTerms, len(terms)) }
	if len(hits) > 1 && (in.speech || demoting) {
		var read []search.IndexHit
		for _, hit := range hits {
			if in.speech || relaxed(hit) {
				read = append(read, hit)
			}
		}
		facts, err := c.readLookupFacts(ctx, read)
		if err != nil {
			return nil, err
		}
		if in.speech {
			in.isMessaging = func(id string) bool { return facts[id].conversational }
		}
		if demoting {
			in.demote = func(hit search.IndexHit) bool { return relaxed(hit) && facts[hit.SourceID].authentication }
		}
	}
	if len(hits) > 1 {
		in.capturedAt = func(hit search.IndexHit) time.Time {
			at, unknown, err := c.sourceCaptureTime(ctx, hit.SourceID, time.Time{})
			if err != nil || unknown {
				return time.Time{}
			}
			return at
		}
	}
	return rankLookupHits(hits, in), nil
}
