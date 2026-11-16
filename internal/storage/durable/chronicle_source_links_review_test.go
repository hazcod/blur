package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

func TestChronicleLinksMaximumSourcesReplacementAndResumableErasure(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xd7)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	observations := make([]capture.SanitizedObservation, chronicle.MaxEvidence)
	ids := make([]string, len(observations))
	for i := range observations {
		observations[i], err = capture.SanitizeObservation("editor.app", "", fmt.Sprintf("Synthetic source %d for maximum-source recap", i), 1024)
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = observations[i].ContentID()
		if err := store.PutObservationAt(ctx, observations[i], start.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(ids)
	summaries := make([]chronicle.Summary, 3)
	for hour := range summaries {
		at := start.Add(time.Duration(hour) * time.Hour)
		if hour > 0 {
			for _, observation := range observations {
				if err := store.RecordObservationSighting(ctx, observation, at.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
			}
		}
		summary := chronicle.Summary{HourID: "hour:" + at.Format("20060102T15Z"), Start: at, End: at.Add(time.Hour), Text: "Reviewed all synthetic sources.", SourceIDs: ids}
		if err := store.PutChronicle(ctx, summary); err != nil {
			t.Fatal("maximum-source recap save", hour, err)
		}
		// An accepted response may be replayed; retained reverse links must not
		// be unlinked and rebuilt beyond the transaction's operation budget.
		summary.Text = "Reviewed all synthetic sources, with updated summary wording."
		if err := store.PutChronicle(ctx, summary); err != nil {
			t.Fatal("maximum-source recap replacement", hour, err)
		}
		summaries[hour] = summary
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	// Removing a middle hour updates both linked neighbors for every source.
	if err := store.DeleteChronicle(ctx, summaries[1].HourID); err != nil {
		t.Fatal("maximum-source middle-hour deletion", err)
	}
	interrupted := false
	err = store.deleteSourceStaged(ctx, ids[0], func() error {
		journal, found, err := store.readSourceErasureLocked(ctx)
		if err != nil || !found {
			t.Fatal("missing erasure checkpoint", err)
		}
		if journal.Phase != 5 {
			return nil
		}
		var root chronicleLinkRoot
		err = store.state.db.View(ctx, func(reader bolt.Reader) error {
			raw, found, err := store.readIndexValueReader(reader, chronicleLinksNS, ids[0], 512)
			defer clear(raw)
			if err != nil || !found {
				return err
			}
			return json.Unmarshal(raw, &root)
		})
		if err != nil {
			t.Fatal(err)
		}
		if root.Count == 1 {
			interrupted = true
			return context.Canceled
		}
		return nil
	})
	if !interrupted || !errors.Is(err, context.Canceled) {
		t.Fatal("did not interrupt between linked recap erasure batches", err)
	}
	if _, _, err := store.GetChronicle(ctx, summaries[2].HourID); !errors.Is(err, ErrDeletionPending) {
		t.Fatal("partial recap erasure escaped the memory gate", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal("reopen failed to resume linked recap erasure", err)
	}
	for _, summary := range summaries {
		if _, found, err := store.GetChronicle(ctx, summary.HourID); err != nil || found {
			t.Fatal("source-derived recap survived restart", found, err)
		}
		if err := store.PutChronicle(ctx, summary); err == nil {
			t.Fatal("late recap replay resurrected an erased source")
		}
	}
	if _, found, err := store.GetObservation(ctx, ids[0]); err != nil || found {
		t.Fatal("erased primary source survived", found, err)
	}
	if _, found, err := store.GetObservation(ctx, ids[1]); err != nil || !found {
		t.Fatal("unrelated primary source was erased", found, err)
	}
	for _, id := range ids {
		if _, found, err := store.readIndexValue(ctx, chronicleLinksNS, id, 512); err != nil || found {
			t.Fatal("empty reverse-link root survived recap erasure", found, err)
		}
	}
}

func TestChronicleLinksMigrateLegacySingletonAndEraseRevisits(t *testing.T) {
	ctx := context.Background()
	store := openReplicaTestStore(t)
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	observation, err := capture.SanitizeObservation("editor.app", "", "Synthetic legacy recap evidence", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, observation, start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	first := chronicle.Summary{HourID: "hour:" + start.Format("20060102T15Z"), Start: start, End: start.Add(time.Hour), Text: "Reviewed the synthetic source.", SourceIDs: []string{observation.ContentID()}}
	if err := store.PutChronicle(ctx, first); err != nil {
		t.Fatal(err)
	}
	// Recreate the authenticated singleton reverse receipt used before links.
	if err := store.Update(ctx, func(tx *Tx) error {
		if err := tx.deleteKnowledgeRecord(chronicleLinksNS, observation.ContentID()); err != nil {
			return err
		}
		if err := tx.deleteKnowledgeRecord(chronicleLinksNS, observation.ContentID()+":"+first.HourID); err != nil {
			return err
		}
		return tx.PutIndexRecord(chronicleSourceNS, observation.ContentID(), mustJSON([]string{first.HourID}))
	}); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Start, second.End = start.Add(time.Hour), start.Add(2*time.Hour)
	second.HourID = "hour:" + second.Start.Format("20060102T15Z")
	if err := store.RecordObservationSighting(ctx, observation, second.Start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutChronicle(ctx, second); err != nil {
		t.Fatal("legacy singleton prevented a valid revisit recap", err)
	}
	if _, found, err := store.readIndexValue(ctx, chronicleSourceNS, observation.ContentID(), 8192); err != nil || found {
		t.Fatal("legacy reverse receipt retained after atomic migration", found, err)
	}
	if err := store.DeleteObservationRecord(ctx, observation.ContentID()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first.HourID, second.HourID} {
		if _, found, err := store.GetChronicle(ctx, id); err != nil || found {
			t.Fatal("migrated source recap survived erasure", found, err)
		}
	}
}
