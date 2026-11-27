package replica

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage/durable"
)

// Target is a resolved sync folder with its key. Release must be called when
// the caller is done (it ends security-scoped access).
type Target struct {
	Dir     string
	Key     []byte
	Release func()
}

// Resolver opens the configured folder and key, or fails if sync is not ready.
type Resolver func(ctx context.Context) (Target, error)

// Fence reports whether memory is stable. When ok, current must be re-checked
// right before durable side effects; it turns false if a deletion window opens.
type Fence func() (current func() bool, ok bool)

// ErrMemoryChanging is returned when a flush or import is skipped because the
// memory fence is closed.
var ErrMemoryChanging = errors.New("replica: memory is changing")

// ErrDisabled is returned when sync is off.
var ErrDisabled = errors.New("replica: sync is off")

// Exporter is the store surface the writer needs.
type Exporter interface {
	ExportReplicaObservations(ctx context.Context, generation string, after []byte, maxCount, maxBytes int) (durable.ReplicaExportPage, error)
	AcknowledgeReplicaExport(context.Context, string, []durable.ExportedObservation, bool) error
	CurrentReplicaObservations(context.Context, []durable.ExportedObservation) ([]bool, error)
	DeletionChanges(context.Context, uint64, int) (durable.DeletionChangePage, error)
}

type CaptureExporter interface {
	ExportReplicaCaptureParts(context.Context, string, string, int, int) (durable.ReplicaCaptureExportPage, error)
	ExportedReplicaCaptureParts(context.Context, string, []durable.ExportedCapturePart) ([]bool, error)
	CurrentReplicaCaptureParts(context.Context, []durable.ExportedCapturePart) ([]bool, error)
	AcknowledgeReplicaCaptureExport(context.Context, string, []durable.ExportedCapturePart, bool) error
	ReplicaCaptureImported(context.Context, string, string) (bool, error)
}

// Writer exports one segment per Flush into <folder>/<device>/<seq>.seg.
type Writer struct {
	Store   Exporter
	State   *StateFile
	Resolve Resolver
	Fence   Fence
	Now     func() time.Time
}

const (
	flushMaxObservations = 4096
	flushMaxBytes        = 12 << 20
)

func segmentName(seq uint64) string {
	const digits = "0123456789"
	var b [12]byte
	for i := len(b) - 1; i >= 0; i-- {
		b[i] = digits[seq%10]
		seq /= 10
	}
	return string(b[:]) + ".seg"
}

func watermarkTime(nanos int64) time.Time {
	if nanos <= 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos).UTC()
}

// Flush processes one bounded observation-order page and at most one immutable
// segment. Exact revision receipts and the physical cursor control export;
// watermarks are compatibility diagnostics. It writes nothing while memory
// is changing and reports whether the current sweep has more work.
func (w *Writer) Flush(parent context.Context) (more bool, err error) {
	if w == nil || w.Store == nil || w.State == nil || w.Resolve == nil {
		return false, ErrDisabled
	}
	if parent == nil {
		return false, ErrFormat
	}
	w.State.exportMu.Lock()
	defer w.State.exportMu.Unlock()
	if w.State.Get().OutboxInvalid {
		if err := w.State.InvalidateOutbox(); err != nil {
			return false, err
		}
	}
	ctx, st, done, err := w.State.beginOperation(parent, true)
	if err != nil {
		return false, err
	}
	defer done()
	device, ok := ParseDeviceID(st.DeviceID)
	if !ok {
		return false, ErrFormat
	}
	current := func() bool { return true }
	if w.Fence != nil {
		var stable bool
		current, stable = w.Fence()
		if !stable {
			return false, ErrMemoryChanging
		}
	}
	target, err := w.Resolve(ctx)
	if err != nil {
		return false, err
	}
	if target.Release != nil {
		defer target.Release()
	}
	defer clear(target.Key)
	if st.Pending != nil {
		return w.replayPending(ctx, st, target, current)
	}
	if st.CapturePending != nil {
		return w.replayCapturePending(ctx, st, target, current)
	}
	// Recover public v1 segments left by an older writer after its state commit
	// failed. Never replace their identities; receipts will resend any uncertain
	// captures under the next sequence, where old peers deduplicate safely.
	dir := filepath.Join(target.Dir, st.DeviceID)
	seq := st.LastSeq + 1
	if st.LastSeq >= 999999999999 {
		return false, ErrFormat
	}
	old, readErr := readBoundedFile(filepath.Join(dir, segmentName(seq)), int64(MaxSegmentPlaintext+headerSize+64))
	if readErr == nil {
		defer clear(old)
		header, records, openErr := OpenSegment(target.Key, old)
		defer clearRecords(records)
		if openErr != nil {
			return false, openErr
		}
		if FormatDeviceID(header.DeviceID) != st.DeviceID || header.Seq != seq {
			return false, ErrFormat
		}
		if !current() {
			return false, ErrMemoryChanging
		}
		err = w.State.updateCurrent(ctx, st, func(s *State) { s.LastSeq = seq })
		return err == nil, err
	}
	if !os.IsNotExist(readErr) {
		return false, readErr
	}
	if _, err := os.Stat(filepath.Join(dir, "."+segmentName(seq)+".icloud")); err == nil {
		return false, ErrFolderNotReady
	} else if !os.IsNotExist(err) {
		return false, err
	}
	page, err := w.Store.ExportReplicaObservations(ctx, st.DeviceID, st.ObservationCursor, flushMaxObservations, flushMaxBytes)
	items, more := page.Items, page.HasMore
	if err != nil {
		return false, err
	}
	defer clearExportItems(items)
	deletions, err := w.Store.DeletionChanges(ctx, st.TombSequence, durable.MaxDeletionChangePage)
	if err != nil {
		return false, err
	}
	more = more || deletions.HasMore
	imported, err := w.importedIDs(ctx, items)
	if err != nil {
		return false, err
	}
	p := pendingExport{tombSequence: deletions.Next, obsMark: st.ObsWatermark, tombMark: st.TombWatermark, nextCursor: page.Next}
	var skipped []durable.ExportedObservation
	for i, item := range items {
		p.obsMark = max(p.obsMark, item.CapturedAt.UnixNano())
		if imported[i] {
			skipped = append(skipped, item)
			continue
		}
		p.items = append(p.items, item)
		p.records = append(p.records, Record{Kind: KindObservation, At: item.CapturedAt, Observation: item.Record})
	}
	if len(skipped) > 0 {
		err = w.State.withCurrent(ctx, st, func() error { return w.Store.AcknowledgeReplicaExport(ctx, st.DeviceID, skipped, true) })
		if err != nil {
			return false, err
		}
	}
	for _, change := range deletions.Changes {
		t := change.Tombstone
		p.records = append(p.records, Record{Kind: KindTombstone, At: t.At, SourceID: t.SourceID})
		p.tombMark = max(p.tombMark, t.At.UnixNano())
	}
	if len(p.records) == 0 {
		err = w.State.updateCurrent(ctx, st, func(s *State) {
			s.ObsWatermark = p.obsMark
			s.TombWatermark = p.tombMark
			s.TombSequence = p.tombSequence
			s.ObservationCursor = append([]byte(nil), p.nextCursor...)
		})
		if err != nil || more {
			return more && err == nil, err
		}
		return w.flushCaptures(ctx, w.State.Get(), target, current)
	}
	p.segment, err = SealSegment(target.Key, device, seq, p.records, nil)
	if err != nil {
		return false, err
	}
	defer clear(p.segment)
	if !current() {
		return false, ErrMemoryChanging
	}
	if err = w.stagePending(ctx, st, target.Key, seq, p); err != nil {
		return false, err
	}
	st = w.State.Get()
	_, err = w.replayPending(ctx, st, target, current)
	return more && err == nil, err
}

func clearExportItems(items []durable.ExportedObservation) {
	for _, item := range items {
		clear(item.Record)
	}
}
func clearRecords(records []Record) {
	for _, record := range records {
		clear(record.Observation)
		clear(record.Summary)
		clear(record.Part)
	}
}

func (w *Writer) stagePending(ctx context.Context, st State, key []byte, seq uint64, p pendingExport) error {
	sealed, err := encodePending(key, p)
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
		path := w.State.outboxPath(st.DeviceID, hash)
		if err := writeFileAtomic(dir, filepath.Base(path), sealed); err != nil {
			return err
		}
		next := cloneState(w.State.st)
		next.Pending = &PendingState{Sequence: seq, Hash: hash}
		if err := writeStateFile(w.State.path, next); err != nil {
			// Rename may have succeeded even if directory durability failed.
			// Keep both sealed versions until a durable state selects one.
			return err
		}
		w.State.st = next
		if st.Pending != nil && st.Pending.Hash != hash {
			_ = os.Remove(w.State.outboxPath(st.DeviceID, st.Pending.Hash))
		}
		return nil
	})
}

func (w *Writer) replayPending(ctx context.Context, st State, target Target, current func() bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if st.Pending == nil || st.Pending.Sequence != st.LastSeq+1 {
		return false, ErrFormat
	}
	sealed, err := readBoundedFile(w.State.outboxPath(st.DeviceID, st.Pending.Hash), int64(MaxSegmentPlaintext+headerSize+64+33+32*flushMaxObservations+64))
	if err != nil {
		if os.IsNotExist(err) {
			if st.ConfigVersion == ^uint64(0) {
				return false, ErrFormat
			}
			// Privacy invalidation may have erased every private byte before
			// its state commit failed. Never reuse that uncertain public identity.
			id, generationErr := NewDeviceID()
			if generationErr != nil {
				return false, generationErr
			}
			err = w.State.updateCurrent(ctx, st, func(s *State) {
				s.DeviceID, s.Pending = id, nil
				s.LastSeq, s.TombSequence = 0, 0
				s.ObsWatermark, s.TombWatermark = 0, 0
				s.ObservationCursor = nil
				s.ConfigVersion++
			})
			return err == nil, err
		}
		return false, err
	}
	defer clear(sealed)
	if outboxHash(sealed) != st.Pending.Hash {
		return false, ErrAuth
	}
	p, err := decodePending(target.Key, sealed, st.DeviceID, st.Pending.Sequence)
	if err != nil {
		return false, err
	}
	defer p.clear()
	dir := filepath.Join(target.Dir, st.DeviceID)
	name := segmentName(st.Pending.Sequence)
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
		valid, err := w.Store.CurrentReplicaObservations(ctx, p.items)
		if err != nil {
			return false, err
		}
		changed, index := false, 0
		var records []Record
		var items []durable.ExportedObservation
		for _, record := range p.records {
			if record.Kind == KindObservation {
				if !valid[index] {
					changed = true
					index++
					continue
				}
				items = append(items, p.items[index])
				index++
			}
			records = append(records, record)
		}
		if changed {
			// Target absence was proved above. Drop stale observations before first
			// publication; retain tombstones and the same unused contiguous sequence.
			device, _ := ParseDeviceID(st.DeviceID)
			replacement, err := SealSegment(target.Key, device, st.Pending.Sequence, records, nil)
			if err != nil {
				return false, err
			}
			clear(p.segment)
			p.segment = replacement
			p.records = records
			p.items = items
			if !current() {
				return false, ErrMemoryChanging
			}
			if err = w.stagePending(ctx, st, target.Key, st.Pending.Sequence, p); err != nil {
				return false, err
			}
			st = w.State.Get()
		}
		err = w.State.withCurrent(ctx, st, func() error {
			if !current() {
				return ErrMemoryChanging
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			return publishImmutable(dir, name, p.segment)
		})
		if err != nil {
			return false, err
		}
	}
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	err = w.State.withCurrent(ctx, st, func() error {
		if !current() {
			return ErrMemoryChanging
		}
		if err := w.Store.AcknowledgeReplicaExport(ctx, st.DeviceID, p.items, false); err != nil {
			return err
		}
		next := cloneState(w.State.st)
		next.LastSeq = st.Pending.Sequence
		next.Pending = nil
		next.ObsWatermark = p.obsMark
		next.TombWatermark = p.tombMark
		next.TombSequence = p.tombSequence
		next.ObservationCursor = append([]byte(nil), p.nextCursor...)
		next.LastBackupAt = now().UTC()
		if err := writeStateFile(w.State.path, next); err != nil {
			return err
		}
		w.State.st = next
		return nil
	})
	if err != nil {
		return false, err
	}
	_ = os.Remove(w.State.outboxPath(st.DeviceID, st.Pending.Hash))
	slog.Info("replica segment written", "result", "ok", "records", len(p.records))
	return true, nil // replay may have been followed by new local captures
}

// importedIDs flags exported items whose content was imported from another
// device. Stores without origin tracking flag nothing.
func (w *Writer) importedIDs(ctx context.Context, items []durable.ExportedObservation) ([]bool, error) {
	flags := make([]bool, len(items))
	tracker, ok := w.Store.(OriginTracker)
	if !ok || len(items) == 0 {
		return flags, nil
	}
	ids := make([]string, len(items))
	for i, item := range items {
		o, err := capture.RestoreAuthenticatedObservation(item.Record)
		if err != nil {
			return nil, ErrFormat
		}
		ids[i] = o.ContentID()
	}
	set, err := tracker.ImportedSet(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i, id := range ids {
		flags[i] = set[id]
	}
	return flags, nil
}

// writeFileAtomic writes via a dot-prefixed temp file and renames into place,
// so importers (which skip dot-names) never see partial segments.
func writeFileAtomic(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmpName, filepath.Join(dir, name))
	}
	if err == nil {
		err = syncDirectory(dir)
	}
	if err != nil {
		_ = os.Remove(tmpName)
	}
	return err
}
