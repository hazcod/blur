package durable

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/power"
	"github.com/hazcod/blur/internal/today"
)

func TestChronicleFollowUpFlowsIntoAnswerableTodayQuestion(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xA1), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	at := start.Add(10 * time.Minute)
	source, err := capture.SanitizeObservation("editor.app", "", "Meeting: discussed the launch checklist.", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, source, at); err != nil {
		t.Fatal(err)
	}
	date, zone := localDate(at)
	snapshot, err := today.Build(today.Input{Date: date, Zone: zone, GeneratedAt: at, GenerationID: "g-existing", Evidence: []today.Evidence{{SourceID: source.ContentID(), At: at, Change: "Meeting: discussed the launch checklist."}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTodaySnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	summary := chronicle.Summary{HourID: "hour:20261106T10Z", Start: start, End: start.Add(time.Hour), Text: "Discussed the launch checklist.", SourceIDs: []string{source.ContentID()}, FollowUp: &chronicle.FollowUp{SourceID: source.ContentID(), Text: "Who owns the launch checklist?"}}
	if err := store.PutChronicle(ctx, summary); err != nil {
		t.Fatal(err)
	}
	if err := store.PutChronicle(ctx, summary); err != nil {
		t.Fatalf("duplicate recap write failed: %v", err)
	}
	got, found, err := store.GetTodaySnapshot(ctx, date)
	if err != nil || !found || len(got.Questions) != 1 || got.Questions[0].Text != summary.FollowUp.Text || len(got.Questions[0].SourceIDs) != 1 || got.Questions[0].SourceIDs[0] != source.ContentID() {
		t.Fatalf("follow-up did not become a source-linked Today question: %+v found=%t err=%v", got.Questions, found, err)
	}
	if len(got.RecentChanges) != 1 || got.RecentChanges[0].Text != "Meeting: discussed the launch checklist." {
		t.Fatalf("adding a follow-up replaced unrelated Today content: %+v", got.RecentChanges)
	}
	if err := store.AnswerQuestion(ctx, date, got.Questions[0].ID, "Mina owns it.", at.Add(time.Hour)); err != nil {
		t.Fatalf("generated follow-up was not answerable: %v", err)
	}
	resolved, found, err := store.GetTodaySnapshot(ctx, date)
	if err != nil || !found || len(resolved.Questions) != 1 || !resolved.Questions[0].Resolved || resolved.Questions[0].AnswerSourceID == "" {
		t.Fatalf("generated follow-up answer was not linked: %+v found=%t err=%v", resolved.Questions, found, err)
	}
}

func TestChronicleFollowUpCreatesMinimalTodayProjectionWhenNeeded(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xA3), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	at := start.Add(time.Minute)
	source, err := capture.SanitizeObservation("editor.app", "", "Meeting: discussed the launch owner.", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, source, at); err != nil {
		t.Fatal(err)
	}
	summary := chronicle.Summary{HourID: "hour:20261106T10Z", Start: start, End: start.Add(time.Hour), Text: "Discussed launch ownership.", SourceIDs: []string{source.ContentID()}, FollowUp: &chronicle.FollowUp{SourceID: source.ContentID(), Text: "Who owns the launch checklist?"}}
	if err := store.PutChronicle(ctx, summary); err != nil {
		t.Fatal(err)
	}
	date, _ := localDate(at)
	got, found, err := store.GetTodaySnapshot(ctx, date)
	if err != nil || !found || len(got.Questions) != 1 || len(got.RecentChanges) != 0 {
		t.Fatalf("minimal question-only projection = %+v found=%t err=%v", got, found, err)
	}
	if err := store.DismissQuestion(ctx, date, got.Questions[0].ID); err != nil {
		t.Fatalf("generated follow-up was not dismissible: %v", err)
	}
	if err := store.DeleteObservationRecord(ctx, source.ContentID()); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetTodaySnapshot(ctx, date); err != nil || found {
		t.Fatalf("source deletion left a generated follow-up projection: found=%t err=%v", found, err)
	}
}

func TestPutChronicleRejectsFollowUpWithoutMeetingEvidence(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xA2), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	at := start.Add(time.Minute)
	source, err := capture.SanitizeObservation("editor.app", "", "Reviewed the launch checklist.", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, source, at); err != nil {
		t.Fatal(err)
	}
	summary := chronicle.Summary{HourID: "hour:20261106T10Z", Start: start, End: start.Add(time.Hour), Text: "Reviewed the checklist.", SourceIDs: []string{source.ContentID()}, FollowUp: &chronicle.FollowUp{SourceID: source.ContentID(), Text: "Who owns the checklist?"}}
	if err := store.PutChronicle(ctx, summary); err == nil {
		t.Fatal("follow-up was stored without meeting evidence")
	}
}

func TestChronicleRoundTripAndSourceDeletionCascade(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0x94)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	first, err := capture.SanitizeObservation("editor.app", "", "private summary source", 1024)
	if err != nil {
		t.Fatal(err)
	}
	second, err := capture.SanitizeObservation("editor.app", "", "retained source", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, first, start.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, second, start.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	sources := []string{first.ContentID(), second.ContentID()}
	sort.Strings(sources)
	summary := chronicle.Summary{HourID: "hour:20261106T10Z", Start: start, End: start.Add(time.Hour), Text: "Worked on a private project.", SourceIDs: sources}
	if err := store.PutChronicle(ctx, summary); err != nil {
		t.Fatal(err)
	}
	if got, found, err := store.GetChronicle(ctx, summary.HourID); err != nil || !found || got.Text != summary.Text {
		t.Fatalf("GetChronicle() = %+v %t %v", got, found, err)
	}
	if page, next, err := store.ListChronicles(ctx, start, start.Add(time.Hour), "", 1); err != nil || len(page) != 1 || next != "" {
		t.Fatalf("ListChronicles() = %d %q %v", len(page), next, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.DeleteObservationRecord(ctx, first.ContentID()); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetChronicle(ctx, summary.HourID); err != nil || found {
		t.Fatalf("summary survived source deletion: found=%t err=%v", found, err)
	}
	if got, _, err := store.ListChronicles(ctx, start, start.Add(time.Hour), "", 10); err != nil || len(got) != 0 {
		t.Fatalf("deleted summary remains listed: %d %v", len(got), err)
	}
}

func TestChronicleRejectsInvalidAndOversizedSummary(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x95), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	for _, summary := range []chronicle.Summary{
		{HourID: "bad", Start: start, End: start.Add(time.Hour), Text: "summary", SourceIDs: []string{repeatHex("a")}},
		{HourID: "hour:20261106T10Z", Start: start, End: start.Add(time.Hour), Text: string(make([]byte, chronicle.MaxSummaryBytes+1)), SourceIDs: []string{repeatHex("a")}},
	} {
		if err := store.PutChronicle(ctx, summary); err == nil {
			t.Fatalf("PutChronicle accepted malformed record: %+v", summary.HourID)
		}
	}
}

func TestDeleteAllChronicles(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x96), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		hour := start.Add(time.Duration(i) * time.Hour)
		// Distinct source identities are required for independent summaries.
		source, err := capture.SanitizeObservation("editor.app", "", string(rune('a'+i)), 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservationAt(ctx, source, hour.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		id := source.ContentID()
		s := chronicle.Summary{HourID: "hour:" + hour.Format("20060102T15Z"), Start: hour, End: hour.Add(time.Hour), Text: "summary", SourceIDs: []string{id}}
		if err := store.PutChronicle(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteAllChronicles(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _, err := store.ListChronicles(ctx, start, start.Add(2*time.Hour), "", 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("DeleteAllChronicles left %d records: %v", len(rows), err)
	}
}

func TestChronicleRequiresLiveSourceInsideItsHourAndRollbackPreservesIndexes(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x97), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	source, err := capture.SanitizeObservation("editor.app", "", "source", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, source, start.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	bad := chronicle.Summary{HourID: "hour:20261106T10Z", Start: start, End: start.Add(time.Hour), Text: "summary", SourceIDs: []string{source.ContentID()}}
	if err := store.PutChronicle(ctx, bad); err == nil {
		t.Fatal("accepted source outside summary hour")
	}
	if err := store.DeleteObservationRecord(ctx, source.ContentID()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, source, start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutChronicle(ctx, bad); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, func(tx *Tx) error {
		if err := tx.deleteChronicle(bad.HourID); err != nil {
			return err
		}
		return context.Canceled
	}); err == nil {
		t.Fatal("expected forced transaction rollback")
	}
	if got, found, err := store.GetChronicle(ctx, bad.HourID); err != nil || !found || got.Text != bad.Text {
		t.Fatalf("rollback lost chronicle: found=%t err=%v", found, err)
	}
	if err := store.DeleteObservationRecord(ctx, source.ContentID()); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetChronicle(ctx, bad.HourID); err != nil || found {
		t.Fatalf("source cascade did not remove rolled-back chronicle: found=%t err=%v", found, err)
	}
}

func TestCaptureHourEvidenceUsesBoundedEncryptedTimeIndexAndDeletion(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x98), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	first, _ := capture.SanitizeObservation("editor.app", "", "first source", 1024)
	second, _ := capture.SanitizeObservation("editor.app", "", "second source", 1024)
	outside, _ := capture.SanitizeObservation("editor.app", "", "next hour", 1024)
	for _, item := range []struct {
		o  capture.SanitizedObservation
		at time.Time
	}{{first, start.Add(time.Minute)}, {second, start.Add(20 * time.Minute)}, {outside, start.Add(time.Hour)}} {
		if err := store.PutObservationAt(ctx, item.o, item.at); err != nil {
			t.Fatal(err)
		}
	}
	hour, err := store.CaptureHourEvidence(ctx, start)
	if err != nil || len(hour.Evidence) != 2 {
		t.Fatalf("hour evidence=%d err=%v", len(hour.Evidence), err)
	}
	if err := store.DeleteObservationRecord(ctx, first.ContentID()); err != nil {
		t.Fatal(err)
	}
	hour, err = store.CaptureHourEvidence(ctx, start)
	if err != nil || len(hour.Evidence) != 1 || hour.Evidence[0].Observation.ContentID() != second.ContentID() {
		t.Fatalf("post-delete hour evidence=%+v err=%v", hour.Evidence, err)
	}
}

type deletingChroniclePolicy struct{}

func (deletingChroniclePolicy) Effective(context.Context) (power.Effective, error) {
	return power.Resolve(power.Config{Mode: power.Balanced}, power.SystemState{})
}

type deletingChronicleGenerator struct {
	store    *Store
	sourceID string
}

func (deletingChronicleGenerator) CountInputTokens(context.Context, chronicle.Packed) (int, error) {
	return 32, nil
}
func (g deletingChronicleGenerator) Generate(ctx context.Context, input chronicle.Packed, _ int) (chronicle.Summary, error) {
	if err := g.store.DeleteObservationRecord(ctx, g.sourceID); err != nil {
		return chronicle.Summary{}, err
	}
	return chronicle.Summary{HourID: input.HourID, Start: input.Start, End: input.End, Text: "generated after deletion", SourceIDs: append([]string(nil), input.SourceIDs...)}, nil
}

func TestChronicleWorkerCannotRecreateSummaryAfterSourceDeletedDuringGeneration(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x99), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	source, err := capture.SanitizeObservation("editor.app", "", "sensitive source", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, source, start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	hour, err := store.CaptureHourEvidence(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	worker := chronicle.NewWorker(store, deletingChroniclePolicy{}, deletingChronicleGenerator{store: store, sourceID: source.ContentID()}, chronicle.WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	defer worker.Close()
	if err := worker.Process(ctx, hour); err == nil {
		t.Fatal("worker persisted a summary after its source was deleted during generation")
	}
	if _, found, err := store.GetChronicle(ctx, "hour:"+start.Format("20060102T15Z")); err != nil || found {
		t.Fatalf("deleted evidence reappeared as a summary: found=%t err=%v", found, err)
	}
}
