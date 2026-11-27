package replica

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage/durable"
)

type pendingCaptureExport struct {
	segment []byte
	cursor  string
	records []Record
	items   []durable.ExportedCapturePart
}

func (p *pendingCaptureExport) clear() {
	clear(p.segment)
	clearRecords(p.records)
	for i := range p.items {
		clear(p.items[i].Summary)
		clear(p.items[i].Part)
	}
}

func captureRecords(items []durable.ExportedCapturePart) ([]Record, error) {
	var records []Record
	for _, item := range items {
		summary, err := capture.RestoreAuthenticatedCaptureSummary(item.Summary)
		if err != nil || summary.ContentID() != item.EditionID || !summary.Header().CapturedAt().Equal(item.CapturedAt) {
			clearRecords(records)
			return nil, ErrFormat
		}
		part, err := capture.RestoreAuthenticatedCapturePart(item.Part)
		if err != nil || part.Index() != item.Index || part.CaptureID() != summary.Header().CaptureID() {
			clearRecords(records)
			return nil, ErrFormat
		}
		records = append(records,
			Record{Kind: KindCaptureManifest, At: item.CapturedAt, EditionID: item.EditionID, Publication: item.Publication, Summary: bytes.Clone(item.Summary)},
			Record{Kind: KindCapturePart, At: item.CapturedAt, EditionID: item.EditionID, Publication: item.Publication, Part: bytes.Clone(item.Part)},
		)
	}
	return records, nil
}

func captureItems(records []Record) ([]durable.ExportedCapturePart, error) {
	if len(records)%2 != 0 {
		return nil, ErrFormat
	}
	var items []durable.ExportedCapturePart
	previousEdition, previousPublication := "", ""
	previousIndex, previousEnd := int64(-2), int64(-1)
	for i := 0; i < len(records); i += 2 {
		manifest, partRecord := records[i], records[i+1]
		if manifest.Kind != KindCaptureManifest || partRecord.Kind != KindCapturePart || manifest.EditionID != partRecord.EditionID || manifest.Publication != partRecord.Publication || !manifest.At.Equal(partRecord.At) {
			return nil, ErrFormat
		}
		summary, err := capture.RestoreAuthenticatedCaptureSummary(manifest.Summary)
		if err != nil || summary.ContentID() != manifest.EditionID || !summary.Header().CapturedAt().Equal(manifest.At) {
			return nil, ErrFormat
		}
		part, err := capture.RestoreAuthenticatedCapturePart(partRecord.Part)
		if err != nil || part.CaptureID() != summary.Header().CaptureID() || part.Index() >= summary.PartCount() {
			return nil, ErrFormat
		}
		if i == 0 {
			if part.Index() == 0 && part.Offset() != 0 {
				return nil, ErrFormat
			}
		} else if manifest.EditionID != previousEdition || manifest.Publication != previousPublication || part.Index() != previousIndex+1 || part.Offset() != previousEnd {
			return nil, ErrFormat
		}
		end := part.Offset() + int64(len(part.Text()))
		if part.Index()+1 == summary.PartCount() && end != summary.Bytes() {
			return nil, ErrFormat
		}
		item := durable.ExportedCapturePart{EditionID: manifest.EditionID, Publication: manifest.Publication, CapturedAt: manifest.At, Index: part.Index(), Summary: bytes.Clone(manifest.Summary), Part: bytes.Clone(partRecord.Part)}
		item.Revision = capturePartRevision(item.EditionID, item.Publication, item.Summary, item.Part)
		items = append(items, item)
		previousEdition, previousPublication, previousIndex, previousEnd = manifest.EditionID, manifest.Publication, part.Index(), end
	}
	return items, nil
}

func capturePartRevision(editionID, publication string, summary, part []byte) [32]byte {
	return durable.CaptureReplicaRevision(editionID, publication, summary, part)
}

func encodeCapturePending(key []byte, p pendingCaptureExport) ([]byte, error) {
	if len(p.segment) > MaxSegmentPlaintext+headerSize+64 || len(p.cursor) > 256 {
		return nil, ErrFormat
	}
	plain := make([]byte, 9, 9+len(p.cursor)+len(p.segment))
	plain[0] = 1
	plain[1] = byte(len(p.cursor) >> 8)
	plain[2] = byte(len(p.cursor))
	plain[3] = byte(len(p.segment) >> 24)
	plain[4] = byte(len(p.segment) >> 16)
	plain[5] = byte(len(p.segment) >> 8)
	plain[6] = byte(len(p.segment))
	plain[7] = 0
	plain[8] = 0
	plain = append(plain, p.cursor...)
	plain = append(plain, p.segment...)
	defer clear(plain)
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, []byte("blur-replica-capture-outbox-v1")), nil
}

func decodeCapturePending(key, sealed []byte, device string, sequence uint64) (pendingCaptureExport, error) {
	var p pendingCaptureExport
	gcm, err := newGCM(key)
	if err != nil {
		return p, err
	}
	if len(sealed) < gcm.NonceSize()+gcm.Overhead() {
		return p, ErrFormat
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], []byte("blur-replica-capture-outbox-v1"))
	if err != nil {
		return p, ErrAuth
	}
	defer clear(plain)
	if len(plain) < 9 || plain[0] != 1 || plain[7] != 0 || plain[8] != 0 {
		return p, ErrFormat
	}
	cursorSize := int(plain[1])<<8 | int(plain[2])
	segmentSize := int(plain[3])<<24 | int(plain[4])<<16 | int(plain[5])<<8 | int(plain[6])
	if segmentSize > MaxSegmentPlaintext+headerSize+64 || cursorSize > 256 || 9+cursorSize+segmentSize != len(plain) {
		return p, ErrFormat
	}
	p.cursor = string(plain[9 : 9+cursorSize])
	p.segment = bytes.Clone(plain[9+cursorSize:])
	header, records, err := OpenCaptureSegment(key, p.segment)
	if err != nil || FormatDeviceID(header.DeviceID) != device || header.Seq != sequence {
		p.clear()
		if err != nil {
			return pendingCaptureExport{}, err
		}
		return pendingCaptureExport{}, ErrFormat
	}
	p.records = records
	p.items, err = captureItems(records)
	if err != nil {
		p.clear()
		return pendingCaptureExport{}, err
	}
	return p, nil
}

func (w *Writer) flushCaptures(ctx context.Context, st State, target Target, current func() bool) (bool, error) {
	exporter, ok := w.Store.(CaptureExporter)
	if !ok {
		return false, nil
	}
	page, err := exporter.ExportReplicaCaptureParts(ctx, st.DeviceID, st.CaptureCursor, 128, 12<<20)
	if errors.Is(err, durable.ErrCaptureCursorChanged) {
		err = w.State.updateCurrent(ctx, st, func(s *State) { s.CaptureCursor = "" })
		return err == nil, err
	}
	if err != nil {
		return false, err
	}
	defer durable.ClearReplicaCapturePage(&page)
	if page.Next != "" && len(page.Items) == 0 {
		return false, ErrFormat
	}
	acked, err := exporter.ExportedReplicaCaptureParts(ctx, st.DeviceID, page.Items)
	if err != nil {
		return false, err
	}
	var selected []durable.ExportedCapturePart
	var skipped []durable.ExportedCapturePart
	for i, item := range page.Items {
		if acked[i] {
			skipped = append(skipped, item)
			continue
		}
		imported, err := exporter.ReplicaCaptureImported(ctx, item.EditionID, item.Publication)
		if err != nil {
			return false, err
		}
		if imported {
			skipped = append(skipped, item)
			continue
		}
		selected = append(selected, item)
	}
	if len(skipped) > 0 {
		local := make([]durable.ExportedCapturePart, 0, len(skipped))
		for _, item := range skipped {
			imported, err := exporter.ReplicaCaptureImported(ctx, item.EditionID, item.Publication)
			if err != nil {
				return false, err
			}
			if imported {
				local = append(local, item)
			}
		}
		if len(local) > 0 {
			if err := w.State.withCurrent(ctx, st, func() error { return exporter.AcknowledgeReplicaCaptureExport(ctx, st.DeviceID, local, true) }); err != nil {
				return false, err
			}
		}
	}
	if len(selected) == 0 {
		err = w.State.updateCurrent(ctx, st, func(s *State) { s.CaptureCursor = page.Next })
		return page.HasMore && err == nil, err
	}
	if st.CaptureLastSeq >= 999999999999 {
		return false, ErrFormat
	}
	device, _ := ParseDeviceID(st.DeviceID)
	records, err := captureRecords(selected)
	if err != nil {
		return false, err
	}
	p := pendingCaptureExport{records: records, items: cloneCaptureItems(selected), cursor: page.Next}
	p.segment, err = SealCaptureSegment(target.Key, device, st.CaptureLastSeq+1, records, nil)
	if err != nil {
		p.clear()
		return false, err
	}
	defer p.clear()
	if !current() {
		return false, ErrMemoryChanging
	}
	if err := w.stageCapturePending(ctx, st, target.Key, st.CaptureLastSeq+1, p); err != nil {
		return false, err
	}
	return w.replayCapturePending(ctx, w.State.Get(), target, current)
}

func cloneCaptureItems(items []durable.ExportedCapturePart) []durable.ExportedCapturePart {
	cloned := make([]durable.ExportedCapturePart, len(items))
	for i, item := range items {
		cloned[i] = item
		cloned[i].Summary = bytes.Clone(item.Summary)
		cloned[i].Part = bytes.Clone(item.Part)
	}
	return cloned
}

func (w *Writer) stageCapturePending(ctx context.Context, st State, key []byte, sequence uint64, p pendingCaptureExport) error {
	sealed, err := encodeCapturePending(key, p)
	if err != nil {
		return err
	}
	defer clear(sealed)
	return w.State.withCurrent(ctx, st, func() error {
		dir := filepath.Dir(w.State.path)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		hash := outboxHash(sealed)
		path := w.State.captureOutboxPath(st.DeviceID, hash)
		if err := writeFileAtomic(dir, filepath.Base(path), sealed); err != nil {
			return err
		}
		next := cloneState(w.State.st)
		next.CapturePending = &PendingState{Sequence: sequence, Hash: hash}
		if err := writeStateFile(w.State.path, next); err != nil {
			return err
		}
		w.State.st = next
		return nil
	})
}

func (w *Writer) replayCapturePending(ctx context.Context, st State, target Target, current func() bool) (bool, error) {
	if st.CapturePending == nil || st.CapturePending.Sequence != st.CaptureLastSeq+1 {
		return false, ErrFormat
	}
	sealed, err := readBoundedFile(w.State.captureOutboxPath(st.DeviceID, st.CapturePending.Hash), int64(MaxSegmentPlaintext+headerSize+512))
	if err != nil {
		if !os.IsNotExist(err) {
			return false, err
		}
		if st.ConfigVersion == ^uint64(0) {
			return false, ErrFormat
		}
		id, generationErr := NewDeviceID()
		if generationErr != nil {
			return false, generationErr
		}
		err = w.State.updateCurrent(ctx, st, func(s *State) {
			s.DeviceID, s.Pending, s.CapturePending = id, nil, nil
			s.LastSeq, s.CaptureLastSeq, s.TombSequence = 0, 0, 0
			s.ObsWatermark, s.TombWatermark = 0, 0
			s.ObservationCursor, s.CaptureCursor = nil, ""
			s.ConfigVersion++
		})
		return err == nil, err
	}
	defer clear(sealed)
	if outboxHash(sealed) != st.CapturePending.Hash {
		return false, ErrAuth
	}
	p, err := decodeCapturePending(target.Key, sealed, st.DeviceID, st.CapturePending.Sequence)
	if err != nil {
		return false, err
	}
	defer p.clear()
	dir := filepath.Join(target.Dir, st.DeviceID, "captures")
	name := captureSegmentName(st.CapturePending.Sequence)
	published, readErr := readBoundedFile(filepath.Join(dir, name), int64(MaxSegmentPlaintext+headerSize+64))
	defer clear(published)
	if readErr == nil {
		if !bytes.Equal(published, p.segment) {
			return false, ErrFormat
		}
	} else if !os.IsNotExist(readErr) {
		return false, readErr
	} else {
		if _, err := os.Stat(filepath.Join(dir, "."+name+".icloud")); err == nil {
			return false, ErrFolderNotReady
		} else if !os.IsNotExist(err) {
			return false, err
		}
		valid, err := w.Store.(CaptureExporter).CurrentReplicaCaptureParts(ctx, p.items)
		if err != nil {
			return false, err
		}
		if !current() {
			return false, ErrMemoryChanging
		}
		if !allValidCaptureParts(valid) {
			var kept []durable.ExportedCapturePart
			for i, item := range p.items {
				if valid[i] {
					kept = append(kept, item)
				}
			}
			records, err := captureRecords(kept)
			if err != nil {
				return false, err
			}
			clearRecords(p.records)
			p.records = records
			for i := range p.items {
				clear(p.items[i].Summary)
				clear(p.items[i].Part)
			}
			p.items = cloneCaptureItems(kept)
			device, _ := ParseDeviceID(st.DeviceID)
			replacement, err := SealCaptureSegment(target.Key, device, st.CapturePending.Sequence, p.records, nil)
			if err != nil {
				return false, err
			}
			clear(p.segment)
			p.segment = replacement
			if err := w.stageCapturePending(ctx, st, target.Key, st.CapturePending.Sequence, p); err != nil {
				return false, err
			}
			st = w.State.Get()
		}
		if err := w.State.withCurrent(ctx, st, func() error {
			if !current() {
				return ErrMemoryChanging
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			return publishImmutable(dir, name, p.segment)
		}); err != nil {
			return false, err
		}
	}
	if err := w.State.withCurrent(ctx, st, func() error {
		if !current() {
			return ErrMemoryChanging
		}
		if err := w.Store.(CaptureExporter).AcknowledgeReplicaCaptureExport(ctx, st.DeviceID, p.items, false); err != nil {
			return err
		}
		next := cloneState(w.State.st)
		next.CaptureLastSeq = st.CapturePending.Sequence
		next.CaptureCursor = p.cursor
		next.CapturePending = nil
		next.LastBackupAt = time.Now().UTC()
		if err := writeStateFile(w.State.path, next); err != nil {
			return err
		}
		w.State.st = next
		return nil
	}); err != nil {
		return false, err
	}
	_ = os.Remove(w.State.captureOutboxPath(st.DeviceID, st.CapturePending.Hash))
	return p.cursor != "", nil
}

func allValidCaptureParts(valid []bool) bool {
	for _, value := range valid {
		if !value {
			return false
		}
	}
	return true
}

func captureSegmentName(seq uint64) string {
	const digits = "0123456789"
	var b [12]byte
	for i := len(b) - 1; i >= 0; i-- {
		b[i] = digits[seq%10]
		seq /= 10
	}
	return string(b[:]) + ".cap"
}
