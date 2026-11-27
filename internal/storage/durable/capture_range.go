package durable

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/privacy"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

func captureRangeHeadKey(id, publication string, part int64) string {
	return "part-ranges:" + capturePartSourceID(id, publication, part)
}
func (s *Store) captureRangeHead(reader bolt.Reader, id, publication string, part int64) (string, error) {
	var head string
	found, err := s.captureReadJSON(reader, captureRangeHeadKey(id, publication, part), 128, &head)
	if err != nil {
		return "", err
	}
	if found && !validHash(head) {
		return "", ErrCorrupt
	}
	return head, nil
}

func (s *Store) captureRangeEvidenceReader(reader bolt.Reader, mapping captureIndexPart, edition captureEditionRecord, summary capture.CaptureSummary) (capture.Evidence, error) {
	if mapping.End > summary.Bytes() || mapping.LastPart >= summary.PartCount() {
		return capture.Evidence{}, ErrCorrupt
	}
	if mapping.Bytes > capture.MaxObservationTextBytes {
		return capture.Evidence{}, search.ErrIndexBudget
	}
	var text strings.Builder
	text.Grow(mapping.Bytes)
	var origins []privacy.OriginSpan
	for i := mapping.Index; i <= mapping.LastPart; i++ {
		part, err := s.captureMemberPartReader(reader, edition, summary, i)
		if err != nil {
			return capture.Evidence{}, err
		}
		a, b := max(mapping.Start, part.Offset()), min(mapping.End, part.Offset()+int64(len(part.Text())))
		if a >= b || i == mapping.Index && a != mapping.Start || i == mapping.LastPart && b != mapping.End {
			return capture.Evidence{}, ErrCorrupt
		}
		piece := part.Text()[a-part.Offset() : b-part.Offset()]
		if !utf8.ValidString(piece) {
			return capture.Evidence{}, ErrCorrupt
		}
		text.WriteString(piece)
		for _, origin := range part.Origins() {
			start, end := max(a, origin.OutputStart), min(b, origin.OutputEnd)
			if start >= end {
				continue
			}
			if origin.Exact {
				origin.InputStart += start - origin.OutputStart
				origin.InputEnd = origin.InputStart + end - start
			}
			origin.OutputStart, origin.OutputEnd = start, end
			origins = append(origins, origin)
		}
	}
	if text.Len() != mapping.Bytes {
		return capture.Evidence{}, ErrCorrupt
	}
	e, err := capture.CaptureRangeEvidence(summary, mapping.Publication, mapping.Start, mapping.Index, text.String(), origins)
	if err != nil || e.SourceID() != mapping.sourceID() {
		return capture.Evidence{}, ErrCorrupt
	}
	return e, nil
}

func (tx *Tx) eraseCaptureRangeStep(j *sourceErasure, id string, edition captureEditionRecord) (bool, error) {
	if j.RangeID == "" {
		head, err := tx.store.captureRangeHead(tx.writer, id, edition.Publication, j.Part)
		if err != nil {
			return false, err
		}
		if head == "" {
			return true, nil
		}
		j.RangeID = head
	}
	mapping, found, err := tx.store.captureIndexPartReader(tx.writer, j.RangeID)
	if err != nil || !found {
		return false, ErrCorrupt
	}
	if !mapping.Range || mapping.EditionID != id || mapping.Publication != edition.Publication || mapping.Index != j.Part {
		return false, ErrCorrupt
	}
	if j.RangePhase < 8 {
		part := sourceErasure{SourceID: j.RangeID, Phase: j.RangePhase, After: j.RangeAfter}
		if _, err := tx.eraseSourceStep(&part); err != nil {
			return false, err
		}
		j.RangePhase, j.RangeAfter = part.Phase, part.After
		return false, nil
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	scoped := tx.capturePartSearch()
	done, err := scoped.removePagedSearchBatchLocked(j.RangeID, true)
	if err != nil || !done {
		return false, err
	}
	if err := scoped.removeSearchIndexSourceLocked(j.RangeID); err != nil {
		return false, err
	}
	if err := tx.deleteIndexValueLocked(captureNS, "index-part:"+j.RangeID); err != nil {
		return false, err
	}
	key := captureRangeHeadKey(id, edition.Publication, j.Part)
	if mapping.Next == "" {
		if err := tx.deleteIndexValueLocked(captureNS, key); err != nil {
			return false, err
		}
	} else if err := tx.capturePutJSONLocked(key, mapping.Next, 128); err != nil {
		return false, err
	}
	j.RangeID = ""
	j.RangePhase = 0
	j.RangeAfter = ""
	return false, nil
}

func (r captureIndexPart) String() string {
	return fmt.Sprintf("capture index membership (%t)", r.Range)
}
