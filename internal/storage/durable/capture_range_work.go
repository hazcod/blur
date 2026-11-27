package durable

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
)

// A second bounded pass finds an identifier's original end before assigning
// its immutable range identity. No component-word list or source text is saved.
type captureRangeWork struct {
	Start, End, FirstPart, LastPart, Part, Offset int64
	Stage, Metadata                               int
	CoreTerms                                     int                        `json:",omitempty"`
	Underscore                                    bool                       `json:",omitempty"`
	Stream                                        *search.CaptureTokenStream `json:",omitempty"`
}

func (w captureRangeWork) valid(parts int64) bool {
	return w.CoreTerms >= 0 && w.CoreTerms <= captureRangeQueueMax && w.Stage >= 0 && w.Stage <= 3 && w.Start >= 0 && w.FirstPart >= 0 && w.FirstPart < parts && w.Metadata >= 0 && w.Metadata <= 128+253 && (w.Stage == 0 && w.Offset >= w.Start && w.Part >= w.FirstPart && w.Part < parts || w.Stage > 0 && w.End > w.Start && w.LastPart >= w.FirstPart && w.LastPart < parts) && (w.Stream == nil || w.Stage == 2 && w.Stream.Valid())
}

func captureIdentifierRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || search.IsCombiningAccent(r) || strings.ContainsRune("_./\\-", r)
}

func (tx *Tx) captureBoundaryIdentifierWork(edition captureEditionRecord, summary capture.CaptureSummary, part capture.CapturePart) (*captureRangeWork, error) {
	if part.Index() == summary.PartCount()-1 {
		return nil, nil
	}
	text := part.Text()
	start := len(text)
	for start > 0 {
		r, n := utf8.DecodeLastRuneInString(text[:start])
		if !captureIdentifierRune(r) {
			break
		}
		start -= n
	}
	if start == len(text) {
		return nil, nil
	}
	if start == 0 && part.Index() > 0 {
		previous, err := tx.store.captureMemberPartReader(tx.writer, edition, summary, part.Index()-1)
		if err != nil {
			return nil, err
		}
		r, _ := utf8.DecodeLastRuneInString(previous.Text())
		if captureIdentifierRune(r) {
			return nil, nil
		}
	}
	next, err := tx.store.captureMemberPartReader(tx.writer, edition, summary, part.Index()+1)
	if err != nil {
		return nil, err
	}
	r, _ := utf8.DecodeRuneInString(next.Text())
	if !captureIdentifierRune(r) {
		return nil, nil
	}
	return &captureRangeWork{Start: part.Offset() + int64(start), FirstPart: part.Index(), Part: part.Index(), Offset: part.Offset() + int64(start)}, nil
}

func (tx *Tx) advanceCaptureRangeWork(id string, edition *captureEditionRecord, summary capture.CaptureSummary) error {
	w := edition.IndexRangeWork
	if w == nil || w.Stage < 0 || w.Stage > 3 || w.Start < 0 || w.FirstPart < 0 || w.FirstPart >= summary.PartCount() {
		return ErrCorrupt
	}
	if w.Stage == 0 {
		if w.Part < w.FirstPart || w.Part >= summary.PartCount() {
			return ErrCorrupt
		}
		part, err := tx.store.captureMemberPartReader(tx.writer, *edition, summary, w.Part)
		if err != nil {
			return err
		}
		if w.Offset < part.Offset() || w.Offset > part.Offset()+int64(len(part.Text())) {
			return ErrCorrupt
		}
		offset := int(w.Offset - part.Offset())
		for offset < len(part.Text()) {
			r, n := utf8.DecodeRuneInString(part.Text()[offset:])
			if !captureIdentifierRune(r) {
				break
			}
			w.Underscore = w.Underscore || strings.ContainsRune("_./\\-", r)
			offset += n
		}
		w.Offset = part.Offset() + int64(offset)
		w.End = w.Offset
		w.LastPart = w.Part
		if offset == len(part.Text()) && w.Part < summary.PartCount()-1 {
			w.Part++
			return nil
		}
		if !w.Underscore || w.LastPart == w.FirstPart {
			edition.IndexRangeWork = nil
			return nil
		}
		w.Stage = 1
		return nil
	}
	if w.End <= w.Start || w.End > summary.Bytes() || w.LastPart < w.FirstPart || w.LastPart >= summary.PartCount() {
		return ErrCorrupt
	}
	if w.Stage == 1 {
		if err := tx.expandCaptureRangeContext(edition, summary, w); err != nil {
			return err
		}
		mapping := captureIndexPart{Version: captureIndexVersion, EditionID: id, Publication: edition.Publication, Index: w.FirstPart, Bytes: int(w.End - w.Start), Format: captureIndexContentFormat, Range: true, Start: w.Start, End: w.End, LastPart: w.LastPart}
		rangeID := mapping.sourceID()
		existing, found, err := tx.store.captureIndexPartReader(tx.writer, rangeID)
		if err != nil {
			return err
		}
		if found {
			if !existing.Range || existing.EditionID != id || existing.Publication != edition.Publication {
				return ErrCorrupt
			}
		} else {
			head, err := tx.store.captureRangeHead(tx.writer, id, edition.Publication, w.FirstPart)
			if err != nil {
				return err
			}
			mapping.Next = head
			if err := tx.initializeCapturePartIndexLocked(rangeID, 0, mapping); err != nil {
				return err
			}
			if err := tx.capturePutJSONLocked(captureRangeHeadKey(id, edition.Publication, w.FirstPart), rangeID, 128); err != nil {
				return err
			}
		}

		w.Stream = &search.CaptureTokenStream{Next: w.Start, BufferStart: w.Start}
		w.Part = w.FirstPart
		w.Offset = w.Start
		w.Stage = 2
		return nil
	}
	rangeID := capture.CaptureRangeSourceID(id, edition.Publication, w.Start, w.End)
	if w.Stage == 2 {
		if w.Stream == nil || !w.Stream.Valid() || w.Part < w.FirstPart || w.Part > w.LastPart {
			return ErrCorrupt
		}
		part, err := tx.store.captureMemberPartReader(tx.writer, *edition, summary, w.Part)
		if err != nil {
			return err
		}
		a, b := max(w.Start, part.Offset()), min(w.End, part.Offset()+int64(len(part.Text())))
		if w.Offset < a || w.Offset > b || w.Offset != w.Stream.Next {
			return ErrCorrupt
		}
		used, tokens, err := feedCaptureIndexTokens(w.Stream, part.Text()[w.Offset-part.Offset():b-part.Offset()], w.Offset, w.Part, w.Part == w.LastPart, exactIndexBatchTerms)
		if err != nil {
			return ErrCorrupt
		}
		w.Offset += int64(used)
		core, err := tx.captureRangeCore(id, *edition, *w)
		if err != nil {
			return err
		}
		metadata, err := searchFieldTermsLimit("", summary.Header().AppID(), summary.Header().Domain(), "", 128+253)
		if err != nil {
			return err
		}
		metadataDigests := make(map[[32]byte]bool, len(metadata))
		for term := range metadata {
			metadataDigests[search.CaptureTermDigest(term)] = true
		}
		terms := make(map[[32]byte]byte, len(tokens))
		for _, token := range tokens {
			if core == nil || core[token.Digest] || metadataDigests[token.Digest] {
				terms[tx.store.capturePartIndex().captureTermLookupDigest(token.Digest)] |= searchFieldText
			}
		}

		if err := tx.capturePartSearch().appendCaptureDigests(rangeID, summary.Header().AppID(), summary.Header().Domain(), terms); err != nil {
			return err
		}
		if w.Offset == b {
			if w.Part < w.LastPart {
				w.Part++
			} else if w.Stream.Complete {
				w.Stage = 3
				w.Stream = nil
			}
		}
		return nil
	}
	metadata, err := searchFieldTermsLimit("", summary.Header().AppID(), summary.Header().Domain(), "", 128+253)
	if err != nil {
		return err
	}
	ordered := sortedTermKeys(metadata)
	if w.Metadata < 0 || w.Metadata > len(ordered) {
		return ErrCorrupt
	}
	end := min(len(ordered), w.Metadata+exactIndexBatchTerms)
	terms := make(map[[32]byte]byte, end-w.Metadata)
	for _, term := range ordered[w.Metadata:end] {
		terms[tx.store.capturePartIndex().searchLookupDigest(searchIndexTerm, term)] |= metadata[term]
	}
	if err := tx.capturePartSearch().appendCaptureDigests(rangeID, summary.Header().AppID(), summary.Header().Domain(), terms); err != nil {
		return err
	}
	w.Metadata = end
	if end == len(ordered) {
		return tx.finishCaptureRangeWork(id, edition)
	}
	return nil
}

// Neighbor context is optional, bounded original text. When its outer edge
// falls inside a word/run, discard that edge fragment rather than indexing it.
func (tx *Tx) expandCaptureRangeContext(edition *captureEditionRecord, summary capture.CaptureSummary, w *captureRangeWork) error {
	first, err := tx.store.captureMemberPartReader(tx.writer, *edition, summary, w.FirstPart)
	if err != nil {
		return err
	}
	last := first
	if w.LastPart != w.FirstPart {
		last, err = tx.store.captureMemberPartReader(tx.writer, *edition, summary, w.LastPart)
		if err != nil {
			return err
		}
	}
	a, b := int(w.Start-first.Offset()), int(w.End-last.Offset())
	start := max(0, a-128)
	for start < a && !utf8.RuneStart(first.Text()[start]) {
		start++
	}
	if start > 0 {
		for start < a {
			r, n := utf8.DecodeRuneInString(first.Text()[start:])
			if !captureIdentifierRune(r) {
				start += n
				break
			}
			start += n
		}
	}
	for start < a {
		r, n := utf8.DecodeRuneInString(first.Text()[start:])
		if !unicode.IsSpace(r) {
			break
		}
		start += n
	}
	end := min(len(last.Text()), b+512)
	for end > b && end < len(last.Text()) && !utf8.RuneStart(last.Text()[end]) {
		end--
	}
	if end < len(last.Text()) {
		for end > b {
			r, n := utf8.DecodeLastRuneInString(last.Text()[:end])
			if !captureIdentifierRune(r) {
				break
			}
			end -= n
		}
	}
	w.Start = first.Offset() + int64(start)
	w.End = last.Offset() + int64(end)
	return nil
}
