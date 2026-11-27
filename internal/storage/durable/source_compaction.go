package durable

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const sourceCompactionNS = "lib-compact-v1"

// These bound one encrypted record and one read response, not a source's
// lifetime coverage. All spans have independent ordinals and remain pageable.
const maxSourceCompactionRecordBytes = 6*derive.MaxCompactionBlockTextBytes + 512
const maxSourceCompactionPageBytes = 128 << 10

var _ enrich.CompactionReader = (*Store)(nil)

func sourceCompactionID(sourceID string, index int) string {
	return sourceID + "/" + strconv.Itoa(index)
}

func (tx *Tx) putSourceCompaction(progress *enrich.Progress, chunk enrich.Chunk) error {
	block := enrich.CompactionBlock{
		SourceID: chunk.SourceID, ModelKey: chunk.ModelKey,
		Index: progress.CompactionParts, Start: chunk.Start, End: chunk.End,
		Next: chunk.Next, Text: chunk.Compaction,
	}
	encoded := mustJSON(block)
	defer clear(encoded)
	if len(encoded) > maxSourceCompactionRecordBytes || len(encoded) > maxSourceCompactionPageBytes {
		return enrich.ErrCapacity
	}
	if err := tx.putIndexValueLocked(sourceCompactionNS, sourceCompactionID(chunk.SourceID, block.Index), encoded); err != nil {
		return err
	}
	progress.CompactionParts++
	progress.CompactionSlots = max(progress.CompactionSlots, progress.CompactionParts)
	return nil
}

// ListSourceCompactions returns only the active model generation's committed
// source spans. after is the next ordinal to read, initially zero.
func (s *Store) ListSourceCompactions(ctx context.Context, sourceID string, after, limit int) (enrich.CompactionPage, error) {
	if ctx == nil || !validHash(sourceID) || after < 0 || limit < 1 || limit > 32 {
		return enrich.CompactionPage{}, enrich.ErrInvalid
	}
	page := enrich.CompactionPage{Blocks: []enrich.CompactionBlock{}}
	err := s.view(ctx, func(reader bolt.Reader) error {
		progress, found, err := s.readEnrichmentProgress(reader, sourceID)
		if err != nil || !found {
			return err
		}
		current, err := s.isCurrentNoteSourceReader(reader, sourceID)
		if err != nil || !current {
			return err
		}
		source, _, found, err := s.sourceEvidenceReader(reader, sourceID)
		if err != nil || !found {
			return err
		}
		if after > progress.CompactionParts {
			return enrich.ErrStale
		}
		page.Next = after
		used := 0
		for index := after; index < progress.CompactionParts && len(page.Blocks) < limit; index++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			encoded, found, err := s.readIndexValueReader(reader, sourceCompactionNS, sourceCompactionID(sourceID, index), maxSourceCompactionRecordBytes)
			if err != nil {
				return err
			}
			if !found {
				return ErrCorrupt
			}
			var block enrich.CompactionBlock
			err = json.Unmarshal(encoded, &block)
			encodedBytes := len(encoded)
			clear(encoded)
			if err != nil || block.SourceID != sourceID || block.ModelKey != progress.ModelKey || block.Index != index || block.Start < 0 || block.End <= block.Start || block.End > len(source.Text()) || block.Next <= block.Start || block.Next > block.End || block.Next > progress.Next || !enrich.ValidCompaction(1, block.Text) {
				return ErrCorrupt
			}
			if used+encodedBytes > maxSourceCompactionPageBytes {
				if len(page.Blocks) == 0 {
					return ErrCorrupt
				}
				break
			}
			page.Blocks = append(page.Blocks, block)
			used += encodedBytes
			page.Next = index + 1
		}
		page.HasMore = page.Next < progress.CompactionParts
		return nil
	})
	if err != nil {
		return enrich.CompactionPage{}, err
	}
	return page, nil
}

// eraseOneSourceCompaction drains the high-water range, including stale slots
// left by a shorter replacement generation. The receipt and deletion commit
// together, making every privacy erasure resumable with bounded transactions.
// The caller holds tx.mu.
func (tx *Tx) eraseOneSourceCompaction(progress *enrich.Progress) error {
	if progress.CompactionSlots == 0 {
		return nil
	}
	index := progress.CompactionSlots - 1
	if err := tx.deleteInferredIndex(sourceCompactionNS, sourceCompactionID(progress.SourceID, index)); err != nil {
		return err
	}
	progress.CompactionSlots = index
	progress.CompactionParts = min(progress.CompactionParts, index)
	if progress.CompactionParts == 0 {
		progress.Compaction, progress.CompactionVersion = "", 0
	}
	return tx.putEnrichmentProgress(*progress)
}
