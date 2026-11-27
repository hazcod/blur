package durable

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const replicaCaptureNS = "replica-capture-v1"

type ExportedCapturePart struct {
	EditionID, Publication string
	CapturedAt             time.Time
	Index                  int64
	Summary, Part          []byte
	Revision               [32]byte
}

type ReplicaCaptureExportPage struct {
	Items   []ExportedCapturePart
	Next    string
	HasMore bool
}

func captureReplicaCursor(id, publication string, index int64) string {
	return id + ":" + publication + ":" + strconv.FormatInt(index, 10)
}

func parseCaptureReplicaCursor(value string) (string, string, int64, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 || !validHash(parts[0]) || !validHash(parts[1]) {
		return "", "", 0, &storage.StoreError{Code: storage.Invalid}
	}
	index, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || index < -1 {
		return "", "", 0, &storage.StoreError{Code: storage.Invalid}
	}
	return parts[0], parts[1], index, nil
}

func captureReplicaRevision(editionID, publication string, summary, part []byte) [32]byte {
	h := sha256.New()
	h.Write([]byte("blur.replica.capture-part.v1\x00"))
	h.Write([]byte(editionID))
	h.Write([]byte(publication))
	h.Write(summary)
	h.Write(part)
	var revision [32]byte
	copy(revision[:], h.Sum(nil))
	return revision
}

// CaptureReplicaRevision binds an exported part to its origin edition,
// publication, authenticated summary, and authenticated part bytes.
func CaptureReplicaRevision(editionID, publication string, summary, part []byte) [32]byte {
	return captureReplicaRevision(editionID, publication, summary, part)
}

func captureReplicaPartID(editionID, publication string, index int64) string {
	return capturePartSourceID(editionID, publication, index)
}

func captureReplicaReceiptKey(generation, sourceID string) string {
	return "receipt:" + generation + ":" + sourceID
}

func captureReplicaImportedKey(editionID, publication string) string {
	return "imported:" + editionID + ":" + publication
}

func (s *Store) ExportReplicaCaptureParts(ctx context.Context, generation, after string, maxCount, maxBytes int) (page ReplicaCaptureExportPage, err error) {
	if ctx == nil || !validReplicaGeneration(generation) || maxCount < 1 || maxCount > 128 || maxBytes < 1 || maxBytes > 12<<20 {
		return page, &storage.StoreError{Code: storage.Invalid}
	}
	var edition CaptureEdition
	var index int64
	if after != "" {
		id, publication, cursorIndex, err := parseCaptureReplicaCursor(after)
		if err != nil {
			return page, err
		}
		if cursorIndex == -1 {
			list, err := s.ListCaptures(ctx, id+":"+publication, 1)
			if err != nil || len(list.Editions) == 0 {
				return page, err
			}
			edition = list.Editions[0]
			index = 0
		} else {
			var found bool
			edition, found, err = s.GetCapture(ctx, id)
			if err != nil {
				return page, err
			}
			if !found || edition.Publication != publication {
				return page, ErrCaptureCursorChanged
			}
			index = cursorIndex
		}
	} else {
		list, err := s.ListCaptures(ctx, "", 1)
		if err != nil || len(list.Editions) == 0 {
			return page, err
		}
		edition = list.Editions[0]
	}
	if edition.Summary.PartCount() == 0 || index >= edition.Summary.PartCount() {
		return page, ErrCorrupt
	}
	start := index
	usedBytes := 0
	for len(page.Items) < maxCount && start < edition.Summary.PartCount() {
		count := min(maxCount-len(page.Items), int(edition.Summary.PartCount()-start), storage.MaxCapturePartsPageRows)
		parts, err := s.ReadCapturePartsPage(ctx, storage.CapturePartsPageRequest{EditionID: edition.Summary.ContentID(), Publication: edition.Publication, After: start - 1, Limit: count})
		if err != nil {
			return ReplicaCaptureExportPage{}, err
		}
		summaryBytes, err := capture.MarshalAuthenticatedCaptureSummary(edition.Summary)
		if err != nil {
			return ReplicaCaptureExportPage{}, ErrCorrupt
		}
		for _, part := range parts.Parts {
			encoded, err := capture.MarshalAuthenticatedCapturePart(part)
			if err != nil {
				clear(summaryBytes)
				return ReplicaCaptureExportPage{}, ErrCorrupt
			}
			itemBytes := len(summaryBytes) + len(encoded) + 256
			if len(page.Items) > 0 && usedBytes+itemBytes > maxBytes {
				clear(encoded)
				page.HasMore = true
				break
			}
			item := ExportedCapturePart{EditionID: edition.Summary.ContentID(), Publication: edition.Publication, CapturedAt: edition.Summary.Header().CapturedAt(), Index: part.Index(), Summary: bytes.Clone(summaryBytes), Part: encoded}
			item.Revision = captureReplicaRevision(item.EditionID, item.Publication, item.Summary, item.Part)
			page.Items = append(page.Items, item)
			usedBytes += itemBytes
			start = part.Index() + 1
		}
		clear(summaryBytes)
		if page.HasMore || len(parts.Parts) == 0 || !parts.HasMore {
			break
		}
	}
	if len(page.Items) == 0 {
		return page, nil
	}
	last := page.Items[len(page.Items)-1]
	if last.Index+1 < edition.Summary.PartCount() {
		page.Next = captureReplicaCursor(last.EditionID, last.Publication, last.Index+1)
		page.HasMore = true
	} else {
		page.Next = captureReplicaCursor(last.EditionID, last.Publication, -1)
		list, err := s.ListCaptures(ctx, last.EditionID+":"+last.Publication, 1)
		if err != nil {
			return ReplicaCaptureExportPage{}, err
		}
		page.HasMore = len(list.Editions) > 0
		if !page.HasMore {
			page.Next = ""
		}
	}
	return page, nil
}

func ClearReplicaCapturePage(page *ReplicaCaptureExportPage) {
	for i := range page.Items {
		clear(page.Items[i].Summary)
		clear(page.Items[i].Part)
	}
}

// MarkReplicaCaptureImported prevents relaying a peer's capture as locally
// originated content while leaving it searchable and erasable on this device.
func (s *Store) MarkReplicaCaptureImported(ctx context.Context, editionID, publication string) error {
	if ctx == nil || !validHash(editionID) || !validHash(publication) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	return s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		return tx.putIndexValueLocked(replicaCaptureNS, captureReplicaImportedKey(editionID, publication), []byte{1})
	})
}

func (s *Store) ReplicaCaptureImported(ctx context.Context, editionID, publication string) (bool, error) {
	if ctx == nil || !validHash(editionID) || !validHash(publication) {
		return false, &storage.StoreError{Code: storage.Invalid}
	}
	value, found, err := s.readIndexValue(ctx, replicaCaptureNS, captureReplicaImportedKey(editionID, publication), 8)
	defer clear(value)
	if err == nil && found && !bytes.Equal(value, []byte{1}) {
		err = ErrCorrupt
	}
	return found, err
}

func (s *Store) AcknowledgeReplicaCaptureExport(ctx context.Context, generation string, items []ExportedCapturePart, importedOnly bool) error {
	if ctx == nil || !validReplicaGeneration(generation) || len(items) > 1<<16 {
		return &storage.StoreError{Code: storage.Invalid}
	}
	if importedOnly {
		for _, item := range items {
			if err := s.MarkReplicaCaptureImported(ctx, item.EditionID, item.Publication); err != nil {
				return err
			}
		}
	}
	for start := 0; start < len(items); start += 128 {
		end := min(start+128, len(items))
		if err := s.Update(ctx, func(tx *Tx) error {
			tx.mu.Lock()
			defer tx.mu.Unlock()
			for _, item := range items[start:end] {
				if !validHash(item.EditionID) || !validHash(item.Publication) || item.Index < 0 || captureReplicaRevision(item.EditionID, item.Publication, item.Summary, item.Part) != item.Revision {
					return &storage.StoreError{Code: storage.Invalid}
				}
				value := append([]byte(nil), item.Revision[:]...)
				if err := tx.putIndexValueLocked(replicaCaptureNS, captureReplicaReceiptKey(generation, captureReplicaPartID(item.EditionID, item.Publication, item.Index)), value); err != nil {
					clear(value)
					return err
				}
				clear(value)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CurrentReplicaCaptureParts(ctx context.Context, items []ExportedCapturePart) ([]bool, error) {
	if ctx == nil || len(items) > 1<<16 {
		return nil, &storage.StoreError{Code: storage.Invalid}
	}
	current := make([]bool, len(items))
	for i, item := range items {
		edition, found, err := s.GetCapture(ctx, item.EditionID)
		if err != nil {
			return nil, err
		}
		if !found || edition.Publication != item.Publication || !edition.Summary.Header().CapturedAt().Equal(item.CapturedAt) {
			continue
		}
		summary, err := capture.MarshalAuthenticatedCaptureSummary(edition.Summary)
		if err != nil {
			return nil, ErrCorrupt
		}
		if !bytes.Equal(summary, item.Summary) {
			clear(summary)
			continue
		}
		clear(summary)
		part, err := capture.RestoreAuthenticatedCapturePart(item.Part)
		if err != nil || part.Index() != item.Index {
			return nil, &storage.StoreError{Code: storage.Invalid}
		}
		page, err := s.ReadCapturePartsPage(ctx, storage.CapturePartsPageRequest{EditionID: item.EditionID, Publication: item.Publication, After: item.Index - 1, Limit: 1})
		if err != nil {
			if errors.Is(err, ErrMissing) || errors.Is(err, ErrCaptureCursorChanged) {
				continue
			}
			return nil, err
		}
		if len(page.Parts) != 1 {
			continue
		}
		encoded, err := capture.MarshalAuthenticatedCapturePart(page.Parts[0])
		if err != nil {
			return nil, ErrCorrupt
		}
		current[i] = bytes.Equal(encoded, item.Part) && captureReplicaRevision(item.EditionID, item.Publication, item.Summary, encoded) == item.Revision
		clear(encoded)
	}
	return current, nil
}

func (s *Store) ExportedReplicaCaptureParts(ctx context.Context, generation string, items []ExportedCapturePart) ([]bool, error) {
	if ctx == nil || !validReplicaGeneration(generation) || len(items) > 1<<16 {
		return nil, &storage.StoreError{Code: storage.Invalid}
	}
	flags := make([]bool, len(items))
	err := s.view(ctx, func(reader bolt.Reader) error {
		for i, item := range items {
			id := captureReplicaReceiptKey(generation, captureReplicaPartID(item.EditionID, item.Publication, item.Index))
			value, found, err := s.readIndexValueReader(reader, replicaCaptureNS, id, 32)
			if err != nil {
				return err
			}
			if found && len(value) != 32 {
				clear(value)
				return ErrCorrupt
			}
			flags[i] = found && bytes.Equal(value, item.Revision[:])
			clear(value)
		}
		return nil
	})
	return flags, err
}

func validReplicaGeneration(value string) bool { _, ok := replicaGeneration(value); return ok }
