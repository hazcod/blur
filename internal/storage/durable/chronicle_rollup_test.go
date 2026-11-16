package durable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
)

const rollupTestZone = "Europe/Amsterdam"

func rollupTestStore(t *testing.T, seed byte) (*Store, string, [32]byte) {
	t.Helper()
	path := durableTestPath(t)
	store, err := OpenDurable(path, durableTestKey(t, seed), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.SetChronicleZone(func() string { return rollupTestZone })
	return store, path, [32]byte{}
}

func rollupTestSource(t *testing.T, store *Store, text string, at time.Time) string {
	t.Helper()
	observation, err := capture.SanitizeObservation("editor.app", "", text, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(context.Background(), observation, at); err != nil {
		t.Fatal(err)
	}
	return observation.ContentID()
}

func rollupTestRecord(t *testing.T, id, text string, children, sources []string) chronicle.Rollup {
	t.Helper()
	rollup, err := chronicle.NewRollupPlaceholder(id, rollupTestZone)
	if err != nil {
		t.Fatal(err)
	}
	if text != "" {
		sum := sha256.Sum256([]byte(text))
		rollup.Text, rollup.ChildIDs, rollup.SourceIDs = text, children, sources
		rollup.GeneratedAt, rollup.Model, rollup.InputHash, rollup.Dirty = time.Date(2026, 11, 16, 6, 0, 0, 0, time.UTC), "local-model", hex.EncodeToString(sum[:]), false
	}
	return rollup
}

func TestChronicleRollupRoundTripBoundsAndReopen(t *testing.T) {
	ctx := context.Background()
	store, path, _ := rollupTestStore(t, 0xB1)
	at := time.Date(2026, 11, 15, 9, 30, 0, 0, time.UTC)
	source := rollupTestSource(t, store, "Reviewed the quarterly plan", at)
	day := rollupTestRecord(t, "day:2026-11-15", "- Reviewed the plan.", []string{"hour:20261115T09Z"}, []string{source})
	if err := store.PutChronicleRollup(ctx, day); err != nil {
		t.Fatal(err)
	}
	week := rollupTestRecord(t, "week:2026-W46", "- A planning week.", []string{"day:2026-11-15"}, []string{source})
	if err := store.PutChronicleRollup(ctx, week); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.GetChronicleRollup(ctx, "day:2026-11-15")
	if err != nil || !found || got.Text != day.Text || got.Zone != rollupTestZone || got.Level != "day" || got.PeriodKey != "2026-11-15" || got.Dirty || len(got.SourceIDs) != 1 || got.SourceIDs[0] != source || !got.Start.Equal(day.Start) || !got.End.Equal(day.End) {
		t.Fatalf("day rollup round trip = %+v found=%t err=%v", got, found, err)
	}
	if _, found, err := store.GetChronicleRollup(ctx, "day:2026-11-14"); err != nil || found {
		t.Fatalf("absent rollup found=%t err=%v", found, err)
	}
	start, end := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	days, next, err := store.ListChronicleRollups(ctx, "day", start, end, "", 10)
	if err != nil || next != "" || len(days) != 1 || days[0].ID != "day:2026-11-15" {
		t.Fatalf("day list = %+v next=%q err=%v", days, next, err)
	}
	weeks, _, err := store.ListChronicleRollups(ctx, "week", start, end, "", 10)
	if err != nil || len(weeks) != 1 || weeks[0].ID != "week:2026-W46" {
		t.Fatalf("week list = %+v err=%v", weeks, err)
	}
	// Pages never repeat or skip rollups.
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("day:2026-11-%02d", i)
		if err := store.PutChronicleRollup(ctx, rollupTestRecord(t, id, "", nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	first, cursor, err := store.ListChronicleRollups(ctx, "day", start, end, "", 2)
	if err != nil || len(first) != 2 || cursor != first[1].ID || first[0].ID != "day:2026-11-01" {
		t.Fatalf("first page = %+v cursor=%q err=%v", first, cursor, err)
	}
	rest, cursor, err := store.ListChronicleRollups(ctx, "day", start, end, cursor, 10)
	if err != nil || cursor != "" || len(rest) != 2 || rest[0].ID != "day:2026-11-03" || rest[1].ID != "day:2026-11-15" {
		t.Fatalf("second page = %+v cursor=%q err=%v", rest, cursor, err)
	}

	invalid := map[string]func(*chronicle.Rollup){
		"oversized text":   func(r *chronicle.Rollup) { r.Text = strings.Repeat("a", chronicle.MaxRollupTextBytes+1) },
		"control text":     func(r *chronicle.Rollup) { r.Text = "bad\x00text" },
		"invalid utf8":     func(r *chronicle.Rollup) { r.Text = "bad\xfftext" },
		"no sources":       func(r *chronicle.Rollup) { r.SourceIDs = nil },
		"unsorted sources": func(r *chronicle.Rollup) { r.SourceIDs = []string{strings.Repeat("f", 64), source} },
		"bad child":        func(r *chronicle.Rollup) { r.ChildIDs = []string{"not-an-hour"} },
		"too many sources": func(r *chronicle.Rollup) { r.SourceIDs = manySourceIDs(chronicle.MaxRollupSources + 1) },
		"too many children": func(r *chronicle.Rollup) {
			r.ChildIDs = nil
			for i := 0; i <= chronicle.MaxRollupChildren; i++ {
				r.ChildIDs = append(r.ChildIDs, fmt.Sprintf("hour:202611%02dT%02dZ", 1+i/24, i%24))
			}
		},
		"wrong period": func(r *chronicle.Rollup) { r.End = r.End.Add(time.Hour) },
		"bad level":    func(r *chronicle.Rollup) { r.Level = "month" },
		"bad hash":     func(r *chronicle.Rollup) { r.InputHash = "abc" },
		"bad zone":     func(r *chronicle.Rollup) { r.Zone = "Mars/Base" },
	}
	for name, mutate := range invalid {
		candidate := rollupTestRecord(t, "day:2026-11-15", "- Reviewed the plan.", []string{"hour:20261115T09Z"}, []string{source})
		mutate(&candidate)
		if err := store.PutChronicleRollup(ctx, candidate); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	unknownSource := rollupTestRecord(t, "day:2026-11-14", "- Something.", []string{"hour:20261114T09Z"}, []string{strings.Repeat("1", 64)})
	if err := store.PutChronicleRollup(ctx, unknownSource); err == nil {
		t.Error("a rollup citing a nonexistent source was accepted")
	}
	for _, id := range []string{"day:2026-13-01", "week:2026-W60", "hour:20261115T09Z", ""} {
		if _, _, err := store.GetChronicleRollup(ctx, id); err == nil {
			t.Errorf("malformed ID %q was accepted", id)
		}
	}
	for name, call := range map[string]func() error{
		"limit zero":    func() error { _, _, err := store.ListChronicleRollups(ctx, "day", start, end, "", 0); return err },
		"limit over 64": func() error { _, _, err := store.ListChronicleRollups(ctx, "day", start, end, "", 65); return err },
		"bad level":     func() error { _, _, err := store.ListChronicleRollups(ctx, "hour", start, end, "", 5); return err },
		"empty range":   func() error { _, _, err := store.ListChronicleRollups(ctx, "day", end, start, "", 5); return err },
		"range too long": func() error {
			_, _, err := store.ListChronicleRollups(ctx, "day", start, start.AddDate(2, 0, 0), "", 5)
			return err
		},
		"cursor level": func() error {
			_, _, err := store.ListChronicleRollups(ctx, "day", start, end, "week:2026-W46", 5)
			return err
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}

	// A store written with rollups reopens unchanged at the same schema.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDurable(path, durableTestKey(t, 0xB1), OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if OpenSchemaVersion != 7 {
		t.Fatalf("schema version changed to %d", OpenSchemaVersion)
	}
	again, found, err := reopened.GetChronicleRollup(ctx, "week:2026-W46")
	if err != nil || !found || again.Text != week.Text {
		t.Fatalf("reopened rollup = %+v found=%t err=%v", again, found, err)
	}
}

func manySourceIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		sum := sha256.Sum256([]byte(fmt.Sprint(i)))
		ids[i] = hex.EncodeToString(sum[:])
	}
	// Sorted unique by construction after sorting.
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j-1] > ids[j]; j-- {
			ids[j-1], ids[j] = ids[j], ids[j-1]
		}
	}
	return ids
}

func TestChronicleHourWritesMarkLocalDayRollupDirty(t *testing.T) {
	ctx := context.Background()
	store, _, _ := rollupTestStore(t, 0xB2)
	// 23:30 UTC on 15 November is 00:30 on 16 November in Amsterdam.
	start := time.Date(2026, 11, 15, 23, 0, 0, 0, time.UTC)
	source := rollupTestSource(t, store, "Late night review", start.Add(30*time.Minute))
	hour := chronicle.Summary{HourID: "hour:20261115T23Z", Start: start, End: start.Add(time.Hour), Text: "Late review.", SourceIDs: []string{source}}
	if err := store.PutChronicle(ctx, hour); err != nil {
		t.Fatal(err)
	}
	placeholder, found, err := store.GetChronicleRollup(ctx, "day:2026-11-16")
	if err != nil || !found || !placeholder.Dirty || placeholder.Text != "" || placeholder.Zone != rollupTestZone {
		t.Fatalf("hour write did not create the local-day placeholder: %+v found=%t err=%v", placeholder, found, err)
	}
	if _, found, _ := store.GetChronicleRollup(ctx, "day:2026-11-15"); found {
		t.Fatal("hour was dated by UTC instead of the recap zone")
	}
	clean := rollupTestRecord(t, "day:2026-11-16", "- Late review.", []string{hour.HourID}, []string{source})
	if err := store.PutChronicleRollup(ctx, clean); err != nil {
		t.Fatal(err)
	}
	if err := store.PutChronicle(ctx, hour); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := store.GetChronicleRollup(ctx, "day:2026-11-16"); !got.Dirty || got.Text != clean.Text {
		t.Fatalf("a recap write did not mark the generated day dirty and keep it readable: %+v", got)
	}
	if err := store.PutChronicleRollup(ctx, clean); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteChronicle(ctx, hour.HourID); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := store.GetChronicleRollup(ctx, "day:2026-11-16"); !got.Dirty {
		t.Fatalf("deleting a recap did not mark its day dirty: %+v", got)
	}
}

func TestChronicleRollupDeletionFenceDropsSourceAndMarksDirty(t *testing.T) {
	ctx := context.Background()
	store, _, _ := rollupTestStore(t, 0xB3)
	start := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	doomed := rollupTestSource(t, store, "Secret planning notes for the doomed capture", start.Add(time.Minute))
	kept := rollupTestSource(t, store, "Unrelated kept capture", start.Add(2*time.Minute))
	sources := []string{doomed, kept}
	if sources[0] > sources[1] {
		sources[0], sources[1] = sources[1], sources[0]
	}
	hour := chronicle.Summary{HourID: "hour:20261115T09Z", Start: start, End: start.Add(time.Hour), Text: "Worked on notes.", SourceIDs: sources}
	if err := store.PutChronicle(ctx, hour); err != nil {
		t.Fatal(err)
	}
	day := rollupTestRecord(t, "day:2026-11-15", "- Planning notes.", []string{hour.HourID}, sources)
	week := rollupTestRecord(t, "week:2026-W46", "- A planning week.", []string{"day:2026-11-15"}, sources)
	for _, rollup := range []chronicle.Rollup{day, week} {
		if err := store.PutChronicleRollup(ctx, rollup); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteObservationRecord(ctx, doomed); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"day:2026-11-15", "week:2026-W46"} {
		got, found, err := store.GetChronicleRollup(ctx, id)
		if err != nil || !found {
			t.Fatalf("%s found=%t err=%v", id, found, err)
		}
		for _, source := range got.SourceIDs {
			if source == doomed {
				t.Fatalf("%s still cites the erased source", id)
			}
		}
		if !got.Dirty || got.Text != "" || got.InputHash != "" || !got.GeneratedAt.IsZero() {
			t.Fatalf("%s kept derived text after its source was erased: %+v", id, got)
		}
	}
	// The reverse index no longer points at the erased source.
	if err := store.Update(ctx, func(tx *Tx) error {
		ids, found, err := tx.readRollupSources(doomed)
		if err != nil || found || len(ids) != 0 {
			t.Fatalf("reverse source index survived erasure: %v found=%t err=%v", ids, found, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteAllChroniclesRemovesRollups(t *testing.T) {
	ctx := context.Background()
	store, _, _ := rollupTestStore(t, 0xB4)
	start := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	source := rollupTestSource(t, store, "Planned the release", start.Add(time.Minute))
	hour := chronicle.Summary{HourID: "hour:20261115T09Z", Start: start, End: start.Add(time.Hour), Text: "Planned.", SourceIDs: []string{source}}
	if err := store.PutChronicle(ctx, hour); err != nil {
		t.Fatal(err)
	}
	if err := store.PutChronicleRollup(ctx, rollupTestRecord(t, "day:2026-11-15", "- Planned.", []string{hour.HourID}, []string{source})); err != nil {
		t.Fatal(err)
	}
	if err := store.PutChronicleRollup(ctx, rollupTestRecord(t, "week:2026-W46", "- A week.", []string{"day:2026-11-15"}, []string{source})); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAllChronicles(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"day:2026-11-15", "week:2026-W46"} {
		if _, found, err := store.GetChronicleRollup(ctx, id); err != nil || found {
			t.Fatalf("%s survived deleting all recaps: found=%t err=%v", id, found, err)
		}
	}
	rows, _, err := store.ListChronicleRollups(ctx, "day", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), "", 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rollup list after delete all = %+v err=%v", rows, err)
	}
}
