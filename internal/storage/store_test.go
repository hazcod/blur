package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nielsgroen/blur/internal/capture"
	"github.com/nielsgroen/blur/internal/privacy"
)

func observation(id string) capture.SanitizedObservation {
	o, err := capture.SanitizeObservation("example.app", "example.com", "password: hunter2\n"+id, 256)
	if err != nil {
		panic(err)
	}
	return o
}
func digest(c byte) string { return strings.Repeat(string(c), 64) }
func idFor(c byte) string  { return observation(digest(c)).ContentID() }

func TestObservationCannotBeForgedOutsideCapture(t *testing.T) {
	shape := reflect.TypeOf(capture.SanitizedObservation{})
	for i := 0; i < shape.NumField(); i++ {
		if shape.Field(i).IsExported() {
			t.Fatalf("exported observation field: %s", shape.Field(i).Name)
		}
	}
	if _, exists := shape.MethodByName("CanonicalID"); exists {
		t.Fatal("public canonical ID setter path")
	}
	constructor := reflect.TypeOf(capture.SanitizeObservation)
	if constructor.NumIn() != 4 || constructor.In(0).Kind() != reflect.String || constructor.In(1).Kind() != reflect.String || constructor.In(2).Kind() != reflect.String || constructor.In(3).Kind() != reflect.Int {
		t.Fatal("constructor accepts caller-supplied identity or counts")
	}
	var decoded capture.SanitizedObservation
	if err := json.Unmarshal([]byte(`{"content_id":"untrusted"}`), &decoded); err == nil {
		t.Fatal("untrusted wire JSON constructed observation")
	}
	encoded, err := json.Marshal(observation(digest('a')))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"content_id", "app_id", "domain", "text", "truncated", "redaction_version", "redaction_counts"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("missing sanitized JSON field %s", name)
		}
	}
	if len(fields) != 7 || strings.Contains(string(encoded), "hunter2") {
		t.Fatal("JSON exposed extra or raw fields")
	}
}

func TestInvalidObservationReturnsBeforeExclusiveStoreLock(t *testing.T) {
	s := NewEphemeralMemoryStore()
	s.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- s.PutObservation(context.Background(), capture.SanitizedObservation{}) }()
	select {
	case err := <-done:
		s.mu.Unlock()
		if !isStorageError(err, Invalid) {
			t.Fatalf("invalid value: %v", err)
		}
	case <-time.After(time.Second):
		s.mu.Unlock()
		t.Fatal("invalid observation waited for exclusive store lock")
	}
}

func TestObservationBoundaryRejectsMalformedAndDuplicates(t *testing.T) {
	ctx := context.Background()
	s := NewEphemeralMemoryStore()
	base := observation(digest('a'))
	if err := s.PutObservation(ctx, capture.SanitizedObservation{}); !isStorageError(err, Invalid) {
		t.Fatalf("zero observation: %v", err)
	}
	for _, raw := range []string{"\xff", strings.Repeat("x", MaxObservationTextBytes+1)} {
		if _, err := capture.SanitizeObservation("example.app", "example.com", raw, MaxObservationTextBytes); err == nil {
			t.Fatal("malformed raw text accepted")
		}
	}
	if err := s.PutObservation(ctx, base); err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservation(ctx, base); !isStorageError(err, Duplicate) {
		t.Fatalf("duplicate: %v", err)
	}
}

func TestObservationListIsBoundedOrderedAndCopied(t *testing.T) {
	ctx := context.Background()
	s := NewEphemeralMemoryStore()
	for _, c := range []byte{'c', 'a', 'b'} {
		if err := s.PutObservation(ctx, observation(digest(c))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ListObservations(ctx, 0, ""); !isStorageError(err, Invalid) {
		t.Fatalf("zero limit: %v", err)
	}
	if _, err := s.ListObservations(ctx, MaxListLimit+1, ""); !isStorageError(err, Invalid) {
		t.Fatalf("large limit: %v", err)
	}
	got, err := s.ListObservations(ctx, 2, "")
	ids := []string{idFor('a'), idFor('b'), idFor('c')}
	sort.Strings(ids)
	if err != nil || len(got) != 2 || got[0].ContentID() != ids[0] || got[1].ContentID() != ids[1] {
		t.Fatalf("first page: %v %v", len(got), err)
	}
	counts := got[0].RedactionCounts()
	counts[0].Rule = "BROKEN"
	x, ok, err := s.GetObservation(ctx, ids[0])
	if err != nil || !ok || x.RedactionCounts()[0].Rule != "SECRET" {
		t.Fatalf("get: %+v %t %v", x, ok, err)
	}
	page, err := s.ListObservations(ctx, 2, ids[1])
	if err != nil || len(page) != 1 || page[0].ContentID() != ids[2] {
		t.Fatalf("second page: %v %v", len(page), err)
	}
	if _, ok, err := s.GetObservation(ctx, digest('f')); err != nil || ok {
		t.Fatalf("missing: %t %v", ok, err)
	}
}

func TestObservationWriteCopiesCallerData(t *testing.T) {
	s := NewEphemeralMemoryStore()
	o := observation(digest('a'))
	if err := s.PutObservation(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	counts := o.RedactionCounts()
	counts[0].Rule = "CHANGED"
	got, ok, err := s.GetObservation(context.Background(), idFor('a'))
	if err != nil || !ok || got.RedactionCounts()[0].Rule != "SECRET" {
		t.Fatalf("copy failed: found=%t err=%v", ok, err)
	}
}

func TestObservationFromTargetWithoutAppIdentityIsAccepted(t *testing.T) {
	s := NewEphemeralMemoryStore()
	o, err := capture.SanitizeObservation("", "example.com", "safe text", 256)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservation(context.Background(), o); err != nil {
		t.Fatal(err)
	}
}

func TestStorageCancellationCloseAndReport(t *testing.T) {
	s := NewEphemeralMemoryStore()
	if s.EncryptedAtRest() {
		t.Fatal("memory backend claimed encryption")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.PutObservation(ctx, observation(digest('a'))); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservation(context.Background(), observation(digest('a'))); !isStorageError(err, Closed) {
		t.Fatal(err)
	}
	if _, _, err := s.GetObservation(context.Background(), digest('a')); !isStorageError(err, Closed) {
		t.Fatal(err)
	}
}

func derivation(id, gen string, state DerivationState) Derivation {
	return Derivation{ID: id, GenerationID: gen, State: state, SourceIDs: []string{idFor('a')}, SourceHashes: []string{digest('b')}, ProcessorVersion: 1, SchemaVersion: 1, PromptVersion: 1, RedactionVersion: privacy.Version, ProviderModelDigest: digest('c'), OutputHash: digest('d'), CoveredSources: 1, OmittedSources: 0, InputTokens: 12, OutputTokens: 4, Latency: time.Millisecond}
}

func TestDerivationAndGenerationAreImmutableAndActivationNeedsCompleteEvidence(t *testing.T) {
	ctx := context.Background()
	s := NewEphemeralMemoryStore()
	if err := s.PutGeneration(ctx, Generation{ID: "g1", DerivationIDs: []string{"d1"}, SchemaVersion: 1, RedactionVersion: privacy.Version}); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateGeneration(ctx, "g1"); !isStorageError(err, Incomplete) {
		t.Fatalf("missing derivation: %v", err)
	}
	d := derivation("d1", "g1", Building)
	if err := s.PutDerivation(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateGeneration(ctx, "g1"); !isStorageError(err, Incomplete) {
		t.Fatalf("building derivation: %v", err)
	}
	if err := s.PutDerivation(ctx, d); !isStorageError(err, Duplicate) {
		t.Fatalf("duplicate derivation: %v", err)
	}
	if err := s.PutGeneration(ctx, Generation{ID: "g1", DerivationIDs: []string{"d1"}, SchemaVersion: 1, RedactionVersion: privacy.Version}); !isStorageError(err, Duplicate) {
		t.Fatalf("duplicate generation: %v", err)
	}
	active := derivation("d2", "g2", Active)
	if err := s.PutObservation(ctx, observation(digest('a'))); err != nil {
		t.Fatal(err)
	}
	g := Generation{ID: "g2", DerivationIDs: []string{"d2"}, SchemaVersion: 1, RedactionVersion: privacy.Version}
	if err := s.PutGeneration(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDerivation(ctx, active); err != nil {
		t.Fatal(err)
	}
	active.SourceIDs[0] = "mutated"
	g.DerivationIDs[0] = "mutated"
	if err := s.ActivateGeneration(ctx, "g2"); err != nil {
		t.Fatal(err)
	}
	selected, ok, err := s.ActiveGeneration(ctx)
	if err != nil || !ok || selected.ID != "g2" {
		t.Fatalf("active: %+v %t %v", selected, ok, err)
	}
	selected.DerivationIDs[0] = "changed"
	stored, ok, err := s.GetGeneration(ctx, "g2")
	if err != nil || !ok || stored.DerivationIDs[0] != "d2" {
		t.Fatalf("generation copy: %+v %t %v", stored, ok, err)
	}
	got, ok, err := s.GetDerivation(ctx, "d2")
	if err != nil || !ok || got.SourceIDs[0] != idFor('a') {
		t.Fatalf("derivation copy: %+v %t %v", got, ok, err)
	}
	got.SourceIDs[0] = "changed"
	list, err := s.ListDerivations(ctx, 10, "")
	if err != nil || len(list) != 2 || list[0].ID != "d1" || list[1].ID != "d2" {
		t.Fatalf("derivation list: %d %v", len(list), err)
	}
	gens, err := s.ListGenerations(ctx, 10, "")
	if err != nil || len(gens) != 2 || gens[0].ID != "g1" || gens[1].ID != "g2" {
		t.Fatalf("generation list: %d %v", len(gens), err)
	}
}

func TestDerivationValidationRejectsUnknownStatesAndInvalidCoverage(t *testing.T) {
	s := NewEphemeralMemoryStore()
	ctx := context.Background()
	for _, d := range []Derivation{
		derivation("bad1", "g", DerivationState("invented")),
		func() Derivation { d := derivation("bad2", "g", Active); d.CoveredSources = 0; return d }(),
		func() Derivation { d := derivation("bad3", "g", Active); d.SourceHashes[0] = "bad"; return d }(),
	} {
		if err := s.PutDerivation(ctx, d); !isStorageError(err, Invalid) {
			t.Fatalf("invalid derivation: %v", err)
		}
	}
}

func TestActivationRequiresStoredObservationProvenance(t *testing.T) {
	ctx := context.Background()
	s := NewEphemeralMemoryStore()
	if err := s.PutGeneration(ctx, Generation{ID: "g", DerivationIDs: []string{"d"}, SchemaVersion: 1, RedactionVersion: privacy.Version}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDerivation(ctx, derivation("d", "g", Active)); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateGeneration(ctx, "g"); !isStorageError(err, Incomplete) {
		t.Fatalf("missing source accepted: %v", err)
	}
	if err := s.PutObservation(ctx, observation(digest('a'))); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateGeneration(ctx, "g"); err != nil {
		t.Fatalf("present source denied: %v", err)
	}
}

func TestSmallPagesStayBoundedOverLargeStore(t *testing.T) {
	const n = 1200
	ctx := context.Background()
	s := NewEphemeralMemoryStore()
	for i := 0; i < n; i++ {
		if err := s.PutObservation(ctx, observation(fmt.Sprintf("%06d", i))); err != nil {
			t.Fatal(err)
		}
		if err := s.PutDerivation(ctx, derivation(fmt.Sprintf("d%06d", i), "g", Building)); err != nil {
			t.Fatal(err)
		}
		if err := s.PutGeneration(ctx, Generation{ID: fmt.Sprintf("g%06d", i), DerivationIDs: []string{"d"}, SchemaVersion: 1, RedactionVersion: privacy.Version}); err != nil {
			t.Fatal(err)
		}
	}
	var after string
	for i := 0; i < n; i++ {
		page, err := s.ListObservations(ctx, 1, after)
		if err != nil || len(page) != 1 || page[0].ContentID() <= after {
			t.Fatalf("observation page %d: %v", i, err)
		}
		after = page[0].ContentID()
	}
	if page, err := s.ListObservations(ctx, 1, after); err != nil || len(page) != 0 {
		t.Fatalf("observation end: %v", err)
	}
	after = ""
	for i := 0; i < n; i++ {
		page, err := s.ListDerivations(ctx, 1, after)
		if err != nil || len(page) != 1 || page[0].ID <= after {
			t.Fatalf("derivation page %d: %v", i, err)
		}
		after = page[0].ID
	}
	if page, err := s.ListDerivations(ctx, 1, after); err != nil || len(page) != 0 {
		t.Fatalf("derivation end: %v", err)
	}
	after = ""
	for i := 0; i < n; i++ {
		page, err := s.ListGenerations(ctx, 1, after)
		if err != nil || len(page) != 1 || page[0].ID <= after {
			t.Fatalf("generation page %d: %v", i, err)
		}
		after = page[0].ID
	}
	if page, err := s.ListGenerations(ctx, 1, after); err != nil || len(page) != 0 {
		t.Fatalf("generation end: %v", err)
	}
	for _, tc := range []struct {
		name string
		read func()
	}{
		{"observations", func() { _, _ = s.ListObservations(ctx, 1, "") }},
		{"derivations", func() { _, _ = s.ListDerivations(ctx, 1, "") }},
		{"generations", func() { _, _ = s.ListGenerations(ctx, 1, "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := testing.Benchmark(func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					tc.read()
				}
			})
			if result.AllocedBytesPerOp() > 8192 {
				t.Fatalf("one-item page allocated %d bytes", result.AllocedBytesPerOp())
			}
		})
	}
}

func TestConcurrentObservationWritesAreRaceSafe(t *testing.T) {
	s := NewEphemeralMemoryStore()
	var wg sync.WaitGroup
	for _, c := range []byte{'a', 'b', 'c', 'd'} {
		wg.Add(1)
		go func(c byte) {
			defer wg.Done()
			_ = s.PutObservation(context.Background(), observation(digest(c)))
			_, _, _ = s.GetObservation(context.Background(), idFor(c))
		}(c)
	}
	wg.Wait()
	list, err := s.ListObservations(context.Background(), 10, "")
	if err != nil || len(list) != 4 {
		t.Fatalf("len=%d err=%v", len(list), err)
	}
}

func isStorageError(err error, code ErrorCode) bool {
	var e *StoreError
	return errors.As(err, &e) && e.Code == code
}
