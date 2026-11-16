package durable

import (
	"context"
	"slices"

	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// A full subject has at most 128 source links. Retiring at most 16 of them in
// one transaction leaves headroom for encrypted entity/name/order updates and
// each source's reverse receipt. At most three proposed subjects are touched.
const maxRetiredNoteClaimSources = 16

func (s *Store) retireSupersededNoteClaims(ctx context.Context, chunk enrich.Chunk) (bool, error) {
	if len(chunk.Proposals) == 0 || len(chunk.Proposals) > derive.MaxSuggestions {
		return false, nil
	}
	var ids []string
	err := s.view(ctx, func(reader bolt.Reader) error {
		source, _, found, err := s.sourceEvidenceReader(reader, chunk.SourceID)
		if err != nil {
			return err
		}
		if !found {
			return enrich.ErrSourceGone
		}
		if source.AppID() != "sh.blur.notes" {
			return nil
		}
		current, err := s.isCurrentNoteSourceReader(reader, chunk.SourceID)
		if err != nil {
			return err
		}
		if !current {
			return enrich.ErrSourceGone
		}
		for _, proposal := range chunk.Proposals {
			id := enrich.EntityID(proposal.Kind, proposal.Name, source.AppID(), "local")
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	retired := false
	for _, id := range ids {
		changed, err := s.retireSupersededNoteEntityClaims(ctx, id)
		if err != nil {
			return retired, err
		}
		retired = retired || changed
	}
	return retired, nil
}

func (s *Store) retireSupersededNoteEntityClaims(ctx context.Context, id string) (bool, error) {
	retired := false
	err := s.inferredUpdate(ctx, func(tx *Tx) error {
		record, found, err := s.readInferredRecord(tx.writer, id)
		if err != nil || !found {
			return err
		}
		count := 0
		for _, sourceID := range record.Sources {
			if count >= maxRetiredNoteClaimSources {
				break
			}
			current, err := s.isCurrentNoteSourceReader(tx.writer, sourceID)
			if err != nil {
				return err
			}
			if current || !hasRetirableNoteClaims(record, sourceID) {
				continue
			}
			progress, found, err := s.readEnrichmentProgress(tx.writer, sourceID)
			if err != nil {
				return err
			}
			if !found || !slices.Contains(progress.Entities, id) {
				return ErrCorrupt
			}
			// Corrections and dismissals are explicit user decisions. Their
			// suppression receipts and muted facts are retained unchanged.
			if progress.Suppressed {
				continue
			}
			one := progress
			one.Entities = []string{id}
			kept, err := tx.pruneInferredClaims(one, true)
			if err != nil {
				return err
			}
			if len(kept) == 0 {
				progress.Entities = removeString(progress.Entities, id)
				// Keep terminal/model progress: obsolete evidence must not be
				// queued again, and later source erasure still finds its receipt.
				if err := tx.putEnrichmentProgress(progress); err != nil {
					return err
				}
			}
			count++
			retired = true
		}
		return nil
	})
	return retired && err == nil, err
}

func hasRetirableNoteClaims(record inferredRecord, sourceID string) bool {
	referenced := false
	for _, fact := range record.Entity.Facts {
		for _, source := range fact.Sources {
			if source.ID != sourceID {
				continue
			}
			referenced = true
			if fact.State != knowledge.Confirmed {
				return true
			}
		}
	}
	// An obsolete activity-only link also consumes the bounded source list.
	// A confirmed source-backed fact, however, keeps its original proof/link.
	return !referenced
}
