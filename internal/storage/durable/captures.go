package durable

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"math"
	"time"
)

var ErrCaptureConflict = errors.New("durable store: conflicting capture progress")
var ErrCaptureCursorChanged = storage.ErrCaptureCursorChanged
var ErrCaptureClosed = errors.New("durable store: capture closed")

type CaptureEdition struct {
	Summary       capture.CaptureSummary
	Publication   string
	LastSeen      time.Time
	Visits        int64
	firstSourceID string
	created       bool
}
type CapturePartPage struct {
	Parts   []capture.CapturePart
	Next    int64
	HasMore bool
}
type CapturePage struct {
	Editions []CaptureEdition
	Next     string
	HasMore  bool
}
type CaptureVisit struct {
	CaptureID  string
	CapturedAt time.Time
}
type CaptureVisitPage struct {
	Visits  []CaptureVisit
	Next    string
	HasMore bool
}

func (s *Store) captureUpdate(ctx context.Context, fn func(*Tx) error) error {
	return s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		if err := tx.checkLocked(); err != nil {
			return err
		}
		return fn(tx)
	})
}
func captureEditionValue(r captureEditionRecord, summary capture.CaptureSummary) CaptureEdition {
	return CaptureEdition{Summary: summary, Publication: r.Publication, LastSeen: time.Unix(0, r.LastSeen).UTC(), Visits: r.Visits, firstSourceID: capturePartSourceID(summary.ContentID(), r.Publication, 0)}
}

func (s *Store) BeginCapture(ctx context.Context, header capture.CaptureHeader) error {
	if header.ValidateForAdmission() != nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	encoded, err := capture.MarshalAuthenticatedCaptureHeader(header)
	if err != nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	return s.captureUpdate(ctx, func(tx *Tx) error {
		if err := tx.checkCaptureAdmissionLocked(header); err != nil {
			return err
		}
		if _, found, err := s.captureReceiptReader(tx.writer, header.CaptureID()); err != nil {
			return err
		} else if found {
			return ErrCaptureClosed
		}
		stage, _, found, err := s.captureStageReader(tx.writer, header.CaptureID())
		if err != nil {
			return err
		}
		if found {
			if !bytes.Equal(stage.Header, encoded) {
				return ErrCaptureConflict
			}
			if stage.PublishedID != "" || stage.Cleaning {
				return ErrCaptureClosed
			}
			return nil
		}
		return tx.captureLinkStageLocked(header.CaptureID(), captureStageRecord{Version: 1, Header: encoded, Root: header.InitialRoot()})
	})
}
func (s *Store) AppendCaptureParts(ctx context.Context, header capture.CaptureHeader, parts []capture.CapturePart) error {
	if ctx == nil || header.ValidateForAdmission() != nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	headerBytes, err := capture.MarshalAuthenticatedCaptureHeader(header)
	if err != nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	if len(parts) == 0 {
		return s.captureUpdate(ctx, func(tx *Tx) error { _, err := tx.captureOpenStageLocked(header, headerBytes); return err })
	}
	for _, part := range parts {
		if part.ValidateForAdmission() != nil {
			return &storage.StoreError{Code: storage.Invalid}
		}
		if part.CaptureID() != header.CaptureID() {
			return ErrCaptureConflict
		}
		encoded, err := capture.MarshalAuthenticatedCapturePart(part)
		if err != nil {
			return &storage.StoreError{Code: storage.Invalid}
		}
		err = s.captureUpdate(ctx, func(tx *Tx) error {
			stage, err := tx.captureOpenStageLocked(header, headerBytes)
			if err != nil {
				return err
			}
			if part.Index() > stage.Parts {
				return ErrCaptureConflict
			}
			if part.Index() < stage.Parts {
				stored, _, err := s.capturePartReader(tx.writer, header.CaptureID(), part.Index())
				if err != nil {
					return err
				}
				if !bytes.Equal(stored.Part, encoded) {
					return ErrCaptureConflict
				}
				return nil
			}
			if part.Offset() != stage.Bytes || stage.Parts == math.MaxInt64 || stage.Bytes > math.MaxInt64-int64(len(part.Text())) {
				return ErrCaptureConflict
			}
			key := s.logicalKey(groupRecord, kindIndex, "index:"+captureNS, capturePartKey(header.CaptureID(), part.Index()))
			exists, checkErr := tx.writer.Has(key)
			clear(key)
			if checkErr != nil {
				return mapStorePhysicalError(checkErr)
			}
			if exists {
				return ErrCorrupt
			}
			after, err := capture.AdvanceCaptureRoot(stage.Root, part)
			if err != nil {
				return ErrCaptureConflict
			}
			stored := captureStoredPart{Version: 1, Before: stage.Root, After: after, Part: encoded}
			if err := tx.capturePutJSONLocked(capturePartKey(header.CaptureID(), part.Index()), stored, captureStoredPartBytes); err != nil {
				return err
			}
			stage.Root = after
			stage.Parts++
			stage.Bytes += int64(len(part.Text()))
			for _, span := range part.Origins() {
				stage.MaxInput = max(stage.MaxInput, span.InputEnd)
			}
			return tx.capturePutJSONLocked("stage:"+header.CaptureID(), stage, captureMetaBytes)
		})
		clear(encoded)
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

// PublishCapture commits the edition and visit before draining a duplicate
// payload in bounded transactions. A nonzero result with an error means the
// publication committed but cleanup needs retry; retrying the same summary is
// idempotent and resumes cleanup without adding another visit.
func (s *Store) PublishCapture(ctx context.Context, summary capture.CaptureSummary) (CaptureEdition, error) {
	return s.publishCaptureAndCleanup(ctx, summary, nil)
}

func (s *Store) publishCaptureAndCleanup(ctx context.Context, summary capture.CaptureSummary, afterCommit func()) (CaptureEdition, error) {
	result, err := s.publishCaptureWithHook(ctx, summary, nil)
	if err != nil {
		return CaptureEdition{}, err
	}
	if afterCommit != nil {
		afterCommit()
	}
	if result.Summary.Header().CaptureID() == summary.Header().CaptureID() {
		return result, nil
	}
	return result, s.DiscardCaptureStage(ctx, summary.Header().CaptureID())
}

func (s *Store) publishCaptureWithHook(ctx context.Context, summary capture.CaptureSummary, afterWrites func() error) (CaptureEdition, error) {
	return s.publishCaptureWithPublicationHook(ctx, summary, "", afterWrites)
}

// PublishCaptureWithPublication imports an authenticated remote publication
// while preserving its publication-scoped source identities. Replays of the
// same edition/publication are idempotent; a conflicting publication fails.
func (s *Store) PublishCaptureWithPublication(ctx context.Context, summary capture.CaptureSummary, publication string) (CaptureEdition, error) {
	if !validHash(publication) {
		return CaptureEdition{}, &storage.StoreError{Code: storage.Invalid}
	}
	return s.publishCaptureWithPublicationHook(ctx, summary, publication, nil)
}

func (s *Store) publishCaptureWithPublicationHook(ctx context.Context, summary capture.CaptureSummary, publication string, afterWrites func() error) (CaptureEdition, error) {
	var result CaptureEdition
	if summary.ValidateForAdmission() != nil || publication != "" && !validHash(publication) {
		return result, &storage.StoreError{Code: storage.Invalid}
	}
	header := summary.Header()
	headerBytes, _ := capture.MarshalAuthenticatedCaptureHeader(header)
	summaryBytes, _ := capture.MarshalAuthenticatedCaptureSummary(summary)
	err := s.captureUpdate(ctx, func(tx *Tx) error {
		if err := tx.checkCaptureAdmissionLocked(header); err != nil {
			return err
		}
		if err := tx.checkSourceAdmissionLocked(summary.ContentID(), header.CapturedAt()); err != nil {
			return err
		}
		if receipt, found, err := s.captureReceiptReader(tx.writer, header.CaptureID()); err != nil {
			return err
		} else if found {
			if receipt.EditionID != summary.ContentID() || receipt.FamilyID != header.FamilyID() || receipt.At != header.CapturedAt().UnixNano() {
				return ErrCaptureConflict
			}
			r, stored, found, err := s.captureEditionReader(tx.writer, receipt.EditionID)
			if err != nil {
				return err
			}
			if !found {
				return ErrCorrupt
			}
			if publication != "" && r.Publication != publication {
				return ErrCaptureConflict
			}
			if err := tx.ensureCaptureFirstSourceLocked(receipt.EditionID, r, stored); err != nil {
				return err
			}
			result = captureEditionValue(r, stored)
			return nil
		}
		stage, _, found, err := s.captureStageReader(tx.writer, header.CaptureID())
		if err != nil {
			return err
		}
		if found && stage.Cleaning {
			return ErrCaptureClosed
		}
		if !found || stage.PublishedID != "" || !bytes.Equal(stage.Header, headerBytes) || stage.Parts != summary.PartCount() || stage.Bytes != summary.Bytes() || stage.Root != summary.Root() || stage.MaxInput > summary.RawBytes() {
			return ErrCaptureConflict
		}
		r, stored, exists, err := s.captureEditionReader(tx.writer, summary.ContentID())
		if err != nil {
			return err
		}
		if !exists {
			r = captureEditionRecord{Version: 1, IndexVersion: captureIndexVersion, Summary: summaryBytes, Owner: header.CaptureID(), LastSeen: header.CapturedAt().UnixNano()}
			if publication == "" {
				var generated [32]byte
				if _, err := rand.Read(generated[:]); err != nil {
					return ErrTransaction
				}
				r.Publication = hex.EncodeToString(generated[:])
				clear(generated[:])
			} else {
				r.Publication = publication
			}
			if err := tx.captureLinkEditionLocked(summary.ContentID(), header.FamilyID(), &r); err != nil {
				return err
			}
			stored, err = capture.RestoreAuthenticatedCaptureSummary(summaryBytes)
			if err != nil {
				return ErrCorrupt
			}
		}
		if publication != "" && r.Publication != publication {
			return ErrCaptureConflict
		}
		if err := tx.captureAppendVisitLocked(summary.ContentID(), header, &r); err != nil {
			return err
		}
		if err := tx.queueCaptureIndexLocked(summary.ContentID(), &r, summary.PartCount()); err != nil {
			return err
		}
		if err := tx.capturePutJSONLocked("edition:"+summary.ContentID(), r, captureMetaBytes); err != nil {
			return err
		}
		if err := tx.ensureCaptureFirstSourceLocked(summary.ContentID(), r, stored); err != nil {
			return err
		}
		if afterWrites != nil {
			if err := afterWrites(); err != nil {
				return err
			}
		}
		if exists {
			stage.PublishedID = summary.ContentID()
			if err := tx.capturePutJSONLocked("stage:"+header.CaptureID(), stage, captureMetaBytes); err != nil {
				return err
			}
		} else {
			if err := tx.captureUnlinkStageLocked(header.CaptureID(), stage); err != nil {
				return err
			}
		}
		result = captureEditionValue(r, stored)
		result.created = !exists
		return nil
	})
	if err != nil {
		return CaptureEdition{}, err
	}
	return result, nil
}

func (s *Store) GetCapture(ctx context.Context, id string) (CaptureEdition, bool, error) {
	if !validHash(id) {
		return CaptureEdition{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	var result CaptureEdition
	found := false
	err := s.view(ctx, func(reader bolt.Reader) error {
		r, summary, live, err := s.captureLiveReader(reader, id)
		if err != nil {
			return err
		}
		found = live
		if live {
			result = captureEditionValue(r, summary)
		}
		return nil
	})
	if err != nil {
		return CaptureEdition{}, false, err
	}
	return result, found, nil
}
func (s *Store) capturePartReader(reader bolt.Reader, nonce string, index int64) (captureStoredPart, capture.CapturePart, error) {
	var r captureStoredPart
	found, err := s.captureReadJSON(reader, capturePartKey(nonce, index), captureStoredPartBytes, &r)
	if err != nil {
		return r, capture.CapturePart{}, err
	}
	if !found || r.Version != 1 || !validHash(r.Before) || !validHash(r.After) {
		return r, capture.CapturePart{}, ErrCorrupt
	}
	p, err := capture.RestoreAuthenticatedCapturePart(r.Part)
	if err != nil || p.CaptureID() != nonce || p.Index() != index {
		return r, p, ErrCorrupt
	}
	after, err := capture.AdvanceCaptureRoot(r.Before, p)
	if err != nil || after != r.After {
		return r, p, ErrCorrupt
	}
	return r, p, nil
}
func (s *Store) ReadCaptureParts(ctx context.Context, id string, after int64, limit int) (CapturePartPage, error) {
	return s.readCaptureParts(ctx, id, "", after, limit)
}

// ReadCapturePartsPage adds publication binding for long-running readers. A
// continuation cannot silently switch to a replacement publication.
func (s *Store) ReadCapturePartsPage(ctx context.Context, request storage.CapturePartsPageRequest) (storage.CapturePartsPage, error) {
	if !storage.ValidCapturePartsPage(request) {
		return storage.CapturePartsPage{}, &storage.StoreError{Code: storage.Invalid}
	}
	page, err := s.readCaptureParts(ctx, request.EditionID, request.Publication, request.After, request.Limit)
	if err != nil {
		return storage.CapturePartsPage{}, err
	}
	return storage.CapturePartsPage{Parts: page.Parts, Next: page.Next, HasMore: page.HasMore}, nil
}

func (s *Store) readCaptureParts(ctx context.Context, id, publication string, after int64, limit int) (CapturePartPage, error) {
	page := CapturePartPage{Next: after}
	if !validHash(id) || after < -1 || limit < 1 || limit > storage.MaxCapturePartsPageRows {
		return page, &storage.StoreError{Code: storage.Invalid}
	}
	err := s.view(ctx, func(reader bolt.Reader) error {
		r, summary, found, err := s.captureLiveReader(reader, id)
		if err != nil {
			return err
		}
		if !found {
			return ErrMissing
		}
		if publication != "" && r.Publication != publication {
			return storage.ErrCaptureCursorChanged
		}
		if after >= summary.PartCount() {
			return &storage.StoreError{Code: storage.Invalid}
		}
		index := after + 1
		root := summary.Header().InitialRoot()
		offset := int64(0)
		if index > 0 && index < summary.PartCount() {
			prev, p, err := s.capturePartReader(reader, r.Owner, index-1)
			if err != nil {
				return err
			}
			root = prev.After
			offset = p.Offset() + int64(len(p.Text()))
		}
		for ; index < summary.PartCount() && len(page.Parts) < limit; index++ {
			stored, p, err := s.capturePartReader(reader, r.Owner, index)
			if err != nil {
				return err
			}
			if p.Offset() != offset || stored.Before != root {
				return ErrCorrupt
			}
			for _, span := range p.Origins() {
				if span.InputEnd > summary.RawBytes() {
					return ErrCorrupt
				}
			}
			offset += int64(len(p.Text()))
			root = stored.After
			if offset > summary.Bytes() || index == summary.PartCount()-1 && (offset != summary.Bytes() || root != summary.Root()) {
				return ErrCorrupt
			}
			page.Parts = append(page.Parts, p)
			page.Next = index
		}
		page.HasMore = index < summary.PartCount()
		return nil
	})
	if err != nil {
		return CapturePartPage{}, err
	}
	return page, nil
}
func (s *Store) ListCaptures(ctx context.Context, after string, limit int) (CapturePage, error) {
	page := CapturePage{Next: after}
	if limit < 1 || limit > 32 {
		return page, &storage.StoreError{Code: storage.Invalid}
	}
	afterID, publication := "", ""
	if after != "" {
		if len(after) != 129 || after[64] != ':' || !validHash(after[:64]) || !validHash(after[65:]) {
			if validHash(after) {
				return CapturePage{}, ErrCaptureCursorChanged
			}
			return CapturePage{}, &storage.StoreError{Code: storage.Invalid}
		}
		afterID, publication = after[:64], after[65:]
	}
	err := s.view(ctx, func(reader bolt.Reader) error {
		root, err := s.captureCatalogReader(reader, "published")
		if err != nil {
			return err
		}
		next := root.Head
		previous := ""
		if after != "" {
			r, _, found, err := s.captureEditionReader(reader, afterID)
			if err != nil {
				return err
			}
			if !found || r.Publication != publication {
				return ErrCaptureCursorChanged
			}
			next = r.Next
			previous = afterID
		}
		seen := map[string]bool{}
		for scanned := 0; next != "" && scanned < 32 && len(page.Editions) < limit; scanned++ {
			if seen[next] {
				return ErrCorrupt
			}
			seen[next] = true
			r, summary, found, err := s.captureEditionReader(reader, next)
			if err != nil {
				return err
			}
			if !found || r.Prev != previous {
				return ErrCorrupt
			}
			_, _, live, err := s.captureLiveReader(reader, next)
			if err != nil {
				return err
			}
			if live {
				page.Editions = append(page.Editions, captureEditionValue(r, summary))
			}
			page.Next = next + ":" + r.Publication
			previous = next
			next = r.Next
			if next == "" && previous != root.Tail {
				return ErrCorrupt
			}
		}
		page.HasMore = next != ""
		return nil
	})
	if err != nil {
		return CapturePage{}, err
	}
	return page, nil
}
