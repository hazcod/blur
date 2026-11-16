package durable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/today"
)

const dependencyMigrationTestID = "chronicle-input-deps-v1"

func seedDependencyMigrationFixture(t *testing.T, store *Store) ([]capture.SanitizedObservation, []chronicle.Summary) {
	t.Helper()
	ctx := context.Background()
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	var sources []capture.SanitizedObservation
	for _, text := range []string{"Synthetic Alpha source retained", "Synthetic Beta source retained"} {
		source, err := capture.SanitizeObservation("editor.app", "", text, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservationAt(ctx, source, start.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, source)
	}
	ids := []string{sources[0].ContentID(), sources[1].ContentID()}
	sort.Strings(ids)
	var rows []chronicle.Summary
	for i := range 4 {
		at := start.Add(time.Duration(i) * 24 * time.Hour)
		if i > 0 {
			for _, source := range sources {
				if err := store.RecordObservationSighting(ctx, source, at.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
			}
		}
		row := chronicle.Summary{HourID: "hour:" + at.Format("20060102T15Z"), Start: at, End: at.Add(time.Hour), Text: "Synthetic Alpha and Beta generated recap.", SourceIDs: ids[:1]}
		if i%2 == 0 {
			row.InputSourceIDs = append([]string(nil), ids...)
			row.InputReceipt = strings.Repeat("a", 64)
		}
		if err := store.PutChronicle(ctx, row); err != nil {
			t.Fatal("seed recap", i, err)
		}
		rows = append(rows, row)
	}
	manual, err := capture.SanitizeObservation("sh.blur.notes", "local", "User-authored note preserved by recap migration.", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, manual, start); err != nil {
		t.Fatal(err)
	}
	sources = append(sources, manual)
	if err := store.Update(ctx, func(tx *Tx) error {
		raw, found, err := tx.readIndexValue("storage-meta", dependencyMigrationTestID, 64)
		clear(raw)
		if err != nil || !found {
			return err
		}
		return tx.deleteKnowledgeRecord("storage-meta", dependencyMigrationTestID)
	}); err != nil {
		t.Fatal("remove migration marker", err)
	}
	return sources, rows
}

func assertDependencyMigrationResult(t *testing.T, store *Store, sources []capture.SanitizedObservation, rows []chronicle.Summary) {
	t.Helper()
	ctx := context.Background()
	for i, row := range rows {
		got, found, err := store.GetChronicle(ctx, row.HourID)
		if err != nil || found != (i%2 == 0) {
			t.Fatal("open returned unknown-input recap or erased dependency-aware recap", i, found, err)
		}
		if found && (got.Text != row.Text || got.InputReceipt != row.InputReceipt || len(got.InputSourceIDs) != len(row.InputSourceIDs)) {
			t.Fatal("migration rewrote retained generated recap")
		}
	}
	for _, source := range sources {
		got, found, err := store.GetObservation(ctx, source.ContentID())
		if err != nil || !found || got.Text() != source.Text() {
			t.Fatal("migration removed or rewrote raw/user-authored memory", found, err)
		}
	}
}

func TestChronicleDependencyMigrationRemovesUnknownInputRecapsBeforeOpenReturns(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xd9)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	sources, rows := seedDependencyMigrationFixture(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	assertDependencyMigrationResult(t, store, sources, rows)
	if _, found, err := store.readIndexValue(ctx, "storage-meta", dependencyMigrationTestID, 64); err != nil || !found {
		t.Fatal("successful migration has no authenticated completion marker", found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	assertDependencyMigrationResult(t, store, sources, rows)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("completed migration rewrote the ready store", err)
	}
	for _, forbidden := range [][]byte{[]byte("Synthetic Alpha"), []byte("User-authored note"), []byte(dependencyMigrationTestID), []byte(rows[1].HourID)} {
		if bytes.Contains(after, forbidden) {
			t.Fatal("migration exposed plaintext source, recap, or cursor metadata")
		}
	}
}

func TestChronicleDependencyMigrationNewStoreStartsComplete(t *testing.T) {
	store := openReplicaTestStore(t)
	raw, found, err := store.readIndexValue(context.Background(), "storage-meta", dependencyMigrationTestID, 64)
	defer clear(raw)
	if err != nil || !found || !bytes.Equal(raw, []byte{2, chronicleDependencyComplete}) {
		t.Fatal("new store did not authenticate completed empty-history migration", found, err)
	}
}

func TestChronicleDependencyMigrationMarkerStages(t *testing.T) {
	hour := "hour:20261106T10Z"
	cursor := make([]byte, physicalKeyBytes)
	cursor[0], cursor[1], cursor[len(cursor)-1] = groupRecord, kindIndex, 7
	for _, test := range []struct {
		name  string
		raw   []byte
		phase byte
		hour  string
		index []byte
	}{
		{"legacy_incomplete", []byte{1, 0}, chronicleDependencyRecaps, "", nil},
		{"legacy_summary_cursor", append([]byte{1, 0}, hour...), chronicleDependencyRecaps, hour, nil},
		{"legacy_complete_needs_questions", []byte{1, 1}, chronicleDependencyQuestions, "", nil},
		{"summary_cursor", encodeChronicleDependencyMigration(hour, false), chronicleDependencyRecaps, hour, nil},
		{"questions_start", []byte{2, 1}, chronicleDependencyQuestions, "", nil},
		{"questions_cursor", append([]byte{2, 1}, cursor...), chronicleDependencyQuestions, "", cursor},
		{"complete", encodeChronicleDependencyMigration("", true), chronicleDependencyComplete, "", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeChronicleDependencyMigration(test.raw)
			defer clear(got.afterIndex)
			if err != nil || got.phase != test.phase || got.afterHour != test.hour || !bytes.Equal(got.afterIndex, test.index) {
				t.Fatal("migration stage lost restart position", got.phase, err)
			}
			if len(got.afterIndex) > 0 {
				test.raw[len(test.raw)-1] ^= 0xff
				if !bytes.Equal(got.afterIndex, test.index) {
					t.Fatal("migration cursor aliases the cleared authenticated record")
				}
			}
		})
	}
}

func TestChronicleDependencyMigrationFailedOpenResumesAfterCommittedSummary(t *testing.T) {
	path, key := durableTestPath(t), durableTestKey(t, 0xda)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	sources, rows := seedDependencyMigrationFixture(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	steps := 0
	store, err = openDurableWithHook(path, key, OpenExisting, func() error {
		steps++
		if steps == 2 {
			return context.Canceled // retained first recap, erased second recap
		}
		return nil
	})
	if store != nil || !errors.Is(err, context.Canceled) || steps != 2 {
		t.Fatal("incomplete migration published a store or failed to interrupt after a bounded commit", steps, err)
	}
	steps = 0
	store, err = openDurableWithHook(path, key, OpenExisting, func() error { steps++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if steps != 2 {
		t.Fatal("restart rescanned the retained prefix or lost a pending summary", steps)
	}
	assertDependencyMigrationResult(t, store, sources, rows)
}

func TestChronicleDependencyMigrationRejectsMalformedMarker(t *testing.T) {
	for i, invalid := range [][]byte{nil, {0xff}, {1, 2}, {1, 0, 'x'}, append([]byte{1, 1}, "hour:20261106T10Z"...), {2, 3}, {2, 0, 'x'}, {2, 1, 'x'}, {2, 2, 'x'}} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			path, key := durableTestPath(t), durableTestKey(t, byte(0xdb+i))
			store, err := OpenDurable(path, key, CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Update(context.Background(), func(tx *Tx) error { return tx.PutIndexRecord("storage-meta", dependencyMigrationTestID, invalid) }); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = OpenDurable(path, key, OpenExisting)
			if store != nil || !errors.Is(err, ErrCorrupt) {
				t.Fatal("invalid authenticated migration state published a store", err)
			}
		})
	}
}

func TestChronicleDependencyMigrationDrainsMaximumLegacySingletonSources(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xe1)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	var ids []string
	var meetingSourceID string
	for i := range chronicle.MaxEvidence {
		text := fmt.Sprintf("Maximum legacy recap source %d", i)
		if i == 0 {
			text = "Meeting: planned the synthetic launch checklist."
		}
		source, err := capture.SanitizeObservation("editor.app", "", text, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservationAt(ctx, source, start.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, source.ContentID())
		if i == 0 {
			meetingSourceID = source.ContentID()
		}
	}
	sort.Strings(ids)
	row := chronicle.Summary{HourID: "hour:" + start.Format("20060102T15Z"), Start: start, End: start.Add(time.Hour), Text: "Maximum legacy generated recap.", SourceIDs: ids, FollowUp: &chronicle.FollowUp{SourceID: meetingSourceID, Text: "Who owns the synthetic launch checklist?"}}
	if err := store.PutChronicle(ctx, row); err != nil {
		t.Fatal(err)
	}
	date, _ := localDate(start.Add(time.Minute))
	snapshot, found, err := store.GetTodaySnapshot(ctx, date)
	if err != nil || !found || len(snapshot.Questions) != 1 {
		t.Fatal("legacy follow-up fixture missing", found, err)
	}
	// Older copies linked the generated question only to its displayed source.
	// Keep that exact compatibility case even when new writes link all inputs.
	snapshot.Questions[0].SourceIDs = []string{meetingSourceID}
	unrelated := ids[0]
	if unrelated == meetingSourceID {
		unrelated = ids[1]
	}
	snapshot.SourceIDs = []string{meetingSourceID, unrelated}
	sort.Strings(snapshot.SourceIDs)
	snapshot.RecentChanges = []today.Change{{At: start, Text: "Retained unrelated change.", SourceIDs: []string{unrelated}}}
	if err := store.PutTodaySnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := store.AnswerQuestion(ctx, date, snapshot.Questions[0].ID, "Mira owns the synthetic launch.", start.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	snapshot, found, err = store.GetTodaySnapshot(ctx, date)
	if err != nil || !found || len(snapshot.Questions) != 1 || snapshot.Questions[0].AnswerSourceID == "" {
		t.Fatal("authored answer fixture missing", found, err)
	}
	answerSourceID := snapshot.Questions[0].AnswerSourceID
	retainedChanges := mustJSON(snapshot.RecentChanges)
	defer clear(retainedChanges)
	if err := store.Update(ctx, func(tx *Tx) error {
		for _, id := range ids {
			if err := tx.deleteKnowledgeRecord(chronicleLinksNS, id); err != nil {
				return err
			}
			if err := tx.deleteKnowledgeRecord(chronicleLinksNS, id+":"+row.HourID); err != nil {
				return err
			}
			if err := tx.PutIndexRecord(chronicleSourceNS, id, mustJSON([]string{row.HourID})); err != nil {
				return err
			}
		}
		return tx.deleteKnowledgeRecord("storage-meta", dependencyMigrationTestID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal("maximum-source migration exceeded atomic deletion budget", err)
	}
	defer func() { _ = store.Close() }()
	if _, found, err := store.GetChronicle(ctx, row.HourID); err != nil || found {
		t.Fatal("legacy recap survived", found, err)
	}
	if snapshot, found, err := store.GetTodaySnapshot(ctx, date); err != nil || !found || len(snapshot.Questions) != 0 || !bytes.Equal(retainedChanges, mustJSON(snapshot.RecentChanges)) {
		t.Fatal("legacy generated follow-up survived, or unrelated Today content was erased", found, err)
	}
	if answer, found, err := store.GetObservation(ctx, answerSourceID); err != nil || !found || !strings.Contains(answer.Text(), "Mira owns the synthetic launch.") {
		t.Fatal("recap migration erased a raw authored answer", found, err)
	}
	for _, id := range ids {
		if _, found, err := store.GetObservation(ctx, id); err != nil || !found {
			t.Fatal("legacy raw source erased", found, err)
		}
		if _, found, err := store.readIndexValue(ctx, chronicleSourceNS, id, 8192); err != nil || found {
			t.Fatal("legacy reverse receipt survived", found, err)
		}
	}
}
