package durable

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
)

func openQuestionStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := durableTestPath(t)
	s, err := OpenDurable(path, durableTestKey(t, 0x7c), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func putQuestionSource(t *testing.T, s *Store, text string, at time.Time) string {
	t.Helper()
	o, err := capture.SanitizeObservation("editor.app", "", text, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(context.Background(), o, at); err != nil {
		t.Fatal(err)
	}
	return o.ContentID()
}

func questionTexts(rows []storage.QuestionRecord) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Question)
	}
	return out
}

func TestQuestionHistoryRoundTripsNewestWindowOldestFirst(t *testing.T) {
	ctx := context.Background()
	s, path := openQuestionStore(t)
	id := putQuestionSource(t, s, "The Orfit retest is scheduled Thursday.", time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC))
	base := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	for i, q := range []string{"what did Dima say", "recap today", "where is the venice key"} {
		rec := storage.QuestionRecord{Question: q, AskedAt: base.Add(time.Duration(i) * time.Minute), PlanKind: "lookup"}
		if i == 0 {
			rec.SourceIDs = []string{id, id}
		}
		if err := s.AppendQuestion(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.ListQuestions(ctx, 2)
	if err != nil || !slices.Equal(questionTexts(rows), []string{"recap today", "where is the venice key"}) {
		t.Fatalf("limit 2 = %v err=%v", questionTexts(rows), err)
	}
	rows, err = s.ListQuestions(ctx, 10)
	if err != nil || len(rows) != 3 || rows[0].Question != "what did Dima say" {
		t.Fatalf("all = %v err=%v", questionTexts(rows), err)
	}
	if !rows[0].AskedAt.Equal(base) || rows[0].PlanKind != "lookup" || !slices.Equal(rows[0].SourceIDs, []string{id}) {
		t.Fatalf("record = %+v", rows[0])
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDurable(path, durableTestKey(t, 0x7c), OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	rows, err = reopened.ListQuestions(ctx, 10)
	if err != nil || len(rows) != 3 {
		t.Fatalf("after reopen = %v err=%v", questionTexts(rows), err)
	}
}

func TestQuestionHistoryKeepsOnlyTheNewestFiveHundred(t *testing.T) {
	ctx := context.Background()
	s, _ := openQuestionStore(t)
	id := putQuestionSource(t, s, "A source cited by every question.", time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC))
	base := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	for i := 0; i < storage.MaxQuestionHistory+7; i++ {
		rec := storage.QuestionRecord{Question: fmt.Sprintf("question %03d", i), AskedAt: base.Add(time.Duration(i) * time.Second), PlanKind: "recap", SourceIDs: []string{id}}
		if err := s.AppendQuestion(ctx, rec); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	rows, err := s.ListQuestions(ctx, storage.MaxQuestionHistory)
	if err != nil || len(rows) != storage.MaxQuestionHistory {
		t.Fatalf("kept %d rows err=%v", len(rows), err)
	}
	if rows[0].Question != "question 007" || rows[len(rows)-1].Question != fmt.Sprintf("question %03d", storage.MaxQuestionHistory+6) {
		t.Fatalf("window = %q .. %q", rows[0].Question, rows[len(rows)-1].Question)
	}
	// Evicted questions leave no reverse links behind: deleting the source is clean.
	if err := s.DeleteObservationRecord(ctx, id); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListQuestions(ctx, storage.MaxQuestionHistory)
	for _, row := range rows {
		if len(row.SourceIDs) != 0 {
			t.Fatalf("%q still cites a deleted source", row.Question)
		}
	}
}

func TestQuestionHistoryRejectsInvalidRecords(t *testing.T) {
	ctx := context.Background()
	s, _ := openQuestionStore(t)
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	for name, rec := range map[string]storage.QuestionRecord{
		"empty":     {Question: "", AskedAt: at, PlanKind: "lookup"},
		"too long":  {Question: strings.Repeat("q", storage.MaxQuestionBytes+1), AskedAt: at, PlanKind: "lookup"},
		"nul":       {Question: "a\x00b", AskedAt: at, PlanKind: "lookup"},
		"bad utf8":  {Question: "\xff\xfe", AskedAt: at, PlanKind: "lookup"},
		"zero time": {Question: "ok", PlanKind: "lookup"},
		"bad kind":  {Question: "ok", AskedAt: at, PlanKind: "Not A Kind!"},
	} {
		if err := s.AppendQuestion(ctx, rec); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := s.AppendQuestion(ctx, storage.QuestionRecord{Question: strings.Repeat("é", storage.MaxQuestionBytes/2), AskedAt: at, PlanKind: "lookup"}); err != nil {
		t.Fatalf("512-byte question rejected: %v", err)
	}
	rows, _ := s.ListQuestions(ctx, 10)
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	for _, limit := range []int{0, -1, storage.MaxQuestionHistory + 1} {
		if _, err := s.ListQuestions(ctx, limit); err == nil {
			t.Errorf("limit %d accepted", limit)
		}
	}
}

func TestQuestionHistoryDropsCitationsToMissingSources(t *testing.T) {
	ctx := context.Background()
	s, _ := openQuestionStore(t)
	live := putQuestionSource(t, s, "A live source.", time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC))
	ghost := strings.Repeat("ab", 32)
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	if err := s.AppendQuestion(ctx, storage.QuestionRecord{Question: "q", AskedAt: at, PlanKind: "lookup", SourceIDs: []string{ghost, live, "hour:2026", ""}}); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.ListQuestions(ctx, 5)
	if len(rows) != 1 || !slices.Equal(rows[0].SourceIDs, []string{live}) {
		t.Fatalf("sources = %+v", rows)
	}
}

func TestDeletingASourceRemovesItFromEveryCitedList(t *testing.T) {
	ctx := context.Background()
	s, _ := openQuestionStore(t)
	doomed := putQuestionSource(t, s, "The source that is going to be deleted.", time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC))
	kept := putQuestionSource(t, s, "The source that stays after deletion.", time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC))
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ { // more than one fence batch
		sources := []string{doomed}
		if i%2 == 0 {
			sources = []string{kept, doomed}
		}
		if err := s.AppendQuestion(ctx, storage.QuestionRecord{Question: fmt.Sprintf("q%02d", i), AskedAt: at.Add(time.Duration(i) * time.Second), PlanKind: "lookup", SourceIDs: sources}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteObservationRecord(ctx, doomed); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListQuestions(ctx, 50)
	if err != nil || len(rows) != 12 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	for _, row := range rows {
		if slices.Contains(row.SourceIDs, doomed) {
			t.Fatalf("%q still cites the deleted source", row.Question)
		}
	}
	if !slices.Equal(rows[0].SourceIDs, []string{kept}) || len(rows[1].SourceIDs) != 0 {
		t.Fatalf("surviving citations = %v / %v", rows[0].SourceIDs, rows[1].SourceIDs)
	}
	// A late answer citing the deleted source cannot resurrect the reference.
	if err := s.AppendQuestion(ctx, storage.QuestionRecord{Question: "late", AskedAt: at.Add(time.Hour), PlanKind: "lookup", SourceIDs: []string{doomed}}); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListQuestions(ctx, 50)
	if slices.Contains(rows[len(rows)-1].SourceIDs, doomed) {
		t.Fatal("late append re-linked a deleted source")
	}
}

func TestStagedAndRetentionDeletionAlsoFenceQuestionHistory(t *testing.T) {
	ctx := context.Background()
	s, _ := openQuestionStore(t)
	old := putQuestionSource(t, s, "An old source that retention removes.", time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	if err := s.AppendQuestion(ctx, storage.QuestionRecord{Question: "about the old one", AskedAt: at, PlanKind: "lookup", SourceIDs: []string{old}}); err != nil {
		t.Fatal(err)
	}
	removed, err := s.DeleteObservationRecordBefore(ctx, old, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), false, nil)
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	rows, _ := s.ListQuestions(ctx, 5)
	if len(rows) != 1 || len(rows[0].SourceIDs) != 0 {
		t.Fatalf("retention left a citation: %+v", rows)
	}
}

func TestDeleteAllQuestionsClearsTheNamespace(t *testing.T) {
	ctx := context.Background()
	s, _ := openQuestionStore(t)
	id := putQuestionSource(t, s, "A source cited by the questions.", time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC))
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 130; i++ {
		if err := s.AppendQuestion(ctx, storage.QuestionRecord{Question: fmt.Sprintf("q%03d", i), AskedAt: at.Add(time.Duration(i) * time.Second), PlanKind: "lookup", SourceIDs: []string{id}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteAllQuestions(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListQuestions(ctx, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	if err := s.DeleteAllQuestions(ctx); err != nil {
		t.Fatalf("clearing an empty history failed: %v", err)
	}
	// The source's reverse index is gone too, so deleting the source is clean.
	if err := s.DeleteObservationRecord(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendQuestion(ctx, storage.QuestionRecord{Question: "after clear", AskedAt: at, PlanKind: "recap"}); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListQuestions(ctx, 10)
	if len(rows) != 1 || rows[0].Question != "after clear" {
		t.Fatalf("rows = %v", questionTexts(rows))
	}
}

func TestStoresWithoutQuestionHistoryOpenUnchanged(t *testing.T) {
	ctx := context.Background()
	s, path := openQuestionStore(t)
	putQuestionSource(t, s, "A store that never recorded a question.", time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC))
	schemaBefore := s.state.schemaVer
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDurable(path, durableTestKey(t, 0x7c), OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	rows, err := reopened.ListQuestions(ctx, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	// Absence of question history preserves the authenticated reader floor.
	if reopened.state.schemaVer != schemaBefore || schemaBefore != OpenSchemaVersion {
		t.Fatalf("question history changed schema: before=%d after=%d current=%d", schemaBefore, reopened.state.schemaVer, OpenSchemaVersion)
	}
}
