package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/storage"
)

// ErrRecapChanged means an earlier derived handle or provenance receipt no
// longer identifies the current recap. Cached answer text is never authority.
var ErrRecapChanged = errors.New("saved recap changed or is unavailable")

const recapSourcePreviewBytes = 4096

func validRecapSourceID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

type RecapSourcePage struct {
	Hits            []SearchHit
	Next            string
	Receipt         string
	Total           int
	CoverageLimited bool
}

type recapSourceState struct {
	ids     []string
	receipt string
	limited bool
}

type recapPublication struct {
	fence storage.ReadPublicationFence
	epoch uint64
}

func (c *Client) beginRecapPublication() (recapPublication, bool) {
	fence, ok := c.data.(storage.ReadPublicationFence)
	if !ok {
		return recapPublication{}, false
	}
	epoch, stable := fence.BeginPublicationRead()
	return recapPublication{fence: fence, epoch: epoch}, stable
}

func hourRecapCoverageLimited(row chronicle.Summary) bool {
	// Structured hour coverage was not persisted in legacy summaries. Trusted
	// current generations retain their deterministic coverage note in Text.
	return row.InputReceipt == "" || len(row.DependencyIDs()) > len(row.SourceIDs) || strings.Contains(row.Text, "\n\nCoverage: saved captures only;")
}

// currentRecapSources authenticates the answer's derived handle against one
// current stored period. Its claimed text and source list are deliberately unused.
func (c *Client) currentRecapSources(ctx context.Context, anchor assistant.Evidence) (recapSourceState, error) {
	if !validRecapSourceID(anchor.SourceID) || anchor.CapturedAt.IsZero() || !anchor.EndAt.After(anchor.CapturedAt) {
		return recapSourceState{}, ErrInvalidRequest
	}
	var ids []string
	var encoded []byte
	var err error
	limited := false
	if anchor.Layer == assistant.LayerHour {
		id := "hour:" + anchor.CapturedAt.UTC().Format("20060102T15Z")
		row, found, readErr := c.chronicles.GetChronicle(ctx, id)
		if readErr != nil {
			return recapSourceState{}, readErr
		}
		if !found || row.HourID != id || !row.Start.Equal(anchor.CapturedAt) || !row.End.Equal(anchor.EndAt) || strings.TrimSpace(row.Text) == "" || anchor.SourceID != syntheticID("chronicle", row.HourID, row.Start.UTC().Format(time.RFC3339Nano), row.End.UTC().Format(time.RFC3339Nano), row.Text) {
			return recapSourceState{}, ErrRecapChanged
		}
		ids = row.SourceIDs
		limited = hourRecapCoverageLimited(row)
		encoded, err = json.Marshal(row)
	} else {
		var id string
		switch anchor.Layer {
		case assistant.LayerDay:
			id = "day:" + anchor.CapturedAt.Format("2006-01-02")
		case assistant.LayerWeek:
			y, w := anchor.CapturedAt.ISOWeek()
			id = fmt.Sprintf("week:%04d-W%02d", y, w)
		case assistant.LayerMonth:
			id = "month:" + anchor.CapturedAt.Format("2006-01")
		case assistant.LayerYear:
			id = "year:" + anchor.CapturedAt.Format("2006")
		default:
			return recapSourceState{}, ErrInvalidRequest
		}
		row, found, readErr := c.chronicles.GetChronicleRollup(ctx, id)
		if readErr != nil {
			return recapSourceState{}, readErr
		}
		if !found || row.ID != id || !row.Start.Equal(anchor.CapturedAt) || !row.End.Equal(anchor.EndAt) || strings.TrimSpace(row.Text) == "" || anchor.SourceID != syntheticID("chronicle-rollup", row.ID, row.Start.UTC().Format(time.RFC3339Nano), row.End.UTC().Format(time.RFC3339Nano), row.Text) {
			return recapSourceState{}, ErrRecapChanged
		}
		ids = row.SourceIDs
		limited = row.Sampled || row.CoverageVersion == 0
		encoded, err = json.Marshal(row)
	}
	if err != nil {
		return recapSourceState{}, ErrInvalidRequest
	}
	defer clear(encoded)
	if len(ids) == 0 || len(ids) > chronicle.MaxRollupSources {
		return recapSourceState{}, ErrRecapChanged
	}
	for i, id := range ids {
		if !validRecapSourceID(id) || i > 0 && id <= ids[i-1] {
			return recapSourceState{}, ErrInvalidRequest
		}
	}
	return recapSourceState{ids: slices.Clone(ids), receipt: syntheticID("recap-source-receipt", anchor.SourceID, string(encoded)), limited: limited}, nil
}

func recapSourceOffset(cursor, receipt string, total int) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	if len(cursor) > 80 {
		return 0, ErrInvalidRequest
	}
	proof, offset, ok := strings.Cut(cursor, ":")
	if !ok || proof != receipt {
		return 0, ErrRecapChanged
	}
	n, err := strconv.Atoi(offset)
	if err != nil || n < 1 || n >= total || strconv.Itoa(n) != offset {
		return 0, ErrInvalidRequest
	}
	return n, nil
}

func (c *Client) readRecapOriginal(ctx context.Context, id string, preview bool) (SearchHit, error) {
	source, found, err := storage.ReadSourceEvidence(ctx, c.data, id)
	if err != nil {
		return SearchHit{}, err
	}
	if !found || source.SourceID() != id || source.AppID() == appidentity.BundleID {
		return SearchHit{}, ErrRecapChanged
	}
	text := source.Text()
	truncated := source.Truncated()
	if preview && len(text) > recapSourcePreviewBytes {
		text = text[:recapSourcePreviewBytes]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		text = strings.Clone(text)
		truncated = true
	}
	return SearchHit{SourceID: id, AppID: source.AppID(), Domain: source.Domain(), WindowTitle: source.Title(), Text: text, Truncated: truncated, Partial: source.Truncated()}, nil
}

func (c *Client) recapSourcesStillCurrent(ctx context.Context, anchor assistant.Evidence, receipt string, epoch uint64) error {
	current, err := c.currentRecapSources(ctx, anchor)
	if err != nil {
		return err
	}
	if current.receipt != receipt {
		return ErrRecapChanged
	}
	if !c.MemoryReadCurrent(epoch) {
		return ErrMemoryChanging
	}
	return ctx.Err()
}

// RecapSourcesPage pages the current recap's bounded supporting citations. It
// hydrates only this page, then rechecks provenance and the memory mutation fence.
func (c *Client) RecapSourcesPage(parent context.Context, anchor assistant.Evidence, after string, limit int) (RecapSourcePage, error) {
	if c == nil || c.data == nil || c.chronicles == nil || parent == nil || limit < 1 || limit > 8 {
		return RecapSourcePage{}, ErrInvalidRequest
	}
	ctx, done, err := c.beginOperation(parent, shortOperationTimeout)
	if err != nil {
		return RecapSourcePage{}, err
	}
	defer done()
	if err := ctx.Err(); err != nil {
		return RecapSourcePage{}, err
	}
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return RecapSourcePage{}, ErrMemoryChanging
	}
	state, err := c.currentRecapSources(ctx, anchor)
	if err != nil {
		return RecapSourcePage{}, err
	}
	start, err := recapSourceOffset(after, state.receipt, len(state.ids))
	if err != nil {
		return RecapSourcePage{}, err
	}
	end := min(start+limit, len(state.ids))
	page := RecapSourcePage{Receipt: state.receipt, Total: len(state.ids), CoverageLimited: state.limited}
	for _, id := range state.ids[start:end] {
		if err := ctx.Err(); err != nil {
			return RecapSourcePage{}, err
		}
		hit, err := c.readRecapOriginal(ctx, id, true)
		if err != nil {
			return RecapSourcePage{}, err
		}
		page.Hits = append(page.Hits, hit)
	}
	if end < len(state.ids) {
		page.Next = state.receipt + ":" + strconv.Itoa(end)
	}
	if err := c.recapSourcesStillCurrent(ctx, anchor, state.receipt, epoch); err != nil {
		return RecapSourcePage{}, err
	}
	return page, nil
}

// RecapSource opens a single supporting original only while both membership and
// the page's complete provenance receipt still match the current stored recap.
func (c *Client) RecapSource(parent context.Context, anchor assistant.Evidence, receipt, sourceID string) (SearchHit, error) {
	if c == nil || c.data == nil || c.chronicles == nil || parent == nil || !validRecapSourceID(receipt) || !validRecapSourceID(sourceID) {
		return SearchHit{}, ErrInvalidRequest
	}
	ctx, done, err := c.beginOperation(parent, shortOperationTimeout)
	if err != nil {
		return SearchHit{}, err
	}
	defer done()
	if err := ctx.Err(); err != nil {
		return SearchHit{}, err
	}
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return SearchHit{}, ErrMemoryChanging
	}
	state, err := c.currentRecapSources(ctx, anchor)
	if err != nil {
		return SearchHit{}, err
	}
	if state.receipt != receipt || !slices.Contains(state.ids, sourceID) {
		return SearchHit{}, ErrRecapChanged
	}
	hit, err := c.readRecapOriginal(ctx, sourceID, false)
	if err != nil {
		return SearchHit{}, err
	}
	if err := c.recapSourcesStillCurrent(ctx, anchor, receipt, epoch); err != nil {
		return SearchHit{}, err
	}
	return hit, nil
}
