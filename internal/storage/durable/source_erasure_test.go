package durable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/tasks"
	"github.com/hazcod/blur/internal/today"
)

func TestSourceErasureRecoversEveryPhaseBeforePublishingStore(t *testing.T) {
	for stop := 0; stop <= 7; stop++ {
		t.Run(fmt.Sprint(stop), func(t *testing.T) {
			ctx := context.Background()
			path, key := durableTestPath(t), durableTestKey(t, byte(0xe9+stop))
			s, err := OpenDurable(path, key, CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			chunk := enrichmentFixture(t, s, "work.example", "Atlas source evidence", derive.Proposal{Kind: knowledge.Project, Name: "Atlas", Fact: "Atlas has source evidence."})
			if err := s.ApplyEnrichment(ctx, chunk); err != nil {
				t.Fatal(err)
			}
			entityID := enrich.EntityID(knowledge.Project, "Atlas", "chat.app", "work.example")
			item := taskFixture(1)
			item.UserAuthored, item.SourceID = false, chunk.SourceID
			if err := s.PutTask(ctx, item, 0); err != nil {
				t.Fatal(err)
			}
			manual := knowledge.Entity{ID: "legacy-project", Kind: knowledge.Project, Name: "Legacy project", State: knowledge.Confirmed, Facts: []knowledge.Fact{{ID: "legacy-fact", Text: "Original evidence", State: knowledge.Confirmed, Sources: []knowledge.Source{{ID: chunk.SourceID}}}}}
			if err := s.PutKnowledgeEntity(ctx, manual); err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
			snapshot, err := today.Build(today.Input{Date: "2026-11-12", Zone: "UTC", GeneratedAt: at, GenerationID: "erasure", Evidence: []today.Evidence{{SourceID: chunk.SourceID, At: at, Change: "Source evidence"}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.PutTodaySnapshot(ctx, snapshot); err != nil {
				t.Fatal(err)
			}
			segment := activity.Segment{ID: "erasure-segment", WallStart: at, WallEnd: at.Add(time.Minute), Duration: int64(time.Minute), AppID: "chat.app", SourceIDs: []string{chunk.SourceID}}
			if err := s.PutActivitySegment(ctx, segment); err != nil {
				t.Fatal(err)
			}
			interrupted := false
			err = s.deleteSourceStaged(ctx, chunk.SourceID, func() error {
				journal, found, err := s.readSourceErasureLocked(ctx)
				if err != nil || !found {
					t.Fatal(found, err)
				}
				if journal.Phase == stop {
					interrupted = true
					return context.Canceled
				}
				return nil
			})
			if !interrupted || !errors.Is(err, context.Canceled) {
				t.Fatal("did not interrupt requested phase", stop, err)
			}
			if _, _, err := s.GetObservation(ctx, chunk.SourceID); !errors.Is(err, ErrDeletionPending) {
				t.Fatal("partial source was readable", err)
			}
			if _, _, err := s.GetInferredKnowledge(ctx, entityID); !errors.Is(err, ErrDeletionPending) {
				t.Fatal("partial Library was readable", err)
			}
			if _, _, err := s.GetTask(ctx, item.ID); !errors.Is(err, ErrDeletionPending) {
				t.Fatal("partial tasks were readable", err)
			}
			late, _ := capture.SanitizeObservation("chat.app", "local", "Late capture", 1024)
			if err := s.PutObservation(ctx, late); !errors.Is(err, ErrDeletionPending) {
				t.Fatal("write entered an interrupted erasure", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			disk, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(disk, []byte(chunk.SourceID)) || bytes.Contains(disk, []byte("source_id")) {
				t.Fatal("erasure journal exposed plaintext provenance")
			}
			s, err = OpenDurable(path, key, OpenExisting)
			if err != nil {
				t.Fatal("reopen failed to recover source erasure", err)
			}
			if _, found, err := s.GetObservation(ctx, chunk.SourceID); err != nil || found {
				t.Fatal("recovery restored source", found, err)
			}
			if _, found, err := s.GetTask(ctx, item.ID); err != nil || found {
				t.Fatal("recovery retained task", found, err)
			}
			if _, found, err := s.GetTodaySnapshot(ctx, snapshot.Date); err != nil || found {
				t.Fatal("Today evidence survived recovery", found, err)
			}
			if segments, err := s.GetActivitySegments(ctx, []string{segment.ID}); err != nil || len(segments) != 0 {
				t.Fatal("activity survived recovery", segments, err)
			}
			if _, found, err := s.GetInferredKnowledge(ctx, entityID); err != nil || found {
				t.Fatal("recovery retained inferred evidence", found, err)
			}
			current, found, err := s.GetKnowledgeEntity(ctx, manual.ID)
			if err != nil || !found || len(current.Facts) != 0 {
				t.Fatal("legacy source claims survived recovery", current, found, err)
			}
			if _, found, err := s.readIndexValue(ctx, erasureNamespace, erasureJournalID, 512); err != nil || found {
				t.Fatal("completed journal retained", found, err)
			}
		})
	}
}

func TestSourceErasureGateRejectsReadersWithoutWaitingForWriter(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xf2), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	o, _ := capture.SanitizeObservation("chat.app", "local", "Source being erased", 1024)
	if err := s.PutObservation(ctx, o); err != nil {
		t.Fatal(err)
	}
	checked := false
	if err := s.deleteSourceStaged(ctx, o.ContentID(), func() error {
		if checked {
			return nil
		}
		checked = true
		result := make(chan error, 1)
		go func() { _, _, err := s.GetObservation(ctx, o.ContentID()); result <- err }()
		select {
		case err := <-result:
			if !errors.Is(err, ErrDeletionPending) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("reader waited on maintenance writer")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCombinedSourceErasureWithFullLibraryLegacyTasksAndVectors(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xf3), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	doc := semanticDocumentFixture(t, s, strings.Repeat("x", semantic.ChunkBytes+semantic.MaxChunksPerSource-1), "a")
	doc.Model.Dimensions = semantic.MaxDimensions
	vector := make([]float32, semantic.MaxDimensions)
	vector[0] = 1
	doc.Chunks = nil
	for n := 0; n < semantic.MaxChunksPerSource; n++ {
		doc.Chunks = append(doc.Chunks, semantic.Chunk{Start: n, End: n + semantic.ChunkBytes, Vector: vector})
	}
	if err := s.PutSemanticDocument(ctx, doc); err != nil {
		t.Fatal(err)
	}
	other, _ := capture.SanitizeObservation("chat.app", "local", "Independent surviving evidence", 1024)
	if err := s.PutObservation(ctx, other); err != nil {
		t.Fatal(err)
	}
	sources := []string{doc.SourceID, other.ContentID()}
	sort.Strings(sources)
	var entityIDs []string
	for n := 0; n < enrich.MaxSourceEntities; n++ {
		name := fmt.Sprintf("Project %03d", n)
		record := inferredRecord{Entity: knowledge.Entity{ID: enrich.EntityID(knowledge.Project, name, "chat.app", "local"), Kind: knowledge.Project, Name: name, State: knowledge.Confirmed, Inference: &knowledge.Inference{AppID: "chat.app", Domain: "local"}, ActivitySourceIDs: sources}, Sources: sources}
		for k := 0; k < knowledge.MaxFacts; k++ {
			refs := []knowledge.Source{{ID: other.ContentID()}}
			if k == 0 {
				refs = []knowledge.Source{{ID: doc.SourceID}}
			}
			if k == 1 {
				refs = []knowledge.Source{{UserAuthored: true}}
			}
			record.Entity.Facts = append(record.Entity.Facts, knowledge.Fact{ID: fmt.Sprintf("fact-%03d", k), Text: fmt.Sprintf("%03d", k) + strings.Repeat("f", 509), State: knowledge.Confirmed, Sources: refs})
		}
		for k := 0; k < knowledge.MaxFacts; k++ {
			record.Entity.OpenLoops = append(record.Entity.OpenLoops, knowledge.OpenLoop{ID: fmt.Sprintf("loop-%03d", k), Text: fmt.Sprintf("%03d", k) + strings.Repeat("l", 509), State: knowledge.Confirmed, Sources: []knowledge.Source{{ID: other.ContentID()}}})
			if len(mustJSON(record)) > enrich.MaxEntityBytes {
				record.Entity.OpenLoops = record.Entity.OpenLoops[:len(record.Entity.OpenLoops)-1]
				break
			}
		}
		if size := len(mustJSON(record)); size < enrich.MaxEntityBytes-1024 || size > enrich.MaxEntityBytes {
			t.Fatal("fixture did not reach supported entity size", size)
		}
		if err := s.inferredUpdate(ctx, func(tx *Tx) error { return tx.putInferredRecord(record, false) }); err != nil {
			t.Fatal(n, err)
		}
		entityIDs = append(entityIDs, record.Entity.ID)
	}
	sort.Strings(entityIDs)
	for _, sourceID := range sources {
		if err := s.inferredUpdate(ctx, func(tx *Tx) error {
			return tx.putEnrichmentProgress(enrich.Progress{SourceID: sourceID, ModelKey: enrich.ModelKey("fixture"), Entities: entityIDs, Complete: true})
		}); err != nil {
			t.Fatal(err)
		}
	}
	for n := 1; n <= tasks.MaxSourceTasks; n++ {
		item := taskFixture(n)
		item.UserAuthored, item.SourceID, item.SourceIDs = false, sources[0], sources
		if err := s.PutTask(ctx, item, 0); err != nil {
			t.Fatal(err)
		}
	}
	for n := 0; n < knowledge.MaxEntities; n++ {
		id := fmt.Sprintf("legacy-%04d", n)
		entity := knowledge.Entity{ID: id, Kind: knowledge.Topic, Name: "Topic " + id, State: knowledge.Confirmed, Facts: []knowledge.Fact{{ID: "fact", Text: "Grounded fact", State: knowledge.Confirmed, Sources: []knowledge.Source{{ID: doc.SourceID}}}}}
		if err := s.PutKnowledgeEntity(ctx, entity); err != nil {
			t.Fatal(n, err)
		}
	}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.deleteObservationRecord(doc.SourceID) }); !errors.Is(err, ErrTransactionBudget) {
		t.Fatal("combined fixture did not exercise the physical budget", err)
	}
	if _, found, err := s.GetObservation(ctx, doc.SourceID); err != nil || !found {
		t.Fatal("failed atomic attempt changed source", found, err)
	}
	if err := s.DeleteObservationRecord(ctx, doc.SourceID); err != nil {
		t.Fatal("combined supported source fanout could not be erased", err)
	}
	if _, found, err := s.GetObservation(ctx, doc.SourceID); err != nil || found {
		t.Fatal(found, err)
	}
	if _, found, err := s.GetSemanticDocument(ctx, doc.SourceID); err != nil || found {
		t.Fatal("vectors retained", found, err)
	}
	if _, found, err := s.GetObservation(ctx, other.ContentID()); err != nil || !found {
		t.Fatal("unrelated evidence removed", found, err)
	}
	for _, id := range entityIDs {
		entity, found, err := s.GetInferredKnowledge(ctx, id)
		if err != nil || !found || len(entity.Facts) != knowledge.MaxFacts-1 {
			t.Fatal("surviving claims/corrections changed", id, found, err)
		}
		for _, fact := range entity.Facts {
			for _, source := range fact.Sources {
				if source.ID == doc.SourceID {
					t.Fatal("erased provenance retained")
				}
			}
		}
		if !entity.Facts[0].Sources[0].UserAuthored {
			t.Fatal("explicit correction removed")
		}
	}
	for n := 0; n < knowledge.MaxEntities; n++ {
		entity, found, err := s.GetKnowledgeEntity(ctx, fmt.Sprintf("legacy-%04d", n))
		if err != nil || !found || len(entity.Facts) != 0 {
			t.Fatal("legacy claim retained", n, found, err)
		}
	}
	if linked, err := s.TasksForSources(ctx, sources); err != nil || len(linked) != 0 {
		t.Fatal("task reverse links retained", linked, err)
	}
}

func TestCorruptErasureJournalCannotPublishStore(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xf4)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		return tx.PutIndexRecord(erasureNamespace, erasureJournalID, []byte(`{"source_id":"invalid","phase":9}`))
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenDurable(path, key, OpenExisting); !errors.Is(err, ErrCorrupt) {
		if reopened != nil {
			_ = reopened.Close()
		}
		t.Fatal("corrupt recovery journal was admitted", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed recovery changed durable data", err)
	}
}

func TestCanceledErasureBeforeJournalLeavesSourceAvailable(t *testing.T) {
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xf5), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	o, _ := capture.SanitizeObservation("chat.app", "local", "Canceled deletion", 1024)
	if err := s.PutObservation(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.deleteSourceStaged(ctx, o.ContentID(), nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, found, err := s.GetObservation(context.Background(), o.ContentID()); err != nil || !found {
		t.Fatal("canceled intent hid or erased source", found, err)
	}
}

func TestSourceErasureRecoversAfterProcessExitWithoutClose(t *testing.T) {
	ctx := context.Background()
	key := durableTestKey(t, 0xf6)
	if os.Getenv("BLUR_ERASURE_CRASH_HELPER") == "1" {
		s, err := OpenDurable(os.Getenv("BLUR_ERASURE_CRASH_PATH"), key, OpenExisting)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.deleteSourceStaged(ctx, os.Getenv("BLUR_ERASURE_CRASH_SOURCE"), func() error { os.Exit(0); return nil }); err != nil {
			t.Fatal(err)
		}
		t.Fatal("crash helper completed without exiting")
	}
	path := durableTestPath(t)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	o, _ := capture.SanitizeObservation("chat.app", "local", "Crash recovery source", 1024)
	if err := s.PutObservation(ctx, o); err != nil {
		t.Fatal(err)
	}
	item := taskFixture(1)
	item.UserAuthored, item.SourceID = false, o.ContentID()
	if err := s.PutTask(ctx, item, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestSourceErasureRecoversAfterProcessExitWithoutClose$")
	command.Env = append(os.Environ(), "BLUR_ERASURE_CRASH_HELPER=1", "BLUR_ERASURE_CRASH_PATH="+path, "BLUR_ERASURE_CRASH_SOURCE="+o.ContentID())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v %s", err, output)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	if _, found, err := s.GetObservation(ctx, o.ContentID()); err != nil || found {
		t.Fatal("process-exit recovery retained source", found, err)
	}
	if _, found, err := s.GetTask(ctx, item.ID); err != nil || found {
		t.Fatal("process-exit recovery retained task", found, err)
	}
}
