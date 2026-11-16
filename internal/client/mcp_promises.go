package client

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/tasks"
)

const (
	maxMCPPromiseLimit      = 50
	defaultMCPPromiseLimit  = 20
	maxMCPPromiseText       = 600
	maxMCPPromiseSources    = 8
	maxMCPPromiseTimeLookup = 256
	maxMCPPromiseTaskPages  = 8
	mcpPromiseNote          = "These are observed or user-authored items from Blur's saved Library and tasks, not verified outcomes. An item is only closed when the user marks it done or dismisses it; treat anything else as unconfirmed until saved source evidence (open source_ids with get_snapshots) shows it was completed."
)

type mcpPromiseParams struct {
	Query         string `json:"query,omitempty"`
	IncludeClosed bool   `json:"include_closed,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

type mcpPromiseItem struct {
	Kind         string   `json:"kind"`
	ID           string   `json:"id"`
	Text         string   `json:"text"`
	State        string   `json:"state"`
	Subject      string   `json:"subject,omitempty"`
	SubjectKind  string   `json:"subject_kind,omitempty"`
	DueAt        *string  `json:"due_at"`
	SourceIDs    []string `json:"source_ids"`
	UserAuthored bool     `json:"user_authored,omitempty"`
	UpdatedAt    string   `json:"updated_at,omitempty"`
}

type mcpPromiseResponse struct {
	Items           []mcpPromiseItem `json:"items"`
	HasMore         bool             `json:"has_more"`
	CoverageLimited bool             `json:"coverage_limited,omitempty"`
	Note            string           `json:"note"`
}

func decodeMCPPromiseParams(raw []byte) (mcpPromiseParams, error) {
	var params mcpPromiseParams
	if err := decodeMCPParityParams(raw, &params); err != nil {
		return mcpPromiseParams{}, err
	}
	params.Query = strings.TrimSpace(params.Query)
	if len(params.Query) > MaxQueryBytes || !validMCPParityText(params.Query, MaxQueryBytes) || params.Limit < 0 || params.Limit > maxMCPPromiseLimit {
		return mcpPromiseParams{}, ErrInvalidRequest
	}
	if params.Limit == 0 {
		params.Limit = defaultMCPPromiseLimit
	}
	return params, nil
}

type mcpPromiseCandidate struct {
	item mcpPromiseItem
	at   time.Time
}

func mcpPromiseMatches(query string, parts ...string) bool {
	if query == "" {
		return true
	}
	query = strings.ToLower(query)
	for _, part := range parts {
		if strings.Contains(strings.ToLower(part), query) {
			return true
		}
	}
	return false
}

// readMCPPromises is a follow-through view over records that already exist:
// Library open loops and the same tasks/reminders list_tasks reads. It never
// infers completion; closed items appear only when explicitly requested.
func (c *Client) readMCPPromises(ctx context.Context, epoch uint64, params mcpPromiseParams) (json.RawMessage, error) {
	response := mcpPromiseResponse{Items: []mcpPromiseItem{}, Note: mcpPromiseNote}
	var candidates []mcpPromiseCandidate

	entities, err := c.listLibraryEntities(ctx)
	if err != nil {
		return nil, err
	}
	lookups := 0
	times := map[string]time.Time{}
	sourceTime := func(id string) time.Time {
		if at, ok := times[id]; ok {
			return at
		}
		if lookups >= maxMCPPromiseTimeLookup {
			response.CoverageLimited = true
			return time.Time{}
		}
		lookups++
		at, unknown, err := c.sourceCaptureTime(ctx, id, time.Time{})
		if err != nil || unknown {
			at = time.Time{}
		}
		times[id] = at
		return at
	}
	for _, entity := range entities {
		for _, loop := range entity.OpenLoops {
			closed := loop.State == knowledge.Dismissed || entity.State == knowledge.Dismissed
			if !closed && loop.State != knowledge.Proposed && loop.State != knowledge.Confirmed || closed && !params.IncludeClosed {
				continue
			}
			if !mcpPromiseMatches(params.Query, loop.Text, entity.Name) {
				continue
			}
			item := mcpPromiseItem{Kind: "open_loop", ID: loop.ID, Text: truncateUTF8Bytes(loop.Text, maxMCPPromiseText), State: string(loop.State), Subject: truncateUTF8Bytes(entity.Name, 256), SubjectKind: string(entity.Kind), SourceIDs: []string{}}
			if closed {
				item.State = string(knowledge.Dismissed)
			}
			var newest time.Time
			for _, source := range loop.Sources {
				item.UserAuthored = item.UserAuthored || source.UserAuthored
				if source.ID == "" || !validMCPSourceID(source.ID) {
					continue
				}
				if len(item.SourceIDs) < maxMCPPromiseSources {
					item.SourceIDs = append(item.SourceIDs, source.ID)
				}
				if at := sourceTime(source.ID); at.After(newest) {
					newest = at
				}
			}
			if !newest.IsZero() {
				item.UpdatedAt = newest.UTC().Format(time.RFC3339Nano)
			}
			candidates = append(candidates, mcpPromiseCandidate{item: item, at: newest})
		}
	}

	if _, ok := c.taskRepository(); ok {
		state := tasks.Open
		if params.IncludeClosed {
			state = ""
		}
		after := ""
		for range maxMCPPromiseTaskPages {
			page, err := c.TaskPage(ctx, after, state)
			if err != nil {
				return nil, err
			}
			for _, task := range page.Items {
				if task.Validate() != nil {
					return nil, ErrInvalidRequest
				}
				if !params.IncludeClosed && task.State != tasks.Open || !mcpPromiseMatches(params.Query, task.Title) {
					continue
				}
				item := mcpPromiseItem{Kind: "task", ID: task.ID, Text: truncateUTF8Bytes(task.Title, maxMCPPromiseText), State: string(task.State), SourceIDs: task.Sources(), UserAuthored: task.UserAuthored, UpdatedAt: task.UpdatedAt.UTC().Format(time.RFC3339Nano)}
				if len(item.SourceIDs) > maxMCPPromiseSources {
					item.SourceIDs = item.SourceIDs[:maxMCPPromiseSources]
				}
				if item.SourceIDs == nil {
					item.SourceIDs = []string{}
				}
				if task.Reminder != nil {
					item.Kind = "reminder"
					due := task.Reminder.DueAt.UTC().Format(time.RFC3339Nano)
					item.DueAt = &due
				}
				candidates = append(candidates, mcpPromiseCandidate{item: item, at: task.UpdatedAt})
			}
			if !page.HasMore {
				break
			}
			if page.Next == "" || page.Next == after {
				return nil, ErrInvalidRequest
			}
			after = page.Next
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if !candidates[i].at.Equal(candidates[j].at) {
			return candidates[i].at.After(candidates[j].at)
		}
		if candidates[i].item.Kind != candidates[j].item.Kind {
			return candidates[i].item.Kind < candidates[j].item.Kind
		}
		return candidates[i].item.ID < candidates[j].item.ID
	})
	for _, candidate := range candidates[:min(params.Limit, len(candidates))] {
		if !utf8.ValidString(candidate.item.Text) {
			return nil, ErrInvalidRequest
		}
		response.Items = append(response.Items, candidate.item)
	}
	response.HasMore = len(candidates) > len(response.Items)
	for len(response.Items) > 0 && !mcpParityFits(response, localapi.MaxRPCSnapshotResponseBytes) {
		response.Items = response.Items[:len(response.Items)-1]
		response.HasMore = true
	}
	return c.publishMCPParity(ctx, epoch, response, localapi.MaxRPCSnapshotResponseBytes)
}
