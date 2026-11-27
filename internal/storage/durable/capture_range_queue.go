package durable

import (
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
	"golang.org/x/text/unicode/norm"
)

// One input quantum can finish several terms from a retained Unicode segment.
// Keep overlapping original spans independent: their core terms must not gain
// siblings from hydration context, particularly across source-read budgets.
type capturePendingRange struct {
	Span    [4]int64
	Digests [][32]byte
}

const captureRangeQueueMax = exactIndexBatchTerms + 4*norm.MaxSegmentSize

// Each span has four int64 values (at most81 JSON bytes); each digest has32
// decimal byte values (at most129 bytes). Total digests and spans each stay
// within the input-quantum bound, independently of capture/word length.
const captureRangeQueueBytes = captureRangeQueueMax*256 + 512
const captureRangeCoreBytes = captureRangeQueueMax*130 + 512

func captureRangeQueueKey(id, publication string) string {
	return "range-queue:" + id + ":" + publication
}
func captureRangeCoreKey(id, publication string) string {
	return "range-core:" + id + ":" + publication
}
func (p capturePendingRange) work() *captureRangeWork {
	return &captureRangeWork{Start: p.Span[0], End: p.Span[1], FirstPart: p.Span[2], LastPart: p.Span[3], Stage: 1, CoreTerms: len(p.Digests)}
}
func (p capturePendingRange) valid(summary capture.CaptureSummary) bool {
	return p.Span[0] >= 0 && p.Span[1] > p.Span[0] && p.Span[1] <= summary.Bytes() && p.Span[2] >= 0 && p.Span[3] > p.Span[2] && p.Span[3] < summary.PartCount() && len(p.Digests) > 0 && len(p.Digests) <= captureRangeQueueMax
}
func (tx *Tx) activateCaptureRange(id string, r *captureEditionRecord, p capturePendingRange) error {
	if err := tx.capturePutJSONLocked(captureRangeCoreKey(id, r.Publication), p.Digests, captureRangeCoreBytes); err != nil {
		return err
	}
	r.IndexRangeWork = p.work()
	return nil
}
func (tx *Tx) queueCaptureRanges(id string, r *captureEditionRecord, summary capture.CaptureSummary, tokens []search.CaptureToken) error {
	if r.IndexRangeQueued != 0 || r.IndexRangeWork != nil {
		return ErrCorrupt
	}
	positions := make(map[[4]int64]int)
	seen := make(map[[4]int64]map[[32]byte]bool)
	var pending []capturePendingRange
	for _, token := range tokens {
		if token.FirstPart == token.LastPart {
			continue
		}
		span := [4]int64{token.Start, token.End, token.FirstPart, token.LastPart}
		index, exists := positions[span]
		if !exists {
			index = len(pending)
			positions[span] = index
			pending = append(pending, capturePendingRange{Span: span})
			seen[span] = make(map[[32]byte]bool)
		}
		if !seen[span][token.Digest] {
			pending[index].Digests = append(pending[index].Digests, token.Digest)
			seen[span][token.Digest] = true
		}
	}
	if len(pending) == 0 {
		return nil
	}
	total := 0
	for _, p := range pending {
		if !p.valid(summary) {
			return ErrCorrupt
		}
		total += len(p.Digests)
	}
	if len(pending) > captureRangeQueueMax || total > captureRangeQueueMax {
		return ErrCorrupt
	}
	if err := tx.activateCaptureRange(id, r, pending[0]); err != nil {
		return err
	}
	pending = pending[1:]
	if len(pending) == 0 {
		return nil
	}
	if err := tx.capturePutJSONLocked(captureRangeQueueKey(id, r.Publication), pending, captureRangeQueueBytes); err != nil {
		return err
	}
	r.IndexRangeQueued = len(pending)
	return nil
}
func (tx *Tx) nextCaptureRange(id string, r *captureEditionRecord, summary capture.CaptureSummary) error {
	if r.IndexRangeWork != nil || r.IndexRangeQueued < 1 || r.IndexRangeQueued > captureRangeQueueMax {
		return ErrCorrupt
	}
	var pending []capturePendingRange
	key := captureRangeQueueKey(id, r.Publication)
	found, err := tx.store.captureReadJSON(tx.writer, key, captureRangeQueueBytes, &pending)
	if err != nil {
		return err
	}
	if !found || len(pending) != r.IndexRangeQueued {
		return ErrCorrupt
	}
	total := 0
	for _, p := range pending {
		if !p.valid(summary) {
			return ErrCorrupt
		}
		total += len(p.Digests)
	}
	if total > captureRangeQueueMax {
		return ErrCorrupt
	}
	if err := tx.activateCaptureRange(id, r, pending[0]); err != nil {
		return err
	}
	pending = pending[1:]
	r.IndexRangeQueued = len(pending)
	if len(pending) == 0 {
		return tx.deleteIndexValueLocked(captureNS, key)
	}
	return tx.capturePutJSONLocked(key, pending, captureRangeQueueBytes)
}
func (tx *Tx) captureRangeCore(id string, r captureEditionRecord, w captureRangeWork) (map[[32]byte]bool, error) {
	if w.CoreTerms == 0 {
		return nil, nil
	}
	if w.CoreTerms < 1 || w.CoreTerms > captureRangeQueueMax {
		return nil, ErrCorrupt
	}
	var digests [][32]byte
	found, err := tx.store.captureReadJSON(tx.writer, captureRangeCoreKey(id, r.Publication), captureRangeCoreBytes, &digests)
	if err != nil {
		return nil, err
	}
	if !found || len(digests) != w.CoreTerms {
		return nil, ErrCorrupt
	}
	core := make(map[[32]byte]bool, len(digests))
	for _, digest := range digests {
		if core[digest] {
			return nil, ErrCorrupt
		}
		core[digest] = true
	}
	return core, nil
}
func (tx *Tx) finishCaptureRangeWork(id string, r *captureEditionRecord) error {
	if r.IndexRangeWork != nil && r.IndexRangeWork.CoreTerms > 0 {
		if err := tx.deleteIndexValueLocked(captureNS, captureRangeCoreKey(id, r.Publication)); err != nil {
			return err
		}
	}
	r.IndexRangeWork = nil
	return nil
}
