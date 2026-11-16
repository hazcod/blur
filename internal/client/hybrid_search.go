package client

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/semantic"
)

// hybridPage keeps exact retrieval immediate and invokes the selected model
// only on a miss. Explicit legacy controllers retain vector fusion support.
func (c *Client) hybridPage(ctx context.Context, question string, exact search.ExactPage, scope semantic.Query, limit int, minimum ...float32) (search.ExactPage, error) {
	return c.hybridPageWithPolicies(ctx, question, exact, scope, limit, true, minimum...)
}

// hybridAskPage uses the selected model to reject off-topic exact hits before
// they are supplied as evidence for the final answer.
func (c *Client) hybridAskPage(ctx context.Context, question string, exact search.ExactPage, scope semantic.Query, limit int, minimum ...float32) (search.ExactPage, error) {
	return c.hybridPageWithPolicies(ctx, question, exact, scope, limit, true, minimum...)
}

func (c *Client) hybridPageWithPolicies(ctx context.Context, question string, exact search.ExactPage, scope semantic.Query, limit int, rankExactQuestions bool, minimum ...float32) (search.ExactPage, error) {
	// Plain topic searches stay immediate. Natural-language questions may have
	// incidental exact matches, so Search and Ask let the selected local model
	// rank those candidates before publishing them.
	if c.localAI != nil || c.memoryIndex == nil || !c.memoryIndex.Settings().Enabled {
		if len(exact.Hits) > 0 && (!rankExactQuestions || !naturalLanguageRecallQuery(question)) {
			return exact, nil
		}
		return c.selectedModelPage(ctx, question, exact, scope, limit, minimum...)
	}
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return search.ExactPage{}, ErrMemoryChanging
	}
	queryCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	semanticHits, err := c.memoryIndex.Search(queryCtx, question, scope)
	if ctx.Err() != nil {
		return search.ExactPage{}, ctx.Err()
	}
	if !c.MemoryReadCurrent(epoch) {
		return search.ExactPage{}, ErrMemoryChanging
	}
	if err != nil {
		exact.CoverageLimited = true
		return exact, nil
	}
	return fuseSearchPages(exact, semanticHits, limit, minimum...), nil
}

func naturalLanguageRecallQuery(question string) bool {
	allTerms, err := search.NormalizeTokens(strings.TrimSpace(question), search.MaxTerms)
	if err != nil || len(allTerms) == 0 {
		return false
	}
	topic, err := assistant.QuestionSearchQuery(question)
	if err != nil {
		return false
	}
	topicTerms, err := search.NormalizeTokens(topic, search.MaxTerms)
	return err == nil && len(topicTerms) > 0 && len(topicTerms) < len(allTerms)
}

func fuseSearchPages(exact search.ExactPage, semanticHits []semantic.Hit, limit int, minimum ...float32) search.ExactPage {
	type ranked struct {
		hit   search.IndexHit
		score float64
		exact bool
	}
	minimumScore := float32(.3)
	if len(minimum) > 0 && minimum[0] >= .3 && minimum[0] <= 1 {
		minimumScore = minimum[0]
	}
	byID := make(map[string]ranked, len(exact.Hits)+len(semanticHits))
	for rank, hit := range exact.Hits {
		byID[hit.SourceID] = ranked{hit: hit, score: 1 / float64(61+rank), exact: true}
	}
	for rank, hit := range semanticHits {
		if hit.Score < minimumScore || hit.Score > 1.001 || math.IsNaN(float64(hit.Score)) || !semantic.ValidID(hit.SourceID) || hit.SourceBytes < 1 || hit.Start < 0 || hit.End <= hit.Start {
			continue
		}
		item, exists := byID[hit.SourceID]
		if !exists {
			item.hit = search.IndexHit{SourceID: hit.SourceID, Bytes: hit.SourceBytes, CapturedAt: hit.CapturedAt}
		}
		item.score += 1 / float64(61+rank)
		item.hit.MatchStart, item.hit.MatchEnd = hit.Start, hit.End
		byID[hit.SourceID] = item
	}
	items := make([]ranked, 0, len(byID))
	for _, item := range byID {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		if items[i].exact != items[j].exact {
			return items[i].exact
		}
		return items[i].hit.SourceID < items[j].hit.SourceID
	})
	page := search.ExactPage{Rows: exact.Rows, Nodes: exact.Nodes, HasMore: exact.HasMore, IndexingPending: exact.IndexingPending, CoverageLimited: exact.CoverageLimited}
	for _, item := range items {
		if len(page.Hits) >= limit {
			page.HasMore = true
			break
		}
		if item.hit.Bytes > MaxSearchBytes-page.Bytes {
			page.HasMore = true
			continue
		}
		page.Hits = append(page.Hits, item.hit)
		page.Bytes += item.hit.Bytes
	}
	return page
}
