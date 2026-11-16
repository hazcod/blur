package client

import (
	"context"
	"math"
	"math/bits"
	"slices"
	"sort"
	"time"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/assistant"
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
	// limit caps the ranked pool; zero keeps every hit.
	limit int
}

// rankLookupHits orders a lookup candidate pool: entity matched, messaging-app
// boost when the question has a speech verb, matched-term count, term rarity
// sum, then capture time with the newest first. Source ID is the final
// deterministic tie-break.
func rankLookupHits(hits []search.IndexHit, in lookupRankInput) []search.IndexHit {
	type key struct {
		entity, messaging bool
		matched           int
		rarity            float64
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
		if !a.at.Equal(b.at) {
			return a.at.After(b.at)
		}
		return ranked[i].SourceID < ranked[j].SourceID
	})
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

func isConversationalSource(appID, domain string) bool {
	return appID == meetingsAppID || appidentity.IsMessagingApp(appID) || appidentity.IsMessagingApp(domain)
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

// rankLookupPool merges all-terms and majority-term candidates into one pool
// capped at pool hits, requires the plan's entity, and orders it with
// rankLookupHits.
func (c *Client) rankLookupPool(ctx context.Context, plan assistant.Plan, terms []string, exact, partial []search.IndexHit, pool int) ([]search.IndexHit, error) {
	hits := filterLookupEntity(append(append([]search.IndexHit(nil), exact...), partial...), lookupEntityMask(plan.Entity, terms))
	in := lookupRankInput{entityMask: lookupEntityMask(plan.Entity, terms), speech: questionHasSpeechVerb(plan.Question), limit: pool}
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
	if in.speech && len(hits) > 1 {
		conversational := make(map[string]bool, len(hits))
		for _, hit := range hits {
			source, found, err := storage.ReadSourceEvidence(ctx, c.data, hit.SourceID)
			if err != nil {
				return nil, err
			}
			conversational[hit.SourceID] = found && isConversationalSource(source.AppID(), source.Domain())
		}
		in.isMessaging = func(id string) bool { return conversational[id] }
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
