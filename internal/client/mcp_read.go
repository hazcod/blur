package client

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/tasks"
)

const (
	maxMCPReadResults = 8
	// Local hydration and external response limits serve different purposes.
	maxMCPReadSourceBytes    = MaxSearchBytes
	maxMCPReadTextBytes      = 2048
	maxMCPReadOutputBytes    = 16 << 10
	maxMCPReadSnapshotIDs    = 3
	maxMCPReadSnapshotChunk  = 4096
	maxMCPActivityDays       = 7
	maxMCPActivityRows       = 8
	maxMCPWikiResults        = 8
	maxMCPTaskResults        = 8
	maxMCPWikiDetails        = 4
	maxMCPWikiOffset         = 256
	maxMCPChronicleResults   = 8
	maxMCPWorkingMemoryHours = 6
	maxMCPWorkingMemoryItems = 8
	maxMCPWorkingExcerpt     = 1600
)

type mcpSearchParams struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type mcpSearchResponse struct {
	Results         []SearchHit `json:"results"`
	Truncated       bool        `json:"truncated"`
	IndexingPending bool        `json:"indexing_pending,omitempty"`
	CoverageLimited bool        `json:"coverage_limited,omitempty"`
}

type mcpSnapshotParams struct {
	SourceIDs         []string       `json:"source_ids"`
	Offsets           map[string]int `json:"offsets,omitempty"`
	MaxBytesPerSource int            `json:"max_bytes_per_source,omitempty"`
}

type mcpSnapshotItem struct {
	SourceID           string `json:"source_id"`
	AppID              string `json:"app_id"`
	Domain             string `json:"domain,omitempty"`
	CapturedAt         string `json:"captured_at,omitempty"`
	Content            string `json:"content"`
	OffsetBytes        int    `json:"offset_bytes"`
	NextOffsetBytes    int    `json:"next_offset_bytes"`
	Complete           bool   `json:"complete"`
	PartialCapture     bool   `json:"partial_capture,omitempty"`
	DocumentID         string `json:"document_id,omitempty"`
	PartIndex          *int64 `json:"part_index,omitempty"`
	PartCount          *int64 `json:"part_count,omitempty"`
	DocumentOffset     *int64 `json:"document_offset_bytes,omitempty"`
	DocumentTotalBytes *int64 `json:"document_total_bytes,omitempty"`
}

type mcpSnapshotResponse struct {
	Snapshots []mcpSnapshotItem `json:"snapshots"`
	Truncated bool              `json:"truncated"`
}

type mcpChronicleParams struct {
	HourID string `json:"hour_id,omitempty"`
	Level  string `json:"level,omitempty"`
	Period string `json:"period,omitempty"`
}

// maxMCPRollupSourceIDs bounds the source IDs one day or week response lists.
const maxMCPRollupSourceIDs = 64

type mcpRollupResponse struct {
	Found              bool      `json:"found"`
	Level              string    `json:"level,omitempty"`
	Period             string    `json:"period,omitempty"`
	Start              time.Time `json:"start,omitempty"`
	End                time.Time `json:"end,omitempty"`
	Text               string    `json:"text,omitempty"`
	ChildIDs           []string  `json:"child_ids,omitempty"`
	SourceIDs          []string  `json:"source_ids,omitempty"`
	SourceIDsTruncated bool      `json:"source_ids_truncated,omitempty"`
	GeneratedAt        time.Time `json:"generated_at,omitempty"`
	Dirty              bool      `json:"dirty,omitempty"`
}

type mcpChronicleResponse struct {
	Found     bool                `json:"found"`
	HourID    string              `json:"hour_id,omitempty"`
	Start     time.Time           `json:"start,omitempty"`
	End       time.Time           `json:"end,omitempty"`
	Summary   string              `json:"summary,omitempty"`
	SourceIDs []string            `json:"source_ids,omitempty"`
	FollowUp  *chronicle.FollowUp `json:"follow_up,omitempty"`
}

type mcpChronicleRangeParams struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	After string    `json:"after,omitempty"`
	Limit int       `json:"limit"`
}

type mcpChronicleRangeItem struct {
	HourID    string              `json:"hour_id"`
	Start     time.Time           `json:"start"`
	End       time.Time           `json:"end"`
	Summary   string              `json:"summary"`
	SourceIDs []string            `json:"source_ids"`
	FollowUp  *chronicle.FollowUp `json:"follow_up,omitempty"`
}

type mcpChronicleRangeResponse struct {
	Items     []mcpChronicleRangeItem `json:"items"`
	Next      string                  `json:"next,omitempty"`
	Truncated bool                    `json:"truncated"`
}

type mcpWorkingMemoryParams struct {
	Hours int `json:"hours"`
	Limit int `json:"limit"`
}

type mcpWorkingMemoryItem struct {
	SourceID   string    `json:"source_id"`
	AppID      string    `json:"app_id"`
	Domain     string    `json:"domain,omitempty"`
	CapturedAt time.Time `json:"captured_at"`
	Content    string    `json:"content"`
	Truncated  bool      `json:"truncated"`
}

type mcpWorkingMemoryResponse struct {
	Start           time.Time              `json:"start"`
	End             time.Time              `json:"end"`
	Items           []mcpWorkingMemoryItem `json:"items"`
	Truncated       bool                   `json:"truncated"`
	IndexingPending bool                   `json:"indexing_pending,omitempty"`
}

type mcpActivityParams struct {
	DaysBack int `json:"days_back,omitempty"`
	Limit    int `json:"limit,omitempty"`
}

type mcpActivitySegment struct {
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Duration  int64     `json:"duration_ns"`
	AppID     string    `json:"app_id,omitempty"`
	Domain    string    `json:"domain,omitempty"`
	Project   string    `json:"project,omitempty"`
	Topic     string    `json:"topic,omitempty"`
	SourceIDs []string  `json:"source_ids"`
}

type mcpActivityResponse struct {
	Segments  []mcpActivitySegment `json:"segments"`
	Truncated bool                 `json:"truncated"`
}

type mcpWikiParams struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type mcpWikiFact struct {
	Text             string             `json:"text"`
	State            knowledge.State    `json:"state"`
	Sources          []knowledge.Source `json:"sources,omitempty"`
	SourcesTruncated bool               `json:"sources_truncated,omitempty"`
}

type mcpWikiLoop struct {
	Text             string             `json:"text"`
	State            knowledge.State    `json:"state"`
	Sources          []knowledge.Source `json:"sources,omitempty"`
	SourcesTruncated bool               `json:"sources_truncated,omitempty"`
}

type mcpWikiEntity struct {
	ID               string          `json:"id"`
	Kind             knowledge.Kind  `json:"kind"`
	Name             string          `json:"name"`
	Aliases          []string        `json:"aliases,omitempty"`
	State            knowledge.State `json:"state"`
	Facts            []mcpWikiFact   `json:"facts,omitempty"`
	OpenLoops        []mcpWikiLoop   `json:"open_loops,omitempty"`
	DetailsTruncated bool            `json:"details_truncated,omitempty"`
}

type mcpWikiResponse struct {
	Entities  []mcpWikiEntity `json:"entities"`
	Truncated bool            `json:"truncated"`
}

type mcpWikiPageParams struct {
	ID     string `json:"id"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

type mcpWikiDetail struct {
	Type             string             `json:"type"`
	ID               string             `json:"id"`
	Text             string             `json:"text"`
	TextTruncated    bool               `json:"text_truncated,omitempty"`
	State            knowledge.State    `json:"state"`
	Sources          []knowledge.Source `json:"sources"`
	SourcesTruncated bool               `json:"sources_truncated,omitempty"`
}

type mcpWikiPageEntity struct {
	ID               string          `json:"id"`
	Kind             knowledge.Kind  `json:"kind"`
	Name             string          `json:"name"`
	Aliases          []string        `json:"aliases,omitempty"`
	AliasesTruncated bool            `json:"aliases_truncated,omitempty"`
	State            knowledge.State `json:"state"`
	RelatedIDs       []string        `json:"related_ids,omitempty"`
	RelationsOmitted bool            `json:"relations_omitted,omitempty"`
}

type mcpWikiPageResponse struct {
	Found      bool               `json:"found"`
	Entity     *mcpWikiPageEntity `json:"entity,omitempty"`
	Details    []mcpWikiDetail    `json:"details"`
	Offset     int                `json:"offset"`
	NextOffset int                `json:"next_offset,omitempty"`
	HasMore    bool               `json:"has_more"`
}

type mcpTaskParams struct {
	State string `json:"state,omitempty"`
	Limit int    `json:"limit,omitempty"`
	After string `json:"after,omitempty"`
}

type mcpTaskTransition struct {
	State    tasks.State `json:"state"`
	At       time.Time   `json:"at"`
	Revision uint64      `json:"revision"`
}

type mcpTaskReminder struct {
	DueAt   time.Time        `json:"due_at"`
	Repeat  tasks.Recurrence `json:"repeat,omitempty"`
	Hour    int              `json:"hour,omitempty"`
	Minute  int              `json:"minute,omitempty"`
	Weekday time.Weekday     `json:"weekday,omitempty"`
}

type mcpTaskItem struct {
	ID               string              `json:"id"`
	Revision         uint64              `json:"revision"`
	Title            string              `json:"title"`
	State            tasks.State         `json:"state"`
	CreatedAt        time.Time           `json:"created_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
	SourceIDs        []string            `json:"source_ids,omitempty"`
	UserAuthored     bool                `json:"user_authored,omitempty"`
	Reminder         *mcpTaskReminder    `json:"reminder,omitempty"`
	DeliveryState    tasks.DeliveryState `json:"delivery_state,omitempty"`
	History          []mcpTaskTransition `json:"history,omitempty"`
	HistoryOmitted   uint64              `json:"history_omitted,omitempty"`
	HistoryTruncated bool                `json:"history_truncated,omitempty"`
}

type mcpTaskResponse struct {
	Items   []mcpTaskItem `json:"items"`
	Next    string        `json:"next,omitempty"`
	HasMore bool          `json:"has_more"`
}

// HandleMCPRead adapts bounded MCP reads to the already-open encrypted store.
// It intentionally supports no writes and never logs memory.
func (c *Client) HandleMCPRead(ctx context.Context, method string, raw json.RawMessage) (json.RawMessage, error) {
	if c == nil || c.data == nil || ctx == nil {
		return nil, ErrNotConfigured
	}
	if method != localapi.RPCSearchMemory && method != localapi.RPCGetSnapshots && method != localapi.RPCGetChronicle && method != localapi.RPCListChronicles && method != localapi.RPCGetWorkingMemory && method != localapi.RPCGetRecentActivity && method != localapi.RPCSearchWiki && method != localapi.RPCGetWikiPage && method != localapi.RPCListTasks {
		return nil, ErrInvalidRequest
	}
	var searchParams mcpSearchParams
	var snapshotParams mcpSnapshotParams
	var chronicleParams mcpChronicleParams
	var chronicleRangeParams mcpChronicleRangeParams
	var workingMemoryParams mcpWorkingMemoryParams
	var activityParams mcpActivityParams
	var wikiParams mcpWikiParams
	var wikiPageParams mcpWikiPageParams
	var taskParams mcpTaskParams
	var err error
	switch method {
	case localapi.RPCSearchMemory:
		searchParams, err = decodeMCPReadParams(raw)
		if err != nil || searchParams.Limit < 1 || searchParams.Limit > maxMCPReadResults || len(searchParams.Query) > MaxQueryBytes || !utf8.ValidString(searchParams.Query) || strings.IndexByte(searchParams.Query, 0) >= 0 || strings.TrimSpace(searchParams.Query) == "" {
			return nil, ErrInvalidRequest
		}
	case localapi.RPCGetSnapshots:
		snapshotParams, err = decodeMCPSnapshotParams(raw)
		if err != nil {
			return nil, err
		}
	case localapi.RPCGetChronicle:
		chronicleParams, err = decodeMCPChronicleParams(raw)
		if err != nil {
			return nil, err
		}
	case localapi.RPCListChronicles:
		chronicleRangeParams, err = decodeMCPChronicleRangeParams(raw)
		if err != nil {
			return nil, err
		}
	case localapi.RPCGetWorkingMemory:
		workingMemoryParams, err = decodeMCPWorkingMemoryParams(raw)
		if err != nil {
			return nil, err
		}
	case localapi.RPCGetRecentActivity:
		activityParams, err = decodeMCPActivityParams(raw)
		if err != nil {
			return nil, err
		}
	case localapi.RPCSearchWiki:
		wikiParams, err = decodeMCPWikiParams(raw)
		if err != nil {
			return nil, err
		}
	case localapi.RPCGetWikiPage:
		wikiPageParams, err = decodeMCPWikiPageParams(raw)
		if err != nil {
			return nil, err
		}
	case localapi.RPCListTasks:
		taskParams, err = decodeMCPTaskParams(raw)
		if err != nil {
			return nil, err
		}
	default:
		return nil, ErrInvalidRequest
	}
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return nil, ErrMemoryChanging
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	operation, done, err := c.beginOperation(ctx, noteOperationTimeout)
	if err != nil {
		if canceled := ctx.Err(); canceled != nil {
			return nil, canceled
		}
		return nil, err
	}
	defer done()
	ctx = operation
	switch method {
	case localapi.RPCGetSnapshots:
		return c.readMCPSnapshots(ctx, epoch, snapshotParams)
	case localapi.RPCGetChronicle:
		return c.readMCPChronicle(ctx, epoch, chronicleParams)
	case localapi.RPCListChronicles:
		return c.readMCPChronicleRange(ctx, epoch, chronicleRangeParams)
	case localapi.RPCGetWorkingMemory:
		return c.readMCPWorkingMemory(ctx, epoch, workingMemoryParams)
	case localapi.RPCGetRecentActivity:
		return c.readMCPRecentActivity(ctx, epoch, activityParams)
	case localapi.RPCSearchWiki:
		return c.searchMCPWiki(ctx, epoch, wikiParams)
	case localapi.RPCGetWikiPage:
		return c.getMCPWikiPage(ctx, epoch, wikiPageParams)
	case localapi.RPCListTasks:
		return c.listMCPTasks(ctx, epoch, taskParams)
	}
	page, err := c.memorySearchPage(ctx, searchParams.Query, searchParams.Limit, true)
	if err != nil {
		return nil, err
	}
	response := mcpSearchResponse{Results: make([]SearchHit, 0, len(page.Hits)), Truncated: page.HasMore, IndexingPending: page.IndexingPending, CoverageLimited: page.CoverageLimited}
	var validation assistant.Result
	used := 0
	for _, hit := range page.Hits {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cost := len(hit.Text) + len(hit.AppID) + len(hit.Domain)
		if cost < 1 || cost > maxMCPReadSourceBytes-used {
			response.Truncated = true
			break
		}
		used += cost
		if hit.Task != nil {
			validation.Targets = append(validation.Targets, assistant.TargetRef{Kind: "task", ID: hit.Task.ID, Revision: hit.Task.Revision, SourceID: hit.SourceID})
			validation.Sources = append(validation.Sources, assistant.Evidence{SourceID: hit.SourceID, Text: hit.Text})
		}
		text := hit.Text
		if hit.MatchEnd > 0 {
			if hit.MatchStart < 0 || hit.MatchEnd <= hit.MatchStart || hit.MatchEnd > len(text) || !utf8.ValidString(text[hit.MatchStart:hit.MatchEnd]) {
				return nil, ErrInvalidRequest
			}
			text = text[hit.MatchStart:hit.MatchEnd]
		}
		if len(text) > maxMCPReadTextBytes {
			start, end := search.MatchPassage(text, page.terms, maxMCPReadTextBytes)
			if end > start {
				text = text[start:end]
			}
		}
		text, clipped := truncateMCPReadText(text)
		item := SearchHit{TimeUnknown: hit.TimeUnknown, CapturedAt: hit.CapturedAt, SourceID: hit.SourceID, AppID: hit.AppID, Domain: hit.Domain, Text: text, Partial: hit.Partial, Truncated: clipped || len(text) != len(hit.Text) || hit.Partial}
		response.Truncated = response.Truncated || item.Truncated
		response.Results = append(response.Results, item)
		encoded, err := json.Marshal(response)
		if err != nil {
			return nil, ErrInvalidRequest
		}
		if len(encoded) > maxMCPReadOutputBytes {
			response.Results = response.Results[:len(response.Results)-1]
			response.Truncated = true
			break
		}
	}
	if err := c.validateTaskTargets(ctx, validation); err != nil {
		return nil, err
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var encoded []byte
	if !c.PublishMemoryRead(epoch, func() {
		encoded, err = json.Marshal(response)
	}) {
		return nil, ErrMemoryChanging
	}
	if err != nil || len(encoded) > maxMCPReadOutputBytes {
		clear(encoded)
		return nil, ErrInvalidRequest
	}
	return encoded, nil
}

func decodeMCPChronicleParams(raw []byte) (mcpChronicleParams, error) {
	if len(raw) == 0 || len(raw) > 2<<10 || !json.Valid(raw) {
		return mcpChronicleParams{}, ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var params mcpChronicleParams
	if decoder.Decode(&params) != nil {
		return mcpChronicleParams{}, ErrInvalidRequest
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return mcpChronicleParams{}, ErrInvalidRequest
	}
	switch params.Level {
	case "", "hour":
		// An hourly read needs its hour ID and takes no period.
		if !validChronicleID(params.HourID) || params.Period != "" {
			return mcpChronicleParams{}, ErrInvalidRequest
		}
		params.Level = ""
	case chronicle.RollupLevelDay:
		if params.HourID != "" || !chronicle.ValidDayKey(params.Period) {
			return mcpChronicleParams{}, ErrInvalidRequest
		}
	case chronicle.RollupLevelWeek:
		if params.HourID != "" || !chronicle.ValidWeekKey(params.Period) {
			return mcpChronicleParams{}, ErrInvalidRequest
		}
	default:
		return mcpChronicleParams{}, ErrInvalidRequest
	}
	return params, nil
}

// readMCPRollup serves one stored day or ISO-week recap. A placeholder, or a
// rollup whose text was discarded because a source was erased, has no text to
// return and is reported as not found; it is never returned without sources.
func (c *Client) readMCPRollup(ctx context.Context, epoch uint64, params mcpChronicleParams) (json.RawMessage, error) {
	if c.chronicles == nil {
		return nil, ErrNotConfigured
	}
	id := params.Level + ":" + params.Period
	rollup, found, err := c.chronicles.GetChronicleRollup(ctx, id)
	if err != nil {
		return nil, err
	}
	response := mcpRollupResponse{}
	if found && rollup.Text != "" {
		if rollup.ID != id || rollup.Level != params.Level || rollup.PeriodKey != params.Period || chronicle.ValidateRollup(rollup) != nil || len(rollup.SourceIDs) == 0 {
			return nil, ErrInvalidRequest
		}
		response = mcpRollupResponse{Found: true, Level: rollup.Level, Period: rollup.PeriodKey, Start: rollup.Start.UTC(), End: rollup.End.UTC(), Text: rollup.Text, ChildIDs: append([]string(nil), rollup.ChildIDs...), GeneratedAt: rollup.GeneratedAt.UTC(), Dirty: rollup.Dirty}
		response.SourceIDs = append([]string(nil), rollup.SourceIDs...)
		if len(response.SourceIDs) > maxMCPRollupSourceIDs {
			response.SourceIDs, response.SourceIDsTruncated = response.SourceIDs[:maxMCPRollupSourceIDs], true
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var encoded []byte
	var marshalErr error
	if !c.PublishMemoryRead(epoch, func() { encoded, marshalErr = json.Marshal(response) }) {
		return nil, ErrMemoryChanging
	}
	if marshalErr != nil || len(encoded) > maxMCPReadOutputBytes {
		clear(encoded)
		return nil, ErrInvalidRequest
	}
	return encoded, nil
}

func (c *Client) readMCPChronicle(ctx context.Context, epoch uint64, params mcpChronicleParams) (json.RawMessage, error) {
	if params.Level != "" {
		return c.readMCPRollup(ctx, epoch, params)
	}
	if c.chronicles == nil {
		return nil, ErrNotConfigured
	}
	summary, found, err := c.chronicles.GetChronicle(ctx, params.HourID)
	if err != nil {
		return nil, err
	}
	response := mcpChronicleResponse{Found: found}
	if found {
		if !validMCPChronicleSummary(summary, params.HourID) {
			return nil, ErrInvalidRequest
		}
		response.HourID = summary.HourID
		response.Start = summary.Start.UTC()
		response.End = summary.End.UTC()
		response.Summary = summary.Text
		response.SourceIDs = append([]string(nil), summary.SourceIDs...)
		if summary.FollowUp != nil {
			followUp := *summary.FollowUp
			response.FollowUp = &followUp
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var encoded []byte
	var marshalErr error
	if !c.PublishMemoryRead(epoch, func() { encoded, marshalErr = json.Marshal(response) }) {
		return nil, ErrMemoryChanging
	}
	if marshalErr != nil || len(encoded) > maxMCPReadOutputBytes {
		clear(encoded)
		return nil, ErrInvalidRequest
	}
	return encoded, nil
}

func decodeMCPChronicleRangeParams(raw []byte) (mcpChronicleRangeParams, error) {
	if len(raw) == 0 || len(raw) > 2<<10 || !json.Valid(raw) {
		return mcpChronicleRangeParams{}, ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var params mcpChronicleRangeParams
	if decoder.Decode(&params) != nil {
		return mcpChronicleRangeParams{}, ErrInvalidRequest
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) || params.Start.IsZero() || params.End.IsZero() || !params.Start.Before(params.End) || params.End.Sub(params.Start) > 32*24*time.Hour || params.Limit < 1 || params.Limit > maxMCPChronicleResults || params.After != "" && !validChronicleID(params.After) {
		return mcpChronicleRangeParams{}, ErrInvalidRequest
	}
	params.Start, params.End = params.Start.UTC(), params.End.UTC()
	return params, nil
}

func (c *Client) readMCPChronicleRange(ctx context.Context, epoch uint64, params mcpChronicleRangeParams) (json.RawMessage, error) {
	if c.chronicles == nil {
		return nil, ErrNotConfigured
	}
	rows, next, err := c.chronicles.ListChronicles(ctx, params.Start, params.End, params.After, params.Limit)
	if err != nil || len(rows) > params.Limit || next != "" && !validChronicleID(next) {
		if err != nil {
			return nil, err
		}
		return nil, ErrInvalidRequest
	}
	response := mcpChronicleRangeResponse{Items: make([]mcpChronicleRangeItem, 0, len(rows)), Next: next, Truncated: next != ""}
	previous := params.After
	for _, row := range rows {
		if !validMCPChronicleSummary(row, row.HourID) || row.HourID <= previous || row.Start.Before(params.Start) || !row.Start.Before(params.End) {
			return nil, ErrInvalidRequest
		}
		previous = row.HourID
		item := mcpChronicleRangeItem{HourID: row.HourID, Start: row.Start.UTC(), End: row.End.UTC(), Summary: row.Text, SourceIDs: append([]string(nil), row.SourceIDs...)}
		if row.FollowUp != nil {
			followUp := *row.FollowUp
			item.FollowUp = &followUp
		}
		response.Items = append(response.Items, item)
	}
	// Keep complete hours only. If a byte-heavy page does not fit the local RPC
	// frame, resume from the last included ID so no recap is skipped.
	for {
		encoded, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return nil, ErrInvalidRequest
		}
		if len(encoded) <= localapi.MaxRPCSearchResponseBytes {
			clear(encoded)
			break
		}
		clear(encoded)
		if len(response.Items) <= 1 {
			return nil, ErrInvalidRequest
		}
		response.Next = response.Items[len(response.Items)-2].HourID
		response.Truncated = true
		response.Items = response.Items[:len(response.Items)-1]
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var encoded []byte
	var marshalErr error
	if !c.PublishMemoryRead(epoch, func() { encoded, marshalErr = json.Marshal(response) }) {
		return nil, ErrMemoryChanging
	}
	if marshalErr != nil || len(encoded) > localapi.MaxRPCSearchResponseBytes {
		clear(encoded)
		return nil, ErrInvalidRequest
	}
	return encoded, nil
}

func decodeMCPWorkingMemoryParams(raw []byte) (mcpWorkingMemoryParams, error) {
	if len(raw) == 0 || len(raw) > 2<<10 || !json.Valid(raw) {
		return mcpWorkingMemoryParams{}, ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var params mcpWorkingMemoryParams
	if decoder.Decode(&params) != nil {
		return mcpWorkingMemoryParams{}, ErrInvalidRequest
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) || params.Hours < 1 || params.Hours > maxMCPWorkingMemoryHours || params.Limit < 1 || params.Limit > maxMCPWorkingMemoryItems {
		return mcpWorkingMemoryParams{}, ErrInvalidRequest
	}
	return params, nil
}

func (c *Client) readMCPWorkingMemory(ctx context.Context, epoch uint64, params mcpWorkingMemoryParams) (json.RawMessage, error) {
	reader, ok := c.data.(ObservationWindowReader)
	if !ok {
		return nil, ErrNotConfigured
	}
	now := c.askClock().UTC()
	start, end := now.Add(-time.Duration(params.Hours)*time.Hour), now.Add(time.Nanosecond)
	page, err := readSourceWindow(ctx, reader, storage.ObservationWindowRequest{
		Start: start, End: end, ExcludeAppIDs: []string{appidentity.BundleID},
		Limit: params.Limit, MaxBytes: storage.MaxObservationPageBytes,
	})
	if err != nil {
		return nil, err
	}
	if len(page.Sources) != len(page.CapturedAt) || len(page.Sources) > params.Limit {
		return nil, ErrInvalidRequest
	}
	response := mcpWorkingMemoryResponse{Start: start, End: end, Items: make([]mcpWorkingMemoryItem, 0, len(page.Sources)), Truncated: page.HasMore, IndexingPending: page.Pending}
	previous := end
	for i, source := range page.Sources {
		at := page.CapturedAt[i]
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if source.Validate() != nil || !validKnowledgeID(source.SourceID()) || at.Before(start) || !at.Before(end) || at.After(previous) || strings.EqualFold(source.AppID(), appidentity.BundleID) {
			return nil, ErrInvalidRequest
		}
		previous = at
		content, clipped := workingMemoryExcerpt(source.Text())
		response.Truncated = response.Truncated || clipped || source.Truncated()
		response.Items = append(response.Items, mcpWorkingMemoryItem{
			SourceID: source.SourceID(), AppID: source.AppID(), Domain: source.Domain(),
			CapturedAt: at.UTC(), Content: content, Truncated: clipped || source.Truncated(),
		})
	}
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded) > maxMCPReadOutputBytes {
		clear(encoded)
		return nil, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		clear(encoded)
		return nil, err
	}
	var published []byte
	var marshalErr error
	if !c.PublishMemoryRead(epoch, func() { published, marshalErr = json.Marshal(response) }) {
		clear(encoded)
		return nil, ErrMemoryChanging
	}
	clear(encoded)
	if marshalErr != nil || len(published) > maxMCPReadOutputBytes {
		clear(published)
		return nil, ErrInvalidRequest
	}
	return published, nil
}

func workingMemoryExcerpt(text string) (string, bool) {
	if len(text) <= maxMCPWorkingExcerpt {
		return text, false
	}
	const marker = "\n[…middle omitted…]\n"
	contentBudget := maxMCPWorkingExcerpt - len(marker)
	headBudget := contentBudget / 2
	tailBudget := contentBudget - headBudget
	head := truncateUTF8Bytes(text, headBudget)
	tailStart := len(text) - tailBudget
	for tailStart < len(text) && !utf8.RuneStart(text[tailStart]) {
		tailStart++
	}
	return head + marker + text[tailStart:], true
}

func validMCPChronicleSummary(summary chronicle.Summary, hourID string) bool {
	if summary.HourID != hourID || len(summary.Text) == 0 || len(summary.Text) > chronicle.MaxSummaryBytes || !utf8.ValidString(summary.Text) || strings.IndexByte(summary.Text, 0) >= 0 || len(summary.SourceIDs) == 0 || len(summary.SourceIDs) > chronicle.MaxEvidence {
		return false
	}
	id, err := (chronicle.Hour{Start: summary.Start, End: summary.End}).ID()
	if err != nil || id != hourID {
		return false
	}
	last := ""
	for _, sourceID := range summary.SourceIDs {
		if len(sourceID) != 64 || strings.ToLower(sourceID) != sourceID || sourceID <= last {
			return false
		}
		if _, err := hex.DecodeString(sourceID); err != nil {
			return false
		}
		last = sourceID
	}
	if followUp := summary.FollowUp; followUp != nil {
		if len(followUp.Text) == 0 || len(followUp.Text) > chronicle.MaxFollowUpBytes || !utf8.ValidString(followUp.Text) || strings.IndexByte(followUp.Text, 0) >= 0 {
			return false
		}
		index := sort.SearchStrings(summary.SourceIDs, followUp.SourceID)
		if index == len(summary.SourceIDs) || summary.SourceIDs[index] != followUp.SourceID {
			return false
		}
	}
	return true
}

func decodeMCPActivityParams(raw []byte) (mcpActivityParams, error) {
	if len(raw) == 0 || len(raw) > 2<<10 || !json.Valid(raw) {
		return mcpActivityParams{}, ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var params mcpActivityParams
	if decoder.Decode(&params) != nil {
		return mcpActivityParams{}, ErrInvalidRequest
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) || params.DaysBack < 0 || params.DaysBack > maxMCPActivityDays || params.Limit < 0 || params.Limit > maxMCPActivityRows {
		return mcpActivityParams{}, ErrInvalidRequest
	}
	if params.DaysBack == 0 {
		params.DaysBack = 1
	}
	if params.Limit == 0 {
		params.Limit = 5
	}
	return params, nil
}

func (c *Client) readMCPRecentActivity(ctx context.Context, epoch uint64, params mcpActivityParams) (json.RawMessage, error) {
	reader, ok := c.data.(ActivityData)
	if !ok {
		return nil, ErrNotConfigured
	}
	zone, err := loadZone(localZoneName())
	if err != nil {
		return nil, ErrInvalidRequest
	}
	now := c.askClock().In(zone)
	end := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, zone)
	start := time.Date(now.Year(), now.Month(), now.Day()-params.DaysBack+1, 0, 0, 0, 0, zone)
	var dates []string
	for day := start.UTC().Truncate(24 * time.Hour); day.Before(end.UTC()); day = day.Add(24 * time.Hour) {
		dates = append(dates, day.Format("2006-01-02"))
	}
	if len(dates) == 0 || len(dates) > maxMCPActivityDays+2 {
		return nil, ErrInvalidRequest
	}
	rows, err := reader.ListActivitySegmentsForDates(ctx, dates, MaxActivityReportSegments)
	if err != nil {
		return nil, err
	}
	if len(rows) > MaxActivityReportSegments {
		return nil, ErrActivityLimit
	}
	filtered := make([]activity.Segment, 0, min(len(rows), MaxActivityReportSegments))
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if row.Validate() != nil {
			return nil, ErrInvalidRequest
		}
		if !row.WallEnd.After(start) || !row.WallStart.Before(end) {
			continue
		}
		if row.WallStart.Before(start) {
			row.WallStart = start
		}
		if row.WallEnd.After(end) {
			row.WallEnd = end
		}
		row.Duration = int64(row.WallEnd.Sub(row.WallStart))
		if row.Duration <= 0 {
			continue
		}
		for _, id := range row.SourceIDs {
			if len(id) != 64 || strings.ToLower(id) != id {
				return nil, ErrInvalidRequest
			}
			if _, err := hex.DecodeString(id); err != nil {
				return nil, ErrInvalidRequest
			}
		}
		filtered = append(filtered, row)
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].WallStart.Equal(filtered[j].WallStart) {
			return filtered[i].ID > filtered[j].ID
		}
		return filtered[i].WallStart.After(filtered[j].WallStart)
	})
	response := mcpActivityResponse{Segments: make([]mcpActivitySegment, 0, min(len(filtered), params.Limit)), Truncated: len(filtered) > params.Limit}
	for _, row := range filtered[:min(len(filtered), params.Limit)] {
		response.Segments = append(response.Segments, mcpActivitySegment{Start: row.WallStart.UTC(), End: row.WallEnd.UTC(), Duration: row.Duration, AppID: row.AppID, Domain: row.Domain, Project: row.Project, Topic: row.Topic, SourceIDs: append([]string(nil), row.SourceIDs...)})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var encoded []byte
	var marshalErr error
	if !c.PublishMemoryRead(epoch, func() { encoded, marshalErr = json.Marshal(response) }) {
		return nil, ErrMemoryChanging
	}
	if marshalErr != nil || len(encoded) > localapi.MaxRPCSnapshotResponseBytes {
		clear(encoded)
		return nil, ErrInvalidRequest
	}
	return encoded, nil
}

func decodeMCPWikiParams(raw []byte) (mcpWikiParams, error) {
	if len(raw) == 0 || len(raw) > 2<<10 || !json.Valid(raw) {
		return mcpWikiParams{}, ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var params mcpWikiParams
	if decoder.Decode(&params) != nil {
		return mcpWikiParams{}, ErrInvalidRequest
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) || len(params.Query) == 0 || len(params.Query) > MaxQueryBytes || !utf8.ValidString(params.Query) || strings.IndexByte(params.Query, 0) >= 0 || strings.TrimSpace(params.Query) == "" || params.Limit < 0 || params.Limit > maxMCPWikiResults {
		return mcpWikiParams{}, ErrInvalidRequest
	}
	if params.Limit == 0 {
		params.Limit = 5
	}
	return params, nil
}

func (c *Client) searchMCPWiki(ctx context.Context, epoch uint64, params mcpWikiParams) (json.RawMessage, error) {
	page, err := c.libraryDirectoryPage(ctx, params.Query, "", 0)
	if err != nil {
		return nil, err
	}
	response := mcpWikiResponse{Entities: make([]mcpWikiEntity, 0, min(params.Limit, len(page.Entities))), Truncated: page.total > params.Limit || page.HasMore}
	for _, entity := range page.Entities[:min(params.Limit, len(page.Entities))] {
		if entity.Validate() != nil {
			return nil, ErrInvalidRequest
		}
		item := boundMCPWikiEntity(entity)
		response.Truncated = response.Truncated || item.DetailsTruncated
		response.Entities = append(response.Entities, item)
		encoded, err := json.Marshal(response)
		if err != nil {
			return nil, ErrInvalidRequest
		}
		if len(encoded) > maxMCPReadOutputBytes {
			response.Entities = response.Entities[:len(response.Entities)-1]
			response.Truncated = true
			break
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var encoded []byte
	var marshalErr error
	if !c.PublishMemoryRead(epoch, func() { encoded, marshalErr = json.Marshal(response) }) {
		return nil, ErrMemoryChanging
	}
	if marshalErr != nil || len(encoded) > maxMCPReadOutputBytes {
		clear(encoded)
		return nil, ErrInvalidRequest
	}
	return encoded, nil
}

func boundMCPWikiEntity(entity knowledge.Entity) mcpWikiEntity {
	item := mcpWikiEntity{ID: entity.ID, Kind: entity.Kind, Name: entity.Name, State: entity.State}
	if len(entity.Aliases) > 8 {
		item.DetailsTruncated = true
		entity.Aliases = entity.Aliases[:8]
	}
	item.Aliases = append([]string(nil), entity.Aliases...)
	if len(entity.Facts) > 6 {
		item.DetailsTruncated = true
		entity.Facts = entity.Facts[:6]
	}
	for _, fact := range entity.Facts {
		text := truncateUTF8Bytes(fact.Text, 1200)
		sources := boundMCPWikiSources(fact.Sources)
		item.DetailsTruncated = item.DetailsTruncated || text != fact.Text || sources.truncated
		item.Facts = append(item.Facts, mcpWikiFact{Text: text, State: fact.State, Sources: sources.items, SourcesTruncated: sources.truncated})
	}
	if len(entity.OpenLoops) > 6 {
		item.DetailsTruncated = true
		entity.OpenLoops = entity.OpenLoops[:6]
	}
	for _, loop := range entity.OpenLoops {
		text := truncateUTF8Bytes(loop.Text, 1200)
		sources := boundMCPWikiSources(loop.Sources)
		item.DetailsTruncated = item.DetailsTruncated || text != loop.Text || sources.truncated
		item.OpenLoops = append(item.OpenLoops, mcpWikiLoop{Text: text, State: loop.State, Sources: sources.items, SourcesTruncated: sources.truncated})
	}
	return item
}

func decodeMCPWikiPageParams(raw []byte) (mcpWikiPageParams, error) {
	if len(raw) == 0 || len(raw) > 2<<10 || !json.Valid(raw) {
		return mcpWikiPageParams{}, ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var params mcpWikiPageParams
	if decoder.Decode(&params) != nil {
		return mcpWikiPageParams{}, ErrInvalidRequest
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) || !validMCPWikiID(params.ID) || params.Offset < 0 || params.Offset > maxMCPWikiOffset || params.Limit < 0 || params.Limit > maxMCPWikiDetails {
		return mcpWikiPageParams{}, ErrInvalidRequest
	}
	if params.Limit == 0 {
		params.Limit = 4
	}
	return params, nil
}

func validMCPWikiID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i := range value {
		c := value[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func (c *Client) getMCPWikiPage(ctx context.Context, epoch uint64, params mcpWikiPageParams) (json.RawMessage, error) {
	entity, found, err := c.readMCPWikiEntity(ctx, params.ID)
	if err != nil {
		return nil, err
	}
	response := mcpWikiPageResponse{Found: found, Details: []mcpWikiDetail{}, Offset: params.Offset}
	if found {
		if entity.Validate() != nil {
			return nil, ErrInvalidRequest
		}
		entity = c.libraryAttribution(ctx, entity)
		if entity.Validate() != nil {
			return nil, ErrInvalidRequest
		}
		aliases := append([]string(nil), entity.Aliases[:min(8, len(entity.Aliases))]...)
		for i := range aliases {
			aliases[i] = truncateUTF8Bytes(aliases[i], 256)
		}
		related := append([]string(nil), entity.RelatedIDs[:min(8, len(entity.RelatedIDs))]...)
		response.Entity = &mcpWikiPageEntity{ID: entity.ID, Kind: entity.Kind, Name: entity.Name, Aliases: aliases, AliasesTruncated: len(entity.Aliases) > len(aliases), State: entity.State, RelatedIDs: related, RelationsOmitted: len(entity.RelatedIDs) > len(related)}
		for _, alias := range entity.Aliases[:min(8, len(entity.Aliases))] {
			response.Entity.AliasesTruncated = response.Entity.AliasesTruncated || len(alias) > 256
		}
		total := len(entity.Facts) + len(entity.OpenLoops)
		start := min(params.Offset, total)
		end := min(start+params.Limit, total)
		for index := start; index < end; index++ {
			item := mcpWikiDetail{}
			if index < len(entity.Facts) {
				fact := entity.Facts[index]
				item = mcpWikiDetail{Type: "fact", ID: fact.ID, Text: truncateUTF8Bytes(fact.Text, 1200), TextTruncated: len(fact.Text) > 1200, State: fact.State}
				bounded := boundMCPWikiSources(fact.Sources)
				item.Sources, item.SourcesTruncated = bounded.items, bounded.truncated
			} else {
				loop := entity.OpenLoops[index-len(entity.Facts)]
				item = mcpWikiDetail{Type: "open_loop", ID: loop.ID, Text: truncateUTF8Bytes(loop.Text, 1200), TextTruncated: len(loop.Text) > 1200, State: loop.State}
				bounded := boundMCPWikiSources(loop.Sources)
				item.Sources, item.SourcesTruncated = bounded.items, bounded.truncated
			}
			response.Details = append(response.Details, item)
		}
		response.HasMore = end < total
		if response.HasMore {
			response.NextOffset = end
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var encoded []byte
	var marshalErr error
	if !c.PublishMemoryRead(epoch, func() { encoded, marshalErr = json.Marshal(response) }) {
		return nil, ErrMemoryChanging
	}
	if marshalErr != nil || len(encoded) > maxMCPReadOutputBytes {
		clear(encoded)
		return nil, ErrInvalidRequest
	}
	return encoded, nil
}

func (c *Client) readMCPWikiEntity(ctx context.Context, id string) (knowledge.Entity, bool, error) {
	if repository, ok := c.inferredRepository(); ok && enrich.IsEntityID(id) {
		entity, found, err := repository.GetInferredKnowledge(ctx, id)
		if err != nil || found {
			return entity, found, err
		}
	}
	if repository, ok := c.data.(interface {
		GetKnowledgeEntity(context.Context, string) (knowledge.Entity, bool, error)
	}); ok {
		entity, found, err := repository.GetKnowledgeEntity(ctx, id)
		if err != nil || found {
			return entity, found, err
		}
	}
	after := ""
	seen := map[string]bool{}
	seenEntities := map[string]bool{}
	for range knowledge.MaxEntities/knowledge.MaxPageSize + 1 {
		if err := ctx.Err(); err != nil {
			return knowledge.Entity{}, false, err
		}
		page, err := c.data.ListKnowledge(ctx, knowledge.PageRequest{Limit: knowledge.MaxPageSize, After: after})
		if err != nil {
			return knowledge.Entity{}, false, err
		}
		if len(page.Entities) > knowledge.MaxPageSize || page.HasMore != (page.Next != "") || page.HasMore && (len(page.Entities) == 0 || page.Entities[len(page.Entities)-1].ID != page.Next || page.Next == after) {
			return knowledge.Entity{}, false, errKnowledgePage
		}
		pageBytes, err := json.Marshal(page)
		if err != nil || len(pageBytes) > knowledge.MaxPageBytes {
			clear(pageBytes)
			return knowledge.Entity{}, false, errKnowledgePage
		}
		clear(pageBytes)
		for _, entity := range page.Entities {
			if entity.Validate() != nil || seenEntities[entity.ID] {
				return knowledge.Entity{}, false, errKnowledgePage
			}
			seenEntities[entity.ID] = true
			if entity.ID == id {
				return entity, true, nil
			}
		}
		if !page.HasMore {
			return knowledge.Entity{}, false, nil
		}
		if seen[page.Next] {
			return knowledge.Entity{}, false, errKnowledgePage
		}
		seen[page.Next] = true
		after = page.Next
	}
	return knowledge.Entity{}, false, errKnowledgePage
}

type boundedWikiSources struct {
	items     []knowledge.Source
	truncated bool
}

func boundMCPWikiSources(sources []knowledge.Source) boundedWikiSources {
	const maxSources = 4
	result := boundedWikiSources{items: append([]knowledge.Source(nil), sources[:min(maxSources, len(sources))]...), truncated: len(sources) > maxSources}
	return result
}

func truncateUTF8Bytes(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	cut := maximum
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

func decodeMCPTaskParams(raw []byte) (mcpTaskParams, error) {
	if len(raw) == 0 || len(raw) > 2<<10 || !json.Valid(raw) {
		return mcpTaskParams{}, ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var params mcpTaskParams
	if decoder.Decode(&params) != nil {
		return mcpTaskParams{}, ErrInvalidRequest
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) || params.Limit < 0 || params.Limit > maxMCPTaskResults || params.State != "" && params.State != "all" && params.State != string(tasks.Open) && params.State != string(tasks.Completed) && params.State != string(tasks.Cancelled) {
		return mcpTaskParams{}, ErrInvalidRequest
	}
	if params.Limit == 0 {
		params.Limit = 5
	}
	if params.State == "" {
		params.State = string(tasks.Open)
	}
	if params.State == "all" {
		params.State = ""
	}
	if params.After != "" && !tasks.ValidID(params.After) {
		return mcpTaskParams{}, ErrInvalidRequest
	}
	return params, nil
}

func (c *Client) listMCPTasks(ctx context.Context, epoch uint64, params mcpTaskParams) (json.RawMessage, error) {
	state := tasks.State(params.State)
	page, err := c.TaskPage(ctx, params.After, state)
	if err != nil {
		return nil, err
	}
	count := min(len(page.Items), params.Limit)
	response := mcpTaskResponse{Items: make([]mcpTaskItem, 0, count), HasMore: page.HasMore || len(page.Items) > count}
	for _, item := range page.Items[:count] {
		if item.Validate() != nil {
			return nil, ErrInvalidRequest
		}
		response.Items = append(response.Items, boundedMCPTask(item))
		encoded, err := json.Marshal(response)
		if err != nil {
			return nil, ErrInvalidRequest
		}
		if len(encoded) > maxMCPReadOutputBytes {
			response.Items = response.Items[:len(response.Items)-1]
			response.HasMore = true
			break
		}
	}
	if response.HasMore && len(response.Items) > 0 {
		response.Next = response.Items[len(response.Items)-1].ID
	} else if response.HasMore {
		response.Next = page.Next
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var encoded []byte
	var marshalErr error
	if !c.PublishMemoryRead(epoch, func() { encoded, marshalErr = json.Marshal(response) }) {
		return nil, ErrMemoryChanging
	}
	if marshalErr != nil || len(encoded) > maxMCPReadOutputBytes {
		clear(encoded)
		return nil, ErrInvalidRequest
	}
	return encoded, nil
}

func boundedMCPTask(item tasks.Item) mcpTaskItem {
	result := mcpTaskItem{ID: item.ID, Revision: item.Revision, Title: item.Title, State: item.State, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt, SourceIDs: item.Sources(), UserAuthored: item.UserAuthored, DeliveryState: item.Delivery.State, HistoryOmitted: item.HistoryOmitted}
	if item.Reminder != nil {
		result.Reminder = &mcpTaskReminder{DueAt: item.Reminder.DueAt, Repeat: item.Reminder.Repeat, Hour: item.Reminder.Hour, Minute: item.Reminder.Minute, Weekday: item.Reminder.Weekday}
	}
	history := item.History
	if len(history) > 4 {
		result.HistoryTruncated = true
		history = history[len(history)-4:]
	}
	for _, transition := range history {
		result.History = append(result.History, mcpTaskTransition{State: transition.State, At: transition.At, Revision: transition.Revision})
	}
	return result
}

func decodeMCPSnapshotParams(raw []byte) (mcpSnapshotParams, error) {
	if len(raw) == 0 || len(raw) > 2<<10 || !json.Valid(raw) {
		return mcpSnapshotParams{}, ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var params mcpSnapshotParams
	if decoder.Decode(&params) != nil {
		return mcpSnapshotParams{}, ErrInvalidRequest
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) || len(params.SourceIDs) == 0 || len(params.SourceIDs) > maxMCPReadSnapshotIDs || params.MaxBytesPerSource < 0 || params.MaxBytesPerSource > maxMCPReadSnapshotChunk || params.MaxBytesPerSource > 0 && params.MaxBytesPerSource < 4 || len(params.Offsets) > len(params.SourceIDs) {
		return mcpSnapshotParams{}, ErrInvalidRequest
	}
	if params.MaxBytesPerSource == 0 {
		params.MaxBytesPerSource = maxMCPReadSnapshotChunk
	}
	seen := make(map[string]bool, len(params.SourceIDs))
	for _, id := range params.SourceIDs {
		if len(id) != 64 || strings.ToLower(id) != id || seen[id] {
			return mcpSnapshotParams{}, ErrInvalidRequest
		}
		if _, err := hex.DecodeString(id); err != nil {
			return mcpSnapshotParams{}, ErrInvalidRequest
		}
		seen[id] = true
	}
	for id, offset := range params.Offsets {
		if !seen[id] || offset < 0 || offset > MaxSearchBytes {
			return mcpSnapshotParams{}, ErrInvalidRequest
		}
	}
	return params, nil
}

func (c *Client) readMCPSnapshots(ctx context.Context, epoch uint64, params mcpSnapshotParams) (json.RawMessage, error) {
	reader, ok := c.data.(storage.ObservationReader)
	if !ok {
		return nil, ErrNotConfigured
	}
	response := mcpSnapshotResponse{Snapshots: make([]mcpSnapshotItem, 0, len(params.SourceIDs))}
	used := 0
	for _, id := range params.SourceIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		offset := params.Offsets[id]
		var evidence capture.Evidence
		var at time.Time
		var found bool
		var err error
		if timed, ok := c.data.(storage.ObservationTimeReader); ok {
			evidence, at, found, err = storage.ReadSourceEvidenceWithCaptureTime(ctx, timed, id)
		} else {
			evidence, found, err = storage.ReadSourceEvidence(ctx, reader, id)
		}
		if err != nil {
			return nil, err
		}
		if !found {
			response.Truncated = true
			continue
		}
		text := evidence.Text()
		if !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 || offset > len(text) || offset < len(text) && !utf8.RuneStart(text[offset]) {
			return nil, ErrInvalidRequest
		}
		end := min(len(text), offset+params.MaxBytesPerSource)
		for end > offset && end < len(text) && !utf8.RuneStart(text[end]) {
			end--
		}
		item := mcpSnapshotItem{SourceID: id, AppID: evidence.AppID(), Domain: evidence.Domain(), Content: text[offset:end], OffsetBytes: offset, NextOffsetBytes: end, Complete: end == len(text), PartialCapture: evidence.Truncated()}
		if !at.IsZero() {
			item.CapturedAt = at.UTC().Format(time.RFC3339Nano)
		}
		if provenance, multipart := evidence.Provenance(); multipart {
			item.DocumentID = provenance.EditionID
			item.PartIndex = &provenance.PartIndex
			item.PartCount = &provenance.PartCount
			item.DocumentOffset = &provenance.Start
			item.DocumentTotalBytes = &provenance.TotalBytes
		}
		response.Snapshots = append(response.Snapshots, item)
		used += len(item.SourceID) + len(item.AppID) + len(item.Domain) + len(item.CapturedAt) + len(item.Content) + 192
		if used > localapi.MaxRPCSnapshotResponseBytes {
			return nil, ErrInvalidRequest
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var encoded []byte
	var marshalErr error
	if !c.PublishMemoryRead(epoch, func() { encoded, marshalErr = json.Marshal(response) }) {
		return nil, ErrMemoryChanging
	}
	if marshalErr != nil || len(encoded) > localapi.MaxRPCSnapshotResponseBytes {
		clear(encoded)
		return nil, ErrInvalidRequest
	}
	return encoded, nil
}

func decodeMCPReadParams(raw []byte) (mcpSearchParams, error) {
	if len(raw) == 0 || len(raw) > 2<<10 || !json.Valid(raw) {
		return mcpSearchParams{}, ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var params mcpSearchParams
	if decoder.Decode(&params) != nil {
		return mcpSearchParams{}, ErrInvalidRequest
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return mcpSearchParams{}, ErrInvalidRequest
	}
	return params, nil
}

func truncateMCPReadText(value string) (string, bool) {
	if len(value) <= maxMCPReadTextBytes {
		return value, false
	}
	cut := maxMCPReadTextBytes
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut], true
}
