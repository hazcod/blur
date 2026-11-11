package replica

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/storage/durable"
)

// MaxSegmentsPerRun bounds one import pass.
const MaxSegmentsPerRun = 200

// Sink is the store surface the importer writes through. Observations go
// through the normal put path so indexes and derived state update.
type Sink interface {
	GetObservation(ctx context.Context, id string) (capture.SanitizedObservation, bool, error)
	PutObservationAt(ctx context.Context, o capture.SanitizedObservation, at time.Time) error
	Tombstones(ctx context.Context) ([]durable.Tombstone, error)
	RecordTombstone(ctx context.Context, t durable.Tombstone) error
}

// NoteSaver is optionally implemented by the store so imported user notes also
// update the Today projection.
type NoteSaver interface {
	SaveNote(ctx context.Context, o capture.SanitizedObservation, at time.Time) error
}

// OriginTracker is optionally implemented by the store. Imported content IDs
// are recorded so the writer never re-exports what another device already has.
type OriginTracker interface {
	MarkImported(ctx context.Context, ids []string) error
	ImportedSet(ctx context.Context, ids []string) (map[string]bool, error)
}

// Deleter applies tombstones through the deletion service.
type Deleter interface {
	DeleteSource(ctx context.Context, sourceID string) (int, error)
	DeleteBefore(ctx context.Context, cutoff time.Time) (int, error)
}

// Result summarizes one import pass; it holds counts only.
type Result struct {
	Segments, Observations, Tombstones, Skipped, Rejected int
}

// Importer imports other devices' segments.
type Importer struct {
	Store    Sink
	Deleter  Deleter
	State    *StateFile
	Resolve  Resolver
	Fence    Fence
	Changing func() // optional: called before the first write of a pass
	Stable   func() // optional: called after a pass that called Changing
	Now      func() time.Time
}

type segmentFile struct {
	seq  uint64
	path string
}

// Run imports up to MaxSegmentsPerRun new segments. It is idempotent: replays
// skip known content IDs, and per-device watermarks only advance after a
// segment was fully applied. Sequence numbers must be contiguous, so a segment
// the provider has not delivered yet pauses that device until it arrives.
func (im *Importer) Run(ctx context.Context) (res Result, err error) {
	if im == nil || im.Store == nil || im.State == nil || im.Resolve == nil {
		return res, ErrDisabled
	}
	st := im.State.Get()
	if !st.Enabled {
		return res, ErrDisabled
	}
	if im.Fence != nil {
		if _, ok := im.Fence(); !ok {
			return res, ErrMemoryChanging
		}
	}
	target, err := im.Resolve(ctx)
	if err != nil {
		return res, err
	}
	if target.Release != nil {
		defer target.Release()
	}
	defer clear(target.Key)
	entries, err := os.ReadDir(target.Dir)
	if err != nil {
		return res, err
	}
	var devices []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || name == st.DeviceID {
			continue
		}
		if _, ok := ParseDeviceID(name); !ok {
			continue // also skips dot-names and placeholders
		}
		devices = append(devices, name)
	}
	sort.Strings(devices)
	if err := im.State.Update(func(s *State) { s.OtherDevices = len(devices) }); err != nil {
		return res, err
	}
	changing := false
	defer func() {
		if changing && im.Stable != nil {
			im.Stable()
		}
	}()
	budget := MaxSegmentsPerRun
	for _, device := range devices {
		if budget == 0 || ctx.Err() != nil {
			break
		}
		files := listSegments(filepath.Join(target.Dir, device))
		last := im.State.Get().Imported[device]
		for _, file := range files {
			if budget == 0 || ctx.Err() != nil {
				break
			}
			if file.seq <= last {
				continue
			}
			if file.seq != last+1 {
				break // gap: wait for the provider to deliver the missing one
			}
			if !changing {
				if im.Changing != nil {
					im.Changing()
				}
				changing = true
			}
			if err := im.importSegment(ctx, target.Key, device, file, &res); err != nil {
				if ctx.Err() != nil {
					return res, ctx.Err()
				}
				res.Rejected++
				slog.Warn("replica segment rejected", "result", classify(err))
				break // fail closed: never skip past a bad segment
			}
			last = file.seq
			budget--
			res.Segments++
			seq := file.seq
			if err := im.State.Update(func(s *State) { s.Imported[device] = seq }); err != nil {
				return res, err
			}
		}
	}
	if res.Segments > 0 || res.Rejected > 0 {
		slog.Info("replica import finished", "result", "ok", "segments", res.Segments, "observations", res.Observations, "rejected", res.Rejected)
	}
	if res.Rejected > 0 {
		return res, ErrAuth
	}
	return res, nil
}

func classify(err error) string {
	switch {
	case errors.Is(err, ErrAuth):
		return "auth_failed"
	case errors.Is(err, ErrFormat):
		return "invalid"
	default:
		return "failed"
	}
}

// listSegments returns NNNNNNNNNNNN.seg files in ascending sequence order.
// Names that do not match exactly (temp files, iCloud placeholders such as
// ".000000000001.seg.icloud") are ignored.
func listSegments(dir string) []segmentFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []segmentFile
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) != 16 || name[12:] != ".seg" {
			continue
		}
		var seq uint64
		valid := true
		for i := 0; i < 12; i++ {
			if name[i] < '0' || name[i] > '9' {
				valid = false
				break
			}
			seq = seq*10 + uint64(name[i]-'0')
		}
		if valid && seq > 0 {
			files = append(files, segmentFile{seq: seq, path: filepath.Join(dir, name)})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].seq < files[j].seq })
	return files
}

func (im *Importer) importSegment(ctx context.Context, key []byte, device string, file segmentFile, res *Result) error {
	info, err := os.Stat(file.path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxSegmentPlaintext+headerSize+64 {
		return ErrFormat
	}
	data, err := os.ReadFile(file.path)
	if err != nil {
		return err
	}
	header, records, err := OpenSegment(key, data)
	if err != nil {
		return err
	}
	if FormatDeviceID(header.DeviceID) != device || header.Seq != file.seq {
		return ErrFormat // renamed or copied segment
	}
	// Validate every observation before applying anything.
	observations := make([]capture.SanitizedObservation, len(records))
	for i, r := range records {
		if r.Kind != KindObservation {
			continue
		}
		o, err := capture.RestoreAuthenticatedObservation(r.Observation)
		if err != nil || o.Validate() != nil || !durable.ValidCapturedAt(r.At) {
			return ErrFormat
		}
		observations[i] = o
	}
	for _, r := range records {
		if r.Kind != KindTombstone {
			continue
		}
		if err := im.applyTombstone(ctx, r); err != nil {
			return err
		}
		res.Tombstones++
	}
	log, err := im.Store.Tombstones(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	if im.Now != nil {
		now = im.Now()
	}
	type pending struct {
		o  capture.SanitizedObservation
		at time.Time
	}
	var puts []pending
	for i, r := range records {
		if r.Kind != KindObservation {
			continue
		}
		o := observations[i]
		if durable.TombstoneCovers(log, o.ContentID(), r.At) {
			res.Skipped++
			continue
		}
		if _, found, err := im.Store.GetObservation(ctx, o.ContentID()); err != nil {
			return err
		} else if found {
			res.Skipped++
			continue
		}
		at := r.At
		if at.After(now.Add(time.Hour)) {
			at = now.UTC()
		}
		puts = append(puts, pending{o, at})
	}
	// Record origin before writing: a crash in between only means the segment
	// is retried, never that imported content is exported back.
	if tracker, ok := im.Store.(OriginTracker); ok && len(puts) > 0 {
		ids := make([]string, len(puts))
		for i, p := range puts {
			ids[i] = p.o.ContentID()
		}
		if err := tracker.MarkImported(ctx, ids); err != nil {
			return err
		}
	}
	for _, p := range puts {
		o, at := p.o, p.at
		if saver, ok := im.Store.(NoteSaver); ok && o.AppID() == "sh.blur.notes" && o.Domain() == "local" {
			if saver.SaveNote(ctx, o, at) == nil {
				res.Observations++
				continue
			}
		}
		if err := im.Store.PutObservationAt(ctx, o, at); err != nil {
			return err
		}
		res.Observations++
	}
	return nil
}

func (im *Importer) applyTombstone(ctx context.Context, r Record) error {
	if im.Deleter == nil {
		return ErrDisabled
	}
	// Record first with the original time so the local deletion's own
	// tombstone is a no-op and nothing is re-exported with a new timestamp.
	if err := im.Store.RecordTombstone(ctx, durable.Tombstone{SourceID: r.SourceID, At: r.At}); err != nil {
		return err
	}
	if r.SourceID == durable.AllSourcesTombstone {
		_, err := im.Deleter.DeleteBefore(ctx, r.At)
		return err
	}
	_, err := im.Deleter.DeleteSource(deletion.WithoutTombstone(ctx), r.SourceID)
	return err
}
