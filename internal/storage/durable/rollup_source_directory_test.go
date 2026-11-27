package durable

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestRollupSourceDirectoryRejectsMalformedEncodings(t *testing.T) {
	bad := [][]byte{
		{}, {0}, {1}, {1, 0, 0}, {1, 0, 0, 0},
		{1, 0, 0, 1, 0, 0, 2}, // repeated byte index
		{1, 0, 1, 1, 0, 0, 1}, // descending indices
		{1, 4, 226, 1},        // byte index1250 is outside the directory
		{2, 1},
		append([]byte{2}, make([]byte, rollupSourceDirectoryBytes)...), // empty dense
		{3, 0, 0, 1},
	}
	s, _, _ := rollupTestStore(t, 0xcd)
	for n, raw := range bad {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			if err := s.Update(context.Background(), func(tx *Tx) error {
				if err := tx.PutIndexRecord(rollupSourceYearNS, "directory:fixture", raw); err != nil {
					return err
				}
				if _, found, err := s.rollupSourceDirectory(tx.writer, "fixture"); err != ErrCorrupt || found {
					t.Fatalf("malformed directory accepted: found%v err%v", found, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRollupSourceErasureDrainsMixedLegacyAndYearReferences(t *testing.T) {
	ctx := context.Background()
	s, path, _ := rollupTestStore(t, 0xce)
	source := rollupTestSource(t, s, "A recurring source survives both index representations.", time.Date(2018, 2, 6, 9, 0, 0, 0, time.UTC))
	legacy := rollupTestRecord(t, "week:2018-W06", "- Legacy weekly evidence.", []string{"day:2018-02-06"}, []string{source})
	installLegacyRollupForTest(t, s, legacy)
	current := rollupTestRecord(t, "day:2020-02-29", "- Leap day evidence.", []string{"hour:20200229T09Z"}, []string{source})
	if err := s.PutChronicleRollup(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = OpenDurable(path, durableTestKey(t, 0xce), OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.DeleteObservationRecord(ctx, source); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{legacy.ID, current.ID} {
		r, found, err := s.GetChronicleRollup(ctx, id)
		if err != nil || !found || r.Text != "" || len(r.SourceIDs) != 0 || !r.Dirty {
			t.Fatalf("mixed representation retained erased evidence: %+v %v %v", r, found, err)
		}
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		if ids, found, err := tx.readRollupSources(source); err != nil || found || len(ids) != 0 {
			t.Fatalf("legacy references remained: %v %v %v", ids, found, err)
		}
		if _, found, err := s.rollupSourceDirectory(tx.writer, source); err != nil || found {
			t.Fatalf("year references remained: %v %v", found, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
