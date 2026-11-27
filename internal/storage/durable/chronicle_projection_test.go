package durable

import (
	"context"
	"testing"
)

func TestChronicleProjectionReadsTrackMutationsAndManifestAcrossReopen(t *testing.T) {
	ctx := context.Background()
	s, path, _ := rollupTestStore(t, 0xe8)
	if err := s.Update(ctx, func(tx *Tx) error {
		projection := newChronicleProjectionWriter(tx.writer)
		tx.writer = projection
		defer func() { tx.writer = projection.Writer; projection.cache.close() }()
		for _, value := range []string{"first", "replacement", "" /* delete */, "recreated"} {
			if _, _, err := tx.readIndexValue("projection-test", "record", 64); err != nil {
				return err
			}
			if value == "" {
				if err := tx.deleteKnowledgeRecord("projection-test", "record"); err != nil {
					return err
				}
			} else if err := tx.PutIndexRecord("projection-test", "record", []byte(value)); err != nil {
				return err
			}
			for n := 0; n < 2; n++ {
				got, found, err := tx.readIndexValue("projection-test", "record", 64)
				if err != nil || found != (value != "") || string(got) != value {
					t.Fatalf("stale transaction value after mutation: %q found%v err%v want%q", got, found, err, value)
				}
				clear(got)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenDurable(path, durableTestKey(t, 0xe8), OpenExisting)
	if err != nil {
		t.Fatalf("projection mutations damaged encrypted manifest: %v", err)
	}
	defer s.Close()
	if err := s.Update(ctx, func(tx *Tx) error {
		got, found, err := tx.readIndexValue("projection-test", "record", 64)
		defer clear(got)
		if err != nil || !found || string(got) != "recreated" {
			t.Fatalf("projection did not survive reopen: %q found%v err%v", got, found, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
