package durable

import (
	"bytes"
	"context"
	"testing"

	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

func schemaFixturePrimaryBytes(t *testing.T, path string) []byte {
	t.Helper()
	db, err := bolt.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(db.Close)
	var primary []byte
	if err := db.View(context.Background(), func(reader bolt.Reader) error {
		entries, more, err := reader.Scan(observationRecordPrefix, nil, 2, 1<<20)
		defer clearBoltEntries(entries)
		if err != nil {
			return err
		}
		if more || len(entries) != 1 {
			t.Fatal("unexpected historical primary fixture")
		}
		primary = bytes.Clone(entries[0].Value)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return primary
}

// Both fixture transformations must reseal every value, including metadata
// introduced after the fixture's schema. An unknown namespace fails the fixture
// instead of silently retaining a value authenticated at the current schema.
func schemaFixtureRecordAtVersion(t *testing.T, s *Store, entry bolt.Entry, version uint32) ([]byte, error) {
	t.Helper()
	switch {
	case bytes.Equal(entry.Key, identityKey):
		return s.sealIdentity(s.state.storeID, version)
	case bytes.Equal(entry.Key, schemaKey):
		plain := encodeSchema(version)
		defer clear(plain)
		return s.sealAtSchema(entry.Key, "schema", plain, version)
	}
	namespaces := []string{"observations", "observation-size"}
	for _, namespace := range []string{"search-index-tree-v4", "search-index-reverse-v4", "search-index-state-v1", "search-pending-v1", "search-proof-v1", "search-source-page-v1"} {
		namespaces = append(namespaces, namespace, "capture-part."+namespace)
	}
	for _, namespace := range []string{captureTimeMigrationNamespace, projectionIndexNamespace, observationTimelineNS, observationTimelineSourceNS, captureHourNS, captureHourSourceNS, erasureNamespace, captureNS, deletionCutoffNS, deletionChangeNS, observationSightingNS, importedNS, tombstoneNS} {
		namespaces = append(namespaces, "index:"+namespace)
	}
	for _, namespace := range namespaces {
		plain, err := s.open(entry.Key, namespace, entry.Value)
		if err != nil {
			continue
		}
		replacement, err := s.sealAtSchema(entry.Key, namespace, plain, version)
		clear(plain)
		return replacement, err
	}
	t.Fatal("schema fixture contains an unrecognised authenticated namespace")
	return nil, ErrCorrupt
}
