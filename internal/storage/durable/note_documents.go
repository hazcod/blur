package durable

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const (
	noteDocumentNS         = "note-document-v1"
	noteDocumentSourceNS   = "note-document-source-v1"
	noteDocumentRevisionNS = "note-document-revision-v1"
	noteDocumentDayNS      = "note-document-day-v1"
	noteDocumentMetaNS     = "note-document-meta-v1"
	noteDocumentFenceNS    = "note-document-fence-v1"
	maxNoteDocumentDays    = 32768
	maxNoteDocumentsPerDay = 4096
)

type noteDocumentSource struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
}
type noteDocumentOrder struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
}
type noteDocumentMigration struct {
	After string `json:"after,omitempty"`
	Done  bool   `json:"done"`
}

func validNoteDocument(d notes.Document) bool {
	return validHash(d.ID) && validHash(d.SourceID) && d.Revision > 0 && d.Revision < 1<<48 && validCaptureTime(d.CreatedAt) && validCaptureTime(d.UpdatedAt) && !d.UpdatedAt.Before(d.CreatedAt)
}
func noteRevisionKey(id string, revision uint64) string { return fmt.Sprintf("%s:%012x", id, revision) }

func (s *Store) noteDocumentReader(r bolt.Reader, id string) (notes.Document, bool, error) {
	b, found, err := s.readIndexValueReader(r, noteDocumentNS, id, 1024)
	defer clear(b)
	if err != nil || !found {
		return notes.Document{}, found, err
	}
	var d notes.Document
	if json.Unmarshal(b, &d) != nil || d.ID != id || !validNoteDocument(d) {
		return notes.Document{}, false, ErrCorrupt
	}
	return d, true, nil
}
func (s *Store) noteDocumentSourceReader(r bolt.Reader, id string) (noteDocumentSource, bool, error) {
	b, found, err := s.readIndexValueReader(r, noteDocumentSourceNS, id, 160)
	defer clear(b)
	if err != nil || !found {
		return noteDocumentSource{}, found, err
	}
	var ref noteDocumentSource
	if json.Unmarshal(b, &ref) != nil || !validHash(ref.ID) || ref.Revision == 0 || ref.Revision >= 1<<48 {
		return noteDocumentSource{}, false, ErrCorrupt
	}
	return ref, true, nil
}

// Legacy observations and non-notes remain eligible. A known revision is only
// eligible while its authenticated document head still names that exact source.
func (s *Store) isCurrentNoteSourceReader(r bolt.Reader, id string) (bool, error) {
	ref, found, err := s.noteDocumentSourceReader(r, id)
	if err != nil || !found {
		return !found, err
	}
	d, live, err := s.noteDocumentReader(r, ref.ID)
	return live && d.SourceID == id && d.Revision == ref.Revision, err
}
func (s *Store) IsCurrentNoteSource(ctx context.Context, id string) (bool, error) {
	if ctx == nil || !validHash(id) {
		return false, notes.ErrInvalid
	}
	var current bool
	err := s.view(ctx, func(r bolt.Reader) error {
		var err error
		current, err = s.isCurrentNoteSourceReader(r, id)
		return err
	})
	return current, err
}

func (s *Store) noteDocumentUpdate(ctx context.Context, fn func(*Tx) error) error {
	var outcome error
	err := s.Update(ctx, func(tx *Tx) error {
		err := fn(tx)
		for _, expected := range []error{notes.ErrInvalid, notes.ErrDocumentConflict, notes.ErrDocumentNotFound, notes.ErrDocumentCapacity, notes.ErrSourceUnavailable} {
			if errors.Is(err, expected) {
				outcome = expected
				return &storage.StoreError{Code: storage.Invalid}
			}
		}
		return err
	})
	if err != nil && outcome != nil {
		return outcome
	}
	return err
}

func sanitizeDocument(id string, revision uint64, text string) (capture.SanitizedObservation, error) {
	if text == "" || strings.TrimSpace(text) == "" || len(text) > notes.MaxNoteBytes || !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 {
		return capture.SanitizedObservation{}, notes.ErrInvalid
	}
	o, err := capture.SanitizeObservation("sh.blur.notes", fmt.Sprintf("r%d.%s.notes.local", revision, id), text, notes.MaxNoteBytes)
	if err != nil || o.Truncated() {
		return capture.SanitizedObservation{}, notes.ErrInvalid
	}
	return o, nil
}

func (s *Store) CreateNoteDocument(ctx context.Context, text string, at time.Time) (notes.Document, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return notes.Document{}, err
	}
	id := hex.EncodeToString(random[:])
	clear(random[:])
	return s.saveNoteDocument(ctx, id, 0, text, at)
}
func (s *Store) UpdateNoteDocument(ctx context.Context, id string, expected uint64, text string, at time.Time) (notes.Document, error) {
	if expected == 0 {
		return notes.Document{}, notes.ErrInvalid
	}
	return s.saveNoteDocument(ctx, id, expected, text, at)
}

func (s *Store) saveNoteDocument(ctx context.Context, id string, expected uint64, text string, at time.Time) (notes.Document, error) {
	if ctx == nil || s == nil || s.state == nil || !validHash(id) || !validCaptureTime(at) || expected >= (1<<48)-1 {
		return notes.Document{}, notes.ErrInvalid
	}
	o, err := sanitizeDocument(id, expected+1, text)
	if err != nil {
		return notes.Document{}, err
	}
	s.state.noteMu.Lock()
	defer s.state.noteMu.Unlock()
	date, zone := localDate(at)
	snapshot, err := s.noteSnapshot(ctx, date, zone, at, o.ContentID(), o.Text())
	if err != nil {
		return notes.Document{}, err
	}
	var saved notes.Document
	err = s.noteDocumentUpdate(ctx, func(tx *Tx) error {
		old, found, err := s.noteDocumentReader(tx.writer, id)
		if err != nil {
			return err
		}
		if expected == 0 && found || expected > 0 && found && old.Revision != expected {
			return notes.ErrDocumentConflict
		}
		if expected > 0 && !found {
			return notes.ErrDocumentNotFound
		}
		if expected > 0 && at.Before(old.UpdatedAt) {
			return notes.ErrInvalid
		}
		fence, err := tx.writer.Has(s.logicalKey(groupRecord, kindIndex, "index:"+noteDocumentFenceNS, id))
		if err != nil {
			return mapStorePhysicalError(err)
		}
		if fence {
			return notes.ErrDocumentNotFound
		}
		if found && text == "" {
			return notes.ErrInvalid
		}
		if err := tx.PutObservationAt(o, at); err != nil {
			return err
		}
		saved = notes.Document{ID: id, Revision: expected + 1, SourceID: o.ContentID(), Text: o.Text(), CreatedAt: at, UpdatedAt: at}
		if found {
			saved.CreatedAt = old.CreatedAt
		}
		tx.mu.Lock()
		err = tx.putNoteDocumentLocked(saved, old, found)
		tx.mu.Unlock()
		if err != nil {
			return err
		}
		return tx.putTodaySnapshot(snapshot)
	})
	if err != nil {
		return notes.Document{}, err
	}
	return saved, nil
}

// SaveNoteDocument preserves idempotent Quick Save receipts. Once that original
// source has been edited or deleted, saving its text again creates a new note;
// it never presents an older revision as the new document's current content.
func (s *Store) SaveNoteDocument(ctx context.Context, o capture.SanitizedObservation, at time.Time) (capture.SanitizedObservation, error) {
	if ctx == nil || s == nil || s.state == nil || o.Validate() != nil || o.AppID() != "sh.blur.notes" || o.Domain() != "local" || !validCaptureTime(at) {
		return capture.SanitizedObservation{}, notes.ErrInvalid
	}
	s.state.noteMu.Lock()
	newDocument := false
	err := s.view(ctx, func(r bolt.Reader) error {
		ref, mapped, err := s.noteDocumentSourceReader(r, o.ContentID())
		if err != nil {
			return err
		}
		if mapped {
			d, found, err := s.noteDocumentReader(r, ref.ID)
			if err != nil {
				return err
			}
			newDocument = !found || d.SourceID != o.ContentID()
			return nil
		}
		key := s.logicalKey(groupRecord, kindIndex, "index:"+noteDocumentFenceNS, o.ContentID())
		defer clear(key)
		fenced, err := r.Has(key)
		newDocument = fenced
		return mapStorePhysicalError(err)
	})
	if err == nil && !newDocument {
		err = s.saveNoteLocked(ctx, o, at)
	}
	s.state.noteMu.Unlock()
	if err != nil {
		return capture.SanitizedObservation{}, err
	}
	if !newDocument {
		return o, nil
	}
	d, err := s.CreateNoteDocument(ctx, o.Text(), at)
	if err != nil {
		return capture.SanitizedObservation{}, err
	}
	return sanitizeDocument(d.ID, d.Revision, d.Text)
}

func (tx *Tx) putNoteDocumentLocked(d, old notes.Document, existed bool) error {
	if existed {
		if err := tx.changeNoteDocumentOrderLocked(old, false); err != nil {
			return err
		}
	}
	if err := tx.changeNoteDocumentOrderLocked(d, true); err != nil {
		return err
	}
	if err := tx.putIndexValueLocked(noteDocumentNS, d.ID, mustJSON(d)); err != nil {
		return err
	}
	if err := tx.putIndexValueLocked(noteDocumentRevisionNS, noteRevisionKey(d.ID, d.Revision), []byte(d.SourceID)); err != nil {
		return err
	}
	return tx.putIndexValueLocked(noteDocumentSourceNS, d.SourceID, mustJSON(noteDocumentSource{ID: d.ID, Revision: d.Revision}))
}

func (s *Store) noteDocumentDaysReader(r bolt.Reader) ([]string, error) {
	b, found, err := s.readIndexValueReader(r, noteDocumentMetaNS, "days", maxNoteDocumentDays*14+2)
	defer clear(b)
	if err != nil || !found {
		return nil, err
	}
	var days []string
	if json.Unmarshal(b, &days) != nil || len(days) > maxNoteDocumentDays {
		return nil, ErrCorrupt
	}
	for i, day := range days {
		if !validTodayDate(day) || i > 0 && days[i-1] >= day {
			return nil, ErrCorrupt
		}
	}
	return days, nil
}
func (s *Store) noteDocumentDayReader(r bolt.Reader, day string) ([]noteDocumentOrder, error) {
	b, found, err := s.readIndexValueReader(r, noteDocumentDayNS, day, maxNoteDocumentsPerDay*128+2)
	defer clear(b)
	if err != nil || !found {
		return nil, err
	}
	var rows []noteDocumentOrder
	if json.Unmarshal(b, &rows) != nil || len(rows) > maxNoteDocumentsPerDay {
		return nil, ErrCorrupt
	}
	for i, row := range rows {
		if !validHash(row.ID) || !validCaptureTime(row.At) || row.At.UTC().Format("2006-01-02") != day || i > 0 && !noteOrderBefore(rows[i-1], row) {
			return nil, ErrCorrupt
		}
	}
	return rows, nil
}
func noteOrderBefore(a, b noteDocumentOrder) bool {
	if !a.At.Equal(b.At) {
		return a.At.After(b.At)
	}
	return a.ID > b.ID
}
func (tx *Tx) changeNoteDocumentOrderLocked(d notes.Document, add bool) error {
	day := d.UpdatedAt.UTC().Format("2006-01-02")
	days, err := tx.store.noteDocumentDaysReader(tx.writer)
	if err != nil {
		return err
	}
	rows, err := tx.store.noteDocumentDayReader(tx.writer, day)
	if err != nil {
		return err
	}
	filtered := rows[:0]
	for _, row := range rows {
		if row.ID != d.ID {
			filtered = append(filtered, row)
		}
	}
	rows = filtered
	if add {
		if len(rows) >= maxNoteDocumentsPerDay {
			return notes.ErrDocumentCapacity
		}
		rows = append(rows, noteDocumentOrder{d.ID, d.UpdatedAt})
	}
	sort.Slice(rows, func(i, j int) bool { return noteOrderBefore(rows[i], rows[j]) })
	i := sort.SearchStrings(days, day)
	if len(rows) > 0 && (i == len(days) || days[i] != day) {
		if len(days) >= maxNoteDocumentDays {
			return notes.ErrDocumentCapacity
		}
		days = append(days, day)
		sort.Strings(days)
	}
	if len(rows) == 0 && i < len(days) && days[i] == day {
		days = append(days[:i], days[i+1:]...)
	}
	if err := tx.putIndexValueLocked(noteDocumentMetaNS, "days", mustJSON(days)); err != nil {
		return err
	}
	if len(rows) == 0 {
		return tx.deleteIndexValueLocked(noteDocumentDayNS, day)
	}
	return tx.putIndexValueLocked(noteDocumentDayNS, day, mustJSON(rows))
}

func (tx *Tx) admitLegacyNoteLocked(o capture.SanitizedObservation, at time.Time) error {
	if o.AppID() != "sh.blur.notes" {
		return nil
	}
	_, found, err := tx.store.noteDocumentSourceReader(tx.writer, o.ContentID())
	if err != nil || found {
		return err
	}
	fenced, err := tx.writer.Has(tx.store.logicalKey(groupRecord, kindIndex, "index:"+noteDocumentFenceNS, o.ContentID()))
	if err != nil {
		return mapStorePhysicalError(err)
	}
	if fenced {
		return nil
	}
	d := notes.Document{ID: o.ContentID(), SourceID: o.ContentID(), Revision: 1, CreatedAt: at, UpdatedAt: at}
	return tx.putNoteDocumentLocked(d, notes.Document{}, false)
}

func (s *Store) GetNoteDocument(ctx context.Context, id string) (notes.Document, bool, error) {
	if ctx == nil || !validHash(id) {
		return notes.Document{}, false, notes.ErrInvalid
	}
	var d notes.Document
	var found bool
	err := s.view(ctx, func(r bolt.Reader) error {
		var err error
		d, found, err = s.noteDocumentReader(r, id)
		if err != nil {
			return err
		}
		if !found {
			ref, mapped, e := s.noteDocumentSourceReader(r, id)
			if e != nil {
				return e
			}
			if mapped {
				d, found, err = s.noteDocumentReader(r, ref.ID)
				if err != nil {
					return err
				}
			}
		}
		if found {
			o, live, e := s.getObservationReader(r, d.SourceID)
			if e != nil {
				return e
			}
			if !live {
				found = false
				return nil
			}
			d.Text = o.Text()
		}
		return nil
	})
	if err != nil || found {
		return d, found, err
	}
	// A directly opened legacy source is admitted without scanning any history.
	s.state.noteMu.Lock()
	defer s.state.noteMu.Unlock()
	err = s.noteDocumentUpdate(ctx, func(tx *Tx) error {
		o, live, err := s.getObservationReader(tx.writer, id)
		if err != nil || !live {
			return err
		}
		if o.AppID() != "sh.blur.notes" {
			return nil
		}
		at, err := s.observationCapturedAtReader(tx.writer, id)
		if err != nil {
			return err
		}
		tx.mu.Lock()
		err = tx.admitLegacyNoteLocked(o, at)
		tx.mu.Unlock()
		if err != nil {
			return err
		}
		ref, mapped, err := s.noteDocumentSourceReader(tx.writer, id)
		if err != nil || !mapped {
			return err
		}
		d, found, err = s.noteDocumentReader(tx.writer, ref.ID)
		if err != nil || !found {
			return err
		}
		current, live, err := s.getObservationReader(tx.writer, d.SourceID)
		if err != nil || !live {
			found = false
			return err
		}
		d.Text = current.Text()
		return nil
	})
	return d, found, err
}
