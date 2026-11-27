package durable

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

func putNamedEntity(t *testing.T, s *Store, domain, name string) {
	t.Helper()
	text := name + " owns the vendor contract renewal."
	chunk := enrichmentFixture(t, s, domain, text, derive.Proposal{Kind: knowledge.Person, Name: name, Fact: text})
	if err := s.ApplyEnrichment(context.Background(), chunk); err != nil {
		t.Fatal(name, err)
	}
}

func countPostings(t *testing.T, s *Store, kind byte) int {
	t.Helper()
	count := 0
	if err := s.viewWithReadLimit(context.Background(), bolt.MaxReadTransactionOps, func(reader bolt.Reader) error {
		var after []byte
		for {
			entries, more, err := reader.Scan([]byte{groupOrder, kind}, after, 512, 1<<20)
			if err != nil {
				return err
			}
			count += len(entries)
			if !more {
				return nil
			}
			after = entries[len(entries)-1].Key
		}
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func entityNames(entities []knowledge.Entity) []string {
	names := make([]string, 0, len(entities))
	for _, entity := range entities {
		names = append(names, entity.Name)
	}
	return names
}

func findNames(t *testing.T, s *Store, query string) []string {
	t.Helper()
	rows, _, err := s.FindInferredKnowledgeByNames(context.Background(), []string{query}, 16)
	if err != nil {
		t.Fatal(query, err)
	}
	return entityNames(rows)
}

func TestLibraryNameVariantsAfterLegacyBackfill(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0x7a)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	names := []string{"Threat-Bot", "José Müller", "Dima Petrov"}
	for _, name := range names {
		putNamedEntity(t, s, "work.example", name)
	}
	var entities []knowledge.Entity
	for _, name := range names {
		rows, err := s.FindInferredKnowledge(ctx, name)
		if err != nil || len(rows) != 1 {
			t.Fatal(name, rows, err)
		}
		entities = append(entities, rows[0])
	}
	// Legacy layout: v1 postings only, no token cursor, no retirement flag.
	if err := s.inferredUpdate(ctx, func(tx *Tx) error {
		for _, entity := range entities {
			if err := tx.deleteNamePostings(entity); err != nil {
				return err
			}
			if err := tx.putPosting(foldedPostings, foldedNameForm(entity.Name), entity.ID); err != nil {
				return err
			}
		}
		if err := tx.deleteInferredIndex(inferredMetaNS, foldedRetiredID); err != nil {
			return err
		}
		return tx.deleteInferredIndex(inferredMetaNS, nameTokenCursorID)
	}); err != nil {
		t.Fatal(err)
	}
	if countPostings(t, s, kindFoldedInferredName) != 3 || countPostings(t, s, kindTokenSetName) != 0 {
		t.Fatal("legacy layout was not restored")
	}
	reopen := func() {
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if s, err = OpenDurable(path, key, OpenExisting); err != nil {
			t.Fatal(err)
		}
	}
	reopen()
	if pending, err := s.LibraryNameIndexPending(ctx); err != nil || !pending {
		t.Fatal("legacy store not pending", pending, err)
	}
	for query, want := range map[string]string{"dima petrov": "Dima Petrov", "Threat-Bot": "Threat-Bot", "jose muller": "José Müller"} {
		if got := findNames(t, s, query); !slices.Equal(got, []string{want}) {
			t.Fatalf("during backfill %q: %v", query, got)
		}
	}
	if more, err := s.IndexPendingLibraryNames(ctx, 1); err != nil || !more {
		t.Fatal("first batch", more, err)
	}
	reopen()
	if pending, err := s.LibraryNameIndexPending(ctx); err != nil || !pending {
		t.Fatal("restart lost pending", pending, err)
	}
	for round := 0; round < 40; round++ {
		more, err := s.IndexPendingLibraryNames(ctx, 32)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	if pending, err := s.LibraryNameIndexPending(ctx); err != nil || pending {
		t.Fatal("still pending", pending, err)
	}
	if n := countPostings(t, s, kindFoldedInferredName); n != 0 {
		t.Fatalf("%d v1 postings survived retirement", n)
	}
	for query, want := range map[string]string{
		"threat bot": "Threat-Bot", "threatbot": "Threat-Bot", "Müller José": "José Müller",
		"Dima Petrov's": "Dima Petrov", "DIMA   petrov": "Dima Petrov",
	} {
		if got := findNames(t, s, query); !slices.Equal(got, []string{want}) {
			t.Fatalf("after backfill %q: %v", query, got)
		}
	}
	for query, want := range map[string]string{"Jose": "José Müller", "Dima": "Dima Petrov", "Petrov": "Dima Petrov"} {
		parts, _, err := s.FindInferredKnowledgeByNamePartsInScope(ctx, []string{query}, 16, nil, nil, nil, nil)
		if err != nil || !slices.Equal(entityNames(parts), []string{want}) {
			t.Fatalf("after backfill parts %q: %v %v", query, entityNames(parts), err)
		}
	}
}

func TestLibraryNameRetirementIsBounded(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x7b), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	putNamedEntity(t, s, "work.example", "Atlas")
	total := foldedRetireBatch + 2
	if err := s.inferredUpdate(ctx, func(tx *Tx) error {
		for i := 0; i < total; i++ {
			id := enrich.EntityID(knowledge.Person, fmt.Sprintf("ghost %d", i), "x", "y")
			if err := tx.putPosting(foldedPostings, fmt.Sprintf("ghost %d", i), id); err != nil {
				return err
			}
		}
		return tx.deleteInferredIndex(inferredMetaNS, foldedRetiredID)
	}); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.LibraryNameIndexPending(ctx); err != nil || !pending {
		t.Fatal("retirement not pending", pending, err)
	}
	if more, err := s.IndexPendingLibraryNames(ctx, 1); err != nil || !more {
		t.Fatal("retirement batch was unbounded", more, err)
	}
	if n := countPostings(t, s, kindFoldedInferredName); n != total-foldedRetireBatch {
		t.Fatalf("one batch left %d postings", n)
	}
	if more, err := s.IndexPendingLibraryNames(ctx, 32); err != nil || more {
		t.Fatal("retirement did not finish", more, err)
	}
	if n := countPostings(t, s, kindFoldedInferredName); n != 0 {
		t.Fatalf("%d v1 postings left", n)
	}
}

func TestLibraryNameDeletionRemovesEveryPosting(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x7c), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	putNamedEntity(t, s, "work.example", "Threat-Bot")
	putNamedEntity(t, s, "other.example", "Threat-Bot")
	putNamedEntity(t, s, "work.example", "José Müller")
	// Per entity: 1 token-set, 1 squashed, one posting per word.
	want := map[byte]int{kindTokenSetName: 3, kindSquashedName: 3, kindWordName: 6}
	for kind, n := range want {
		if got := countPostings(t, s, kind); got != n {
			t.Fatalf("kind %#x before delete: %d want %d", kind, got, n)
		}
	}
	rows, err := s.FindInferredKnowledge(ctx, "threatbot")
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	for _, row := range rows {
		if err := s.DeleteInferredKnowledge(ctx, row.ID); err != nil {
			t.Fatal(err)
		}
	}
	for kind, n := range map[byte]int{kindTokenSetName: 1, kindSquashedName: 1, kindWordName: 2} {
		if got := countPostings(t, s, kind); got != n {
			t.Fatalf("kind %#x after deleting two entities: %d want %d", kind, got, n)
		}
	}
	if err := s.DeleteAllKnowledge(ctx); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []byte{kindTokenSetName, kindSquashedName, kindWordName, kindFoldedInferredName} {
		if got := countPostings(t, s, kind); got != 0 {
			t.Fatalf("kind %#x left %d postings after delete-all", kind, got)
		}
	}
}

func TestLibraryNamePartialMatchesKeepIdentitiesSeparate(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x7d), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, name := range []string{"Dima Petrov", "Dima Ivanov", "Atlas", "Project Atlas Migration Plan", "Alpha Beta Gamma", "Alpha Beta Gamma Delta", "ThreatBot"} {
		putNamedEntity(t, s, "work.example", name)
	}
	for query, want := range map[string][]string{
		"Dima":                {"Dima Ivanov", "Dima Petrov"},
		"Dima Petrov":         {"Dima Petrov"},
		"Atlas":               {"Atlas"},
		"threat bot":          {"ThreatBot"},
		"Delta Gamma Beta":    {"Alpha Beta Gamma Delta"},
		"nothing":             nil,
		"d":                   nil,
		"dima j":              nil,
		"Dima Petrov Smithee": nil,
	} {
		exact, _, err := s.FindInferredKnowledgeByNames(ctx, []string{query}, 16)
		if err != nil {
			t.Fatal(query, err)
		}
		var have []string
		for _, entity := range exact {
			have = append(have, entity.ID)
		}
		parts, _, err := s.FindInferredKnowledgeByNamePartsInScope(ctx, []string{query}, 16, nil, nil, nil, have)
		if err != nil {
			t.Fatal(query, err)
		}
		got := append(entityNames(exact), entityNames(parts)...)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("%q: %v want %v", query, got, want)
		}
	}
	// Exact tier alone never returns name parts.
	if got := findNames(t, s, "Dima"); len(got) != 0 {
		t.Fatalf("exact tier returned name parts: %v", got)
	}
	// Writing a variant spelling never merges into the existing identity.
	putNamedEntity(t, s, "work.example", "Threat Bot")
	rows, err := s.FindInferredKnowledge(ctx, "threat bot")
	if err != nil || len(rows) != 2 || rows[0].ID == rows[1].ID {
		t.Fatalf("variant spelling merged or lost: %v %v", entityNames(rows), err)
	}
}

func TestLibraryNamePartsHonorKindExcludeScopeAndAmbiguity(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x7e), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	putNamedEntity(t, s, "work.example", "Dima A")
	putNamedEntity(t, s, "work.example", "Dima B")
	putNamedEntity(t, s, "other.example", "Dima C")
	parts := func(domains []string, kinds []knowledge.Kind, exclude []string) ([]knowledge.Entity, bool) {
		rows, more, err := s.FindInferredKnowledgeByNamePartsInScope(ctx, []string{"dima"}, 16, nil, domains, kinds, exclude)
		if err != nil {
			t.Fatal(err)
		}
		return rows, more
	}
	rows, more := parts(nil, nil, nil)
	if len(rows) != 3 || more {
		t.Fatalf("three people: %v %v", entityNames(rows), more)
	}
	if rows, _ := parts([]string{"work.example"}, nil, nil); len(rows) != 2 {
		t.Fatalf("scope: %v", entityNames(rows))
	}
	if rows, _ := parts(nil, []knowledge.Kind{knowledge.Project}, nil); len(rows) != 0 {
		t.Fatalf("kind: %v", entityNames(rows))
	}
	if rows, _ := parts(nil, []knowledge.Kind{knowledge.Person}, []string{rows[0].ID}); len(rows) != 2 {
		t.Fatalf("exclude: %v", entityNames(rows))
	}
	if limited, more, err := s.FindInferredKnowledgeByNamePartsInScope(ctx, []string{"dima"}, 2, nil, nil, nil, nil); err != nil || !more || len(limited) != 2 {
		t.Fatalf("limit: %d %v %v", len(limited), more, err)
	}
	for _, name := range []string{"Dima D", "Dima E", "Dima F", "Dima G", "Dima H", "Dima I"} {
		putNamedEntity(t, s, "work.example", name)
	}
	if rows, more := parts(nil, nil, nil); len(rows) != 0 || !more {
		t.Fatalf("nine people must be ambiguous: %d %v", len(rows), more)
	}
}

// A chunk carries at most derive.MaxSuggestions proposals (ApplyEnrichment
// validates it), so that is the most first-time entities one transaction adds.
func TestLibraryNameFirstTimeEntityWritesFitOneChunk(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		ctx := context.Background()
		s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x80), CreateNew)
		if err != nil {
			t.Fatal(err)
		}
		if legacy {
			putNamedEntity(t, s, "seed.example", "Seed") // Writes the cursors the next step removes.
			// A legacy-layout store has not retired v1, so v1 postings are written too.
			if err := s.inferredUpdate(ctx, func(tx *Tx) error {
				return tx.deleteInferredIndex(inferredMetaNS, foldedRetiredID)
			}); err != nil {
				t.Fatal(err)
			}
		}
		seeded := countPostings(t, s, kindWordName)
		v1Before := countPostings(t, s, kindFoldedInferredName)
		var proposals []derive.Proposal
		for i := 0; i < derive.MaxSuggestions; i++ {
			name := fmt.Sprintf("a%02d b%02d c%02d d%02d e%02d f%02d g%02d h%02d i%02d j%02d k%02d l%02d m%02d n%02d o%02d p%02d", i, i, i, i, i, i, i, i, i, i, i, i, i, i, i, i)
			proposals = append(proposals, derive.Proposal{Kind: knowledge.Topic, Name: name, Fact: "Shared fact."})
		}
		chunk := enrichmentFixture(t, s, "work.example", "Shared fact.", proposals...)
		if err := s.ApplyEnrichment(ctx, chunk); err != nil {
			t.Fatalf("legacy=%v: %v", legacy, err)
		}
		words := countPostings(t, s, kindWordName) - seeded
		if words != derive.MaxSuggestions*maxWordPostingTokens {
			t.Fatalf("legacy=%v: %d word postings", legacy, words)
		}
		v1 := countPostings(t, s, kindFoldedInferredName) - v1Before
		t.Logf("legacy=%v: %d name postings per first-time entity (%d of them v1)", legacy, (words+v1)/derive.MaxSuggestions+2, v1/derive.MaxSuggestions)
		_ = s.Close()
	}
}

func TestLibraryNameSharedTokenIsBoundedAndReported(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x7f), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for i := 0; i < enrich.MaxNameEntries+2; i++ {
		putNamedEntity(t, s, fmt.Sprintf("ctx%03d.example", i), fmt.Sprintf("Dima Surname%03d", i))
	}
	// A scope that matches nothing forces the whole bounded posting list to be
	// examined without filling the result limit.
	rows, more, err := s.FindInferredKnowledgeByNamePartsInScope(ctx, []string{"dima"}, 4, nil, []string{"none.example"}, nil, nil)
	if err != nil || !more || len(rows) != 0 {
		t.Fatalf("shared token: rows=%d more=%v err=%v", len(rows), more, err)
	}
	// A first name shared by too many entries identifies nobody and is
	// reported, but it must not hide the entries another part of the same
	// question identifies, even though its own posting list is cut short.
	rows, more, err = s.FindInferredKnowledgeByNamePartsInScope(ctx, []string{"dima", "dima surname001", "surname002"}, 4, nil, nil, nil, nil)
	if err != nil || !more || !slices.Equal(entityNames(rows), []string{"Dima Surname001", "Dima Surname002"}) {
		t.Fatalf("shared token beside identifying parts: rows=%v more=%v err=%v", entityNames(rows), more, err)
	}
	for i := 0; i < enrich.MaxNameEntries+2; i++ {
		name := fmt.Sprintf("Dima Surname%03d", i)
		rows, _, err := s.FindInferredKnowledgeByNamePartsInScope(ctx, []string{name}, 4, nil, nil, nil, nil)
		if err != nil || !slices.Equal(entityNames(rows), []string{name}) {
			t.Fatalf("truncated shared token hid %q: rows=%v err=%v", name, entityNames(rows), err)
		}
	}
	// The exact tier is untouched by a shared first name.
	if rows, _, err := s.FindInferredKnowledgeByNames(ctx, []string{"dima"}, 4); err != nil || len(rows) != 0 {
		t.Fatalf("exact tier: rows=%d err=%v", len(rows), err)
	}
}
