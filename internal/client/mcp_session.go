package client

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/storage"
)

const (
	maxMCPSessionLimit        = 20
	defaultMCPSessionLimit    = 10
	maxMCPSessionWindow       = 24 * time.Hour
	defaultMCPSessionChunk    = 2048
	maxMCPSessionScan         = 512
	maxMCPSessionAppMatches   = 4
	mcpSessionItemOverheadEst = 256
)

type mcpSessionParams struct {
	App               string `json:"app"`
	From              int64  `json:"from"`
	To                int64  `json:"to"`
	Limit             int    `json:"limit,omitempty"`
	After             string `json:"after,omitempty"`
	MaxBytesPerSource int    `json:"max_bytes_per_source,omitempty"`
}

type mcpSessionCapture struct {
	SourceID        string `json:"source_id"`
	AppID           string `json:"app_id"`
	Domain          string `json:"domain,omitempty"`
	CapturedAt      string `json:"captured_at,omitempty"`
	LastSeenAt      string `json:"last_seen_at"`
	Content         string `json:"content"`
	OffsetBytes     int    `json:"offset_bytes"`
	NextOffsetBytes int    `json:"next_offset_bytes"`
	Complete        bool   `json:"complete"`
	PartialCapture  bool   `json:"partial_capture,omitempty"`
}

type mcpSessionResponse struct {
	AppIDs          []string            `json:"app_ids"`
	From            int64               `json:"from"`
	To              int64               `json:"to"`
	Captures        []mcpSessionCapture `json:"captures"`
	HasMore         bool                `json:"has_more"`
	Next            string              `json:"next,omitempty"`
	IndexingPending bool                `json:"indexing_pending,omitempty"`
	CoverageLimited bool                `json:"coverage_limited,omitempty"`
}

func decodeMCPSessionParams(raw []byte) (mcpSessionParams, error) {
	var params mcpSessionParams
	if err := decodeMCPParityParams(raw, &params); err != nil {
		return mcpSessionParams{}, err
	}
	params.App = strings.TrimSpace(params.App)
	if params.App == "" || !validMCPParityText(params.App, 128) {
		return mcpSessionParams{}, ErrInvalidRequest
	}
	if params.From <= 0 || params.To <= params.From || time.Duration(params.To-params.From)*time.Second > maxMCPSessionWindow {
		return mcpSessionParams{}, ErrInvalidRequest
	}
	if params.Limit < 0 || params.Limit > maxMCPSessionLimit {
		return mcpSessionParams{}, ErrInvalidRequest
	}
	if params.Limit == 0 {
		params.Limit = defaultMCPSessionLimit
	}
	if params.MaxBytesPerSource != 0 && (params.MaxBytesPerSource < 4 || params.MaxBytesPerSource > maxMCPReadSnapshotChunk) {
		return mcpSessionParams{}, ErrInvalidRequest
	}
	if params.MaxBytesPerSource == 0 {
		params.MaxBytesPerSource = defaultMCPSessionChunk
	}
	if params.After != "" && !validMCPSourceID(params.After) {
		return mcpSessionParams{}, ErrInvalidRequest
	}
	return params, nil
}

// resolveMCPSessionApps maps a host-supplied name to stored app IDs by
// case-insensitive app ID or display name. An unmatched value is tried as a
// literal app ID so apps older than the KnownApps horizon still work.
func (c *Client) resolveMCPSessionApps(ctx context.Context, name string) ([]string, error) {
	known, err := c.KnownApps(ctx)
	if err != nil {
		if cancelled := ctx.Err(); cancelled != nil {
			return nil, cancelled
		}
		known = nil
	}
	var ids []string
	seen := map[string]bool{}
	for _, app := range known {
		if app.Domain || seen[app.ID] || !strings.EqualFold(app.ID, name) && !strings.EqualFold(app.Name, name) {
			continue
		}
		// The capture index stores lower-case app identities.
		id := strings.ToLower(app.ID)
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
		if len(ids) == maxMCPSessionAppMatches {
			break
		}
	}
	if len(ids) == 0 {
		ids = append(ids, strings.ToLower(name))
	}
	return ids, nil
}

type mcpSessionEntry struct {
	id   string
	app  string
	seen time.Time
}

func (c *Client) readMCPSession(ctx context.Context, epoch uint64, params mcpSessionParams) (json.RawMessage, error) {
	reader, ok := c.data.(ObservationWindowReader)
	if !ok {
		return nil, ErrNotConfigured
	}
	start, end := time.Unix(params.From, 0).UTC(), time.Unix(params.To, 0).UTC()
	apps, err := c.resolveMCPSessionApps(ctx, params.App)
	if err != nil {
		return nil, err
	}
	response := mcpSessionResponse{AppIDs: []string{}, From: params.From, To: params.To, Captures: []mcpSessionCapture{}}
	var entries []mcpSessionEntry
	for _, app := range apps {
		if strings.EqualFold(app, appidentity.BundleID) {
			continue
		}
		response.AppIDs = append(response.AppIDs, app)
		before, beforeID := time.Time{}, ""
		for scanned := 0; ; {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			page, err := readSourceWindow(ctx, reader, storage.ObservationWindowRequest{
				Start: start, End: end, AppID: app, ExcludeAppIDs: []string{appidentity.BundleID},
				BeforeCapturedAt: before, BeforeSourceID: beforeID,
				Limit: storage.MaxObservationPageRows, MaxBytes: storage.MaxObservationPageBytes,
			})
			if err != nil {
				return nil, err
			}
			response.IndexingPending = response.IndexingPending || page.Pending
			for i, source := range page.Sources {
				entries = append(entries, mcpSessionEntry{id: source.SourceID(), app: source.AppID(), seen: page.CapturedAt[i]})
			}
			scanned += len(page.Sources)
			if !page.HasMore {
				break
			}
			if page.NextSourceID == "" || page.NextCapturedAt.IsZero() || len(page.Sources) == 0 {
				return nil, ErrInvalidRequest
			}
			if scanned >= maxMCPSessionScan {
				response.CoverageLimited = true
				break
			}
			before, beforeID = page.NextCapturedAt, page.NextSourceID
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].seen.Equal(entries[j].seen) {
			return entries[i].seen.Before(entries[j].seen)
		}
		return entries[i].id < entries[j].id
	})
	first := 0
	if params.After != "" {
		first = -1
		for i, entry := range entries {
			if entry.id == params.After {
				first = i + 1
				break
			}
		}
		if first < 0 {
			return nil, ErrInvalidRequest
		}
	}
	used := 0
	for index := first; index < len(entries); index++ {
		if len(response.Captures) == params.Limit {
			response.HasMore = true
			break
		}
		item, err := c.sessionCapture(ctx, entries[index], params.MaxBytesPerSource)
		if err != nil {
			return nil, err
		}
		cost := len(item.SourceID) + len(item.AppID) + len(item.Domain) + len(item.CapturedAt) + len(item.LastSeenAt) + len(item.Content) + mcpSessionItemOverheadEst
		if len(response.Captures) > 0 && used+cost > localapi.MaxRPCSnapshotResponseBytes-1024 {
			response.HasMore = true
			break
		}
		response.Captures = append(response.Captures, item)
		used += cost
		// The first capture must always fit, even when JSON escaping expands
		// control characters; shorten it instead of failing the whole page.
		for len(response.Captures) == 1 && !mcpParityFits(response, localapi.MaxRPCSnapshotResponseBytes) && len(item.Content) > 4 {
			cut := len(item.Content) / 2
			for cut > 0 && !utf8.RuneStart(item.Content[cut]) {
				cut--
			}
			item.Content = item.Content[:cut]
			item.NextOffsetBytes = cut
			item.Complete = false
			response.Captures[0] = item
		}
	}
	if response.HasMore && len(response.Captures) > 0 {
		response.Next = response.Captures[len(response.Captures)-1].SourceID
	}
	for len(response.Captures) > 1 && !mcpParityFits(response, localapi.MaxRPCSnapshotResponseBytes) {
		response.Captures = response.Captures[:len(response.Captures)-1]
		response.HasMore = true
		response.Next = response.Captures[len(response.Captures)-1].SourceID
	}
	return c.publishMCPParity(ctx, epoch, response, localapi.MaxRPCSnapshotResponseBytes)
}

// sessionCapture hydrates one capture with the same UTF-8 paging convention as
// get_snapshots: Content starts at offset 0 and NextOffsetBytes continues.
func (c *Client) sessionCapture(ctx context.Context, entry mcpSessionEntry, chunk int) (mcpSessionCapture, error) {
	reader, ok := c.data.(storage.ObservationReader)
	if !ok {
		return mcpSessionCapture{}, ErrNotConfigured
	}
	var captured time.Time
	var found bool
	var text, appID, domain string
	var partial bool
	if timed, ok := c.data.(storage.ObservationTimeReader); ok {
		evidence, at, ok, err := storage.ReadSourceEvidenceWithCaptureTime(ctx, timed, entry.id)
		if err != nil {
			return mcpSessionCapture{}, err
		}
		found, captured = ok, at
		text, appID, domain, partial = evidence.Text(), evidence.AppID(), evidence.Domain(), evidence.Truncated()
	} else {
		evidence, ok, err := storage.ReadSourceEvidence(ctx, reader, entry.id)
		if err != nil {
			return mcpSessionCapture{}, err
		}
		found = ok
		text, appID, domain, partial = evidence.Text(), evidence.AppID(), evidence.Domain(), evidence.Truncated()
	}
	// A capture deleted between listing and reading means memory is changing.
	if !found || strings.EqualFold(appID, appidentity.BundleID) || !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 {
		return mcpSessionCapture{}, ErrMemoryChanging
	}
	end := min(len(text), chunk)
	for end > 0 && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	item := mcpSessionCapture{SourceID: entry.id, AppID: appID, Domain: domain, LastSeenAt: entry.seen.UTC().Format(time.RFC3339Nano), Content: text[:end], NextOffsetBytes: end, Complete: end == len(text), PartialCapture: partial}
	if !captured.IsZero() {
		item.CapturedAt = captured.UTC().Format(time.RFC3339Nano)
	}
	return item, nil
}
