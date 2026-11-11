package replica

import (
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
	ExportObservations(ctx context.Context, after time.Time, maxCount, maxBytes int) ([]durable.ExportedObservation, bool, error)
	Tombstones(ctx context.Context) ([]durable.Tombstone, error)
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

// Flush writes at most one segment containing observations and tombstones
// newer than the watermarks, then advances them. It reports whether more
// content remains. It writes nothing while memory is changing.
func (w *Writer) Flush(ctx context.Context) (more bool, err error) {
	if w == nil || w.Store == nil || w.State == nil || w.Resolve == nil {
		return false, ErrDisabled
	}
	st := w.State.Get()
	if !st.Enabled {
		return false, ErrDisabled
	}
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
	items, more, err := w.Store.ExportObservations(ctx, watermarkTime(st.ObsWatermark), flushMaxObservations, flushMaxBytes)
	if err != nil {
		return false, err
	}
	defer func() {
		for _, item := range items {
			clear(item.Record)
		}
	}()
	tombs, err := w.Store.Tombstones(ctx)
	if err != nil {
		return false, err
	}
	var records []Record
	obsMark, tombMark := st.ObsWatermark, st.TombWatermark
	imported, err := w.importedIDs(ctx, items)
	if err != nil {
		return false, err
	}
	for i, item := range items {
		obsMark = max(obsMark, item.CapturedAt.UnixNano())
		if imported[i] {
			continue // came from another device; it already has it
		}
		records = append(records, Record{Kind: KindObservation, At: item.CapturedAt, Observation: item.Record})
	}
	for _, t := range tombs {
		if t.At.UnixNano() <= st.TombWatermark {
			continue
		}
		records = append(records, Record{Kind: KindTombstone, At: t.At, SourceID: t.SourceID})
		tombMark = max(tombMark, t.At.UnixNano())
	}
	if len(records) == 0 {
		if obsMark != st.ObsWatermark || tombMark != st.TombWatermark {
			// Only imported content was scanned: advance so it is not rescanned.
			err = w.State.Update(func(s *State) {
				s.ObsWatermark = max(s.ObsWatermark, obsMark)
				s.TombWatermark = max(s.TombWatermark, tombMark)
			})
		}
		return more && err == nil, err
	}
	target, err := w.Resolve(ctx)
	if err != nil {
		return false, err
	}
	if target.Release != nil {
		defer target.Release()
	}
	defer clear(target.Key)
	seq := st.LastSeq + 1
	sealed, err := SealSegment(target.Key, device, seq, records, nil)
	if err != nil {
		return false, err
	}
	if !current() {
		return false, ErrMemoryChanging
	}
	dir := filepath.Join(target.Dir, st.DeviceID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	if err := writeFileAtomic(dir, segmentName(seq), sealed); err != nil {
		return false, err
	}
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	if err := w.State.Update(func(s *State) {
		s.LastSeq = seq
		s.ObsWatermark = obsMark
		s.TombWatermark = tombMark
		s.LastBackupAt = now().UTC()
	}); err != nil {
		return false, err
	}
	slog.Info("replica segment written", "result", "ok", "records", len(records))
	return more, nil
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
	if err != nil {
		_ = os.Remove(tmpName)
	}
	return err
}
