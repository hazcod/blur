package durable

import (
	"context"
	"slices"

	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/enrich"
)

var _ enrich.FactConfirmer = (*Store)(nil)

// ConfirmInferredFact accepts the unchanged preview and preserves its source
// attribution. A correction is a different operation with user-authored text.
func (s *Store) ConfirmInferredFact(ctx context.Context, id string, expected knowledge.Fact) error {
	if s == nil || s.state == nil {
		return enrich.ErrUnavailable
	}
	if ctx == nil || !enrich.IsEntityID(id) || !validOpaqueID(expected.ID) || expected.State != knowledge.Proposed || len(expected.Sources) == 0 {
		return enrich.ErrInvalid
	}
	expected.Sources = slices.Clone(expected.Sources)
	return s.inferredUpdate(ctx, func(tx *Tx) error {
		record, found, err := s.readInferredRecord(tx.writer, id)
		if err != nil {
			return err
		}
		if !found {
			return enrich.ErrStale
		}
		for i, fact := range record.Entity.Facts {
			if fact.ID != expected.ID {
				continue
			}
			if fact.Text != expected.Text || !slices.Equal(fact.Sources, expected.Sources) || fact.State != knowledge.Proposed && fact.State != knowledge.Confirmed {
				return enrich.ErrStale
			}
			for _, source := range fact.Sources {
				if source.UserAuthored || source.ID == "" {
					return enrich.ErrInvalid
				}
				if _, _, found, err := s.sourceEvidenceReader(tx.writer, source.ID); err != nil {
					return err
				} else if !found {
					return enrich.ErrSourceGone
				}
				current, err := s.isCurrentNoteSourceReader(tx.writer, source.ID)
				if err != nil {
					return err
				}
				if !current {
					return enrich.ErrSourceGone
				}
			}
			if fact.State == knowledge.Confirmed {
				return nil
			}
			record.Entity.Facts[i].State = knowledge.Confirmed
			return tx.putInferredRecord(record, true)
		}
		for pageIndex := 0; pageIndex < record.FactPageCount; pageIndex++ {
			page, err := s.readInferredFactPage(tx.writer, id, pageIndex)
			if err != nil {
				return err
			}
			for i, fact := range page.Facts {
				if fact.ID != expected.ID {
					continue
				}
				if fact.Text != expected.Text || !slices.Equal(fact.Sources, expected.Sources) || fact.State != knowledge.Proposed && fact.State != knowledge.Confirmed {
					return enrich.ErrStale
				}
				if err := validateConfirmableFactSources(tx, fact); err != nil {
					return err
				}
				if fact.State == knowledge.Confirmed {
					return nil
				}
				page.Facts[i].State = knowledge.Confirmed
				encoded := mustJSON(page)
				defer clear(encoded)
				return tx.putIndexValueLocked(inferredFactPageNS, inferredPageID(id, pageIndex), encoded)
			}
		}
		return enrich.ErrStale
	})
}

func validateConfirmableFactSources(tx *Tx, fact knowledge.Fact) error {
	for _, source := range fact.Sources {
		if source.UserAuthored || source.ID == "" {
			return enrich.ErrInvalid
		}
		if _, _, found, err := tx.store.sourceEvidenceReader(tx.writer, source.ID); err != nil {
			return err
		} else if !found {
			return enrich.ErrSourceGone
		}
		current, err := tx.store.isCurrentNoteSourceReader(tx.writer, source.ID)
		if err != nil {
			return err
		}
		if !current {
			return enrich.ErrSourceGone
		}
	}
	return nil
}
