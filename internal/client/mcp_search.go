package client

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hazcod/blur/internal/assistant"
)

const (
	// maxMCPSearchResults is the largest page search_memory returns.
	maxMCPSearchResults = 30
)

type mcpSearchParams struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// mcpSearchItem is one capture row. Excerpt is a preview: the full text is
// read through get_snapshots, and a row names only what the index matched.
type mcpSearchItem struct {
	SourceID      string    `json:"source_id"`
	AppID         string    `json:"app_id"`
	WindowTitle   string    `json:"window_title,omitempty"`
	Domain        string    `json:"domain,omitempty"`
	TimeUnknown   bool      `json:"capture_time_unknown,omitempty"`
	CapturedAt    time.Time `json:"captured_at,omitzero"`
	LastSeenAt    string    `json:"last_seen_at,omitempty"`
	SightingCount int64     `json:"sighting_count,omitempty"`
	Partial       bool      `json:"partial_capture,omitempty"`
	Excerpt       string    `json:"excerpt"`
	MatchedTerms  []string  `json:"matched_terms,omitempty"`
	PartialMatch  bool      `json:"partial_match,omitempty"`
}

type mcpSearchResponse struct {
	Results         []mcpSearchItem    `json:"results"`
	Summaries       []MemorySummaryHit `json:"summaries,omitempty"`
	Relaxed         bool               `json:"relaxed,omitempty"`
	Truncated       bool               `json:"truncated"`
	IndexingPending bool               `json:"indexing_pending,omitempty"`
	CoverageLimited bool               `json:"coverage_limited,omitempty"`
}

// searchMCPMemory answers search_memory from the shared search core. The
// response is previews and index rows, sized to the MCP byte budget.
func (c *Client) searchMCPMemory(ctx context.Context, epoch uint64, params mcpSearchParams) (json.RawMessage, error) {
	page, err := c.memorySearchPage(ctx, params.Query, params.Limit)
	if err != nil {
		return nil, err
	}
	type row struct {
		hit        SearchHit
		start, end int
		sightings  int64
		lastSeen   string
	}
	rows := make([]row, 0, len(page.Hits))
	truncated := page.HasMore
	used, unavailable := 0, 0
	var validation assistant.Result
	for _, hit := range page.Hits {
		cost := len(hit.Text) + len(hit.AppID) + len(hit.Domain)
		if cost < 1 || cost > maxMCPReadSourceBytes-used {
			truncated = true
			break
		}
		used += cost
		item := row{hit: hit}
		if hit.Task != nil {
			validation.Targets = append(validation.Targets, assistant.TargetRef{Kind: "task", ID: hit.Task.ID, Revision: hit.Task.Revision, SourceID: hit.SourceID})
			validation.Sources = append(validation.Sources, assistant.Evidence{SourceID: hit.SourceID, Text: hit.Text})
		} else {
			item.sightings, item.lastSeen, err = c.mcpSighting(ctx, hit.SourceID, hit.CapturedAt)
			if err != nil {
				return nil, err
			}
			if item.sightings == 0 && item.lastSeen == "" {
				unavailable++
			}
			item.start, item.end = queryMatchSpan(hit.Text, page.terms, hit.MatchStart, hit.MatchEnd, searchExcerptBytes)
		}
		rows = append(rows, item)
	}
	noteSightingUnavailable(unavailable)
	if err := c.validateTaskTargets(ctx, validation); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	build := func(excerptBytes int) mcpSearchResponse {
		response := mcpSearchResponse{Results: make([]mcpSearchItem, 0, len(rows)), Relaxed: page.Relaxed, Truncated: truncated, IndexingPending: page.IndexingPending, CoverageLimited: page.CoverageLimited}
		for _, r := range rows {
			response.Results = append(response.Results, mcpSearchItem{
				SourceID: r.hit.SourceID, AppID: r.hit.AppID, WindowTitle: r.hit.WindowTitle, Domain: r.hit.Domain,
				TimeUnknown: r.hit.TimeUnknown, CapturedAt: r.hit.CapturedAt, LastSeenAt: r.lastSeen, SightingCount: r.sightings,
				Partial: r.hit.Partial, Excerpt: Excerpt(r.hit.Text, r.start, r.end, excerptBytes), MatchedTerms: r.hit.MatchedTerms, PartialMatch: r.hit.Relaxed,
			})
		}
		for _, summary := range page.Summaries {
			summary.Excerpt = QueryExcerpt(summary.text, summary.terms, 0, 0, excerptBytes)
			response.Summaries = append(response.Summaries, summary)
		}
		return response
	}
	response, err := fitMCPSearchResponse(build, maxMCPReadOutputBytes)
	if err != nil {
		return nil, err
	}
	var encoded []byte
	if !c.PublishMemoryRead(epoch, func() { encoded, err = json.Marshal(response) }) {
		return nil, ErrMemoryChanging
	}
	if err != nil || len(encoded) > maxMCPReadOutputBytes {
		clear(encoded)
		return nil, ErrInvalidRequest
	}
	return encoded, nil
}

// fitMCPSearchResponse meets the byte budget in a fixed order: shorten every
// excerpt, never below 80 bytes, and only then drop capture rows from the end,
// marking the response truncated. build renders the whole response with the
// given excerpt size.
func fitMCPSearchResponse(build func(excerptBytes int) mcpSearchResponse, budget int) (mcpSearchResponse, error) {
	fits := func(response mcpSearchResponse) (bool, error) {
		encoded, err := json.Marshal(response)
		if err != nil {
			return false, ErrInvalidRequest
		}
		return len(encoded) <= budget, nil
	}
	var response mcpSearchResponse
	for size := searchExcerptBytes; size >= minSearchExcerptBytes; size -= 16 {
		response = build(size)
		if ok, err := fits(response); err != nil || ok {
			return response, err
		}
	}
	response = build(minSearchExcerptBytes)
	for len(response.Results) > 0 {
		response.Results = response.Results[:len(response.Results)-1]
		response.Truncated = true
		if ok, err := fits(response); err != nil || ok {
			return response, err
		}
	}
	// Summaries alone never exceed the budget (five bounded rows), but a
	// response that still cannot fit is refused rather than sent oversized.
	return mcpSearchResponse{}, ErrInvalidRequest
}
