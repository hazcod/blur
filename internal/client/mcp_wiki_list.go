package client

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/localapi"
)

const (
	maxMCPWikiListLimit     = 50
	maxMCPWikiListSummary   = 160
	maxMCPWikiListName      = 256
	maxMCPWikiListAliases   = 4
	maxMCPWikiListAliasText = 128
)

type mcpWikiListParams struct {
	Type  string `json:"type,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type mcpWikiListEntry struct {
	ID            string         `json:"id"`
	Kind          knowledge.Kind `json:"kind"`
	Name          string         `json:"name"`
	Aliases       []string       `json:"aliases"`
	Summary       string         `json:"summary,omitempty"`
	FactCount     int            `json:"fact_count"`
	OpenLoopCount int            `json:"open_loop_count"`
}

type mcpWikiListResponse struct {
	Entries   []mcpWikiListEntry `json:"entries"`
	Total     int                `json:"total"`
	Truncated bool               `json:"truncated"`
}

func decodeMCPWikiListParams(raw []byte) (mcpWikiListParams, error) {
	var params mcpWikiListParams
	if err := decodeMCPParityParams(raw, &params); err != nil {
		return mcpWikiListParams{}, err
	}
	if params.Type != "" && !validLibraryKind(knowledge.Kind(params.Type)) || params.Limit < 0 || params.Limit > maxMCPWikiListLimit {
		return mcpWikiListParams{}, ErrInvalidRequest
	}
	if params.Limit == 0 {
		params.Limit = maxMCPWikiListLimit
	}
	return params, nil
}

// listMCPWiki is an orientation index of the Library: the best-supported
// subjects first. It reads only already-saved records and returns no sources;
// get_wiki_page opens details with their source references.
func (c *Client) listMCPWiki(ctx context.Context, epoch uint64, params mcpWikiListParams) (json.RawMessage, error) {
	entities, err := c.listLibraryEntities(ctx)
	if err != nil {
		return nil, err
	}
	type ranked struct {
		entry mcpWikiListEntry
		key   string
	}
	var rows []ranked
	for _, entity := range entities {
		if entity.State == knowledge.Dismissed || params.Type != "" && string(entity.Kind) != params.Type {
			continue
		}
		entry := mcpWikiListEntry{ID: entity.ID, Kind: entity.Kind, Name: truncateUTF8Bytes(entity.Name, maxMCPWikiListName), Aliases: []string{}}
		for _, alias := range entity.Aliases[:min(maxMCPWikiListAliases, len(entity.Aliases))] {
			entry.Aliases = append(entry.Aliases, truncateUTF8Bytes(alias, maxMCPWikiListAliasText))
		}
		for _, fact := range entity.Facts {
			if fact.State != knowledge.Confirmed {
				continue
			}
			if entry.FactCount == 0 {
				entry.Summary = truncateUTF8Bytes(fact.Text, maxMCPWikiListSummary)
			}
			entry.FactCount++
		}
		for _, loop := range entity.OpenLoops {
			if loop.State != knowledge.Dismissed {
				entry.OpenLoopCount++
			}
		}
		rows = append(rows, ranked{entry: entry, key: strings.ToLower(entity.Name)})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i].entry.FactCount+rows[i].entry.OpenLoopCount, rows[j].entry.FactCount+rows[j].entry.OpenLoopCount
		if left != right {
			return left > right
		}
		if rows[i].key != rows[j].key {
			return rows[i].key < rows[j].key
		}
		return rows[i].entry.ID < rows[j].entry.ID
	})
	response := mcpWikiListResponse{Entries: make([]mcpWikiListEntry, 0, min(params.Limit, len(rows))), Total: len(rows)}
	for _, row := range rows[:min(params.Limit, len(rows))] {
		response.Entries = append(response.Entries, row.entry)
	}
	response.Truncated = len(rows) > len(response.Entries)
	for len(response.Entries) > 0 && !mcpParityFits(response, localapi.MaxRPCSnapshotResponseBytes) {
		response.Entries = response.Entries[:len(response.Entries)-1]
		response.Truncated = true
	}
	return c.publishMCPParity(ctx, epoch, response, localapi.MaxRPCSnapshotResponseBytes)
}
