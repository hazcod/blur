package durable

import (
	"bytes"
	"context"
	"slices"
	"strings"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// A receipt is local to one reset attempt. Each cleanup step leaves the old
// model/cursor intact and atomically updates its reverse entity receipt. A
// restart can repeat the bounded cleanup without any new durable format.
// The final span commit must still match this exact last committed progress.
type enrichmentGenerationReset struct {
	progress enrich.Progress
}

func sameEnrichmentResetProgress(a, b enrich.Progress) bool {
	left, right := mustJSON(a), mustJSON(b)
	defer clear(left)
	defer clear(right)
	return bytes.Equal(left, right)
}

// afterStep runs outside all store locks, after a committed bounded step. It
// supports interruption checks using the same boundary as source erasure.
func (s *Store) retireEnrichmentGenerationClaims(ctx context.Context, chunk enrich.Chunk, afterStep func() error) (*enrichmentGenerationReset, error) {
	if ctx == nil || !validHash(chunk.SourceID) || !validHash(chunk.ModelKey) || !enrich.ValidCompaction(chunk.CompactionVersion, chunk.Compaction) {
		return nil, enrich.ErrInvalid
	}
	if chunk.PreserveClaims || chunk.Start != 0 || chunk.RetryInvalid {
		return nil, nil
	}
	var reset *enrichmentGenerationReset
	err := s.view(ctx, func(reader bolt.Reader) error {
		progress, found, err := s.readEnrichmentProgress(reader, chunk.SourceID)
		if err != nil || !found || progress.ModelKey == chunk.ModelKey || progress.Suppressed || len(progress.Entities) == 0 {
			return err
		}
		// The failed bulk attempt checked this span, but cleanup has its own
		// source/currentness validation so it never trusts an earlier snapshot.
		source, _, found, err := s.sourceEvidenceReader(reader, chunk.SourceID)
		if err != nil {
			return err
		}
		if !found {
			return enrich.ErrSourceGone
		}
		current, err := s.isCurrentNoteSourceReader(reader, chunk.SourceID)
		if err != nil {
			return err
		}
		if !current {
			return enrich.ErrSourceGone
		}
		size := chunk.SpanBytes
		if size == 0 {
			size = enrich.ChunkBytes
		}
		end, next, complete, err := enrich.NextSizedChunk(source.Text(), chunk.Start, size)
		if err != nil || chunk.End != end || chunk.Next != next || chunk.Complete != complete {
			return enrich.ErrStale
		}
		span := strings.TrimSpace(source.Text()[chunk.Start:chunk.End])
		if chunk.Declined || source.AppID() == appidentity.BundleID || span == "" {
			if len(chunk.Proposals) != 0 || chunk.Declined && (source.AppID() == appidentity.BundleID || span == "") {
				return enrich.ErrInvalid
			}
		} else if derive.ValidateSuggestions([]derive.Evidence{{SourceID: chunk.SourceID, Text: span}}, chunk.Proposals) != nil {
			return enrich.ErrInvalid
		}
		reset = &enrichmentGenerationReset{progress: progress}
		return nil
	})
	if err != nil || reset == nil {
		return nil, err
	}
	for _, id := range slices.Clone(reset.progress.Entities) {
		var committed enrich.Progress
		err := s.inferredUpdate(ctx, func(tx *Tx) error {
			// Membership receipts prove immutable source presence with bounded
			// IO. Source erasure gates Update before any derived mutation.
			_, present, err := s.sourceAdmissionReader(tx.writer, chunk.SourceID)
			if err != nil {
				return err
			}
			current, err := s.isCurrentNoteSourceReader(tx.writer, chunk.SourceID)
			if err != nil {
				return err
			}
			if !present || !current {
				return enrich.ErrSourceGone
			}
			progress, found, err := s.readEnrichmentProgress(tx.writer, chunk.SourceID)
			if err != nil {
				return err
			}
			if !found || !sameEnrichmentResetProgress(progress, reset.progress) {
				return enrich.ErrStale
			}
			one := progress
			one.Entities = []string{id}
			retained, err := tx.pruneInferredClaims(one, true)
			if err != nil {
				return err
			}
			if len(retained) == 0 {
				progress.Entities = removeString(progress.Entities, id)
			}
			if err := tx.putEnrichmentProgress(progress); err != nil {
				return err
			}
			committed = progress
			return nil
		})
		if err != nil {
			return nil, err
		}
		reset.progress = committed
		if afterStep != nil {
			if err := afterStep(); err != nil {
				return nil, err
			}
		}
	}
	return reset, nil
}
