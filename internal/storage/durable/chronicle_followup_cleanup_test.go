package durable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/today"
)

func followupDependencyFixture(t *testing.T, s *Store, withInputs bool) (chronicle.Summary, capture.SanitizedObservation, capture.SanitizedObservation, capture.SanitizedObservation) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 11, 6, 0, 10, 0, 0, time.UTC)
	var sources []capture.SanitizedObservation
	for _, text := range []string{"Meeting: discuss release ownership.", "Synthetic uncited private release context.", "Unrelated user-authored retained change."} {
		o, err := capture.SanitizeObservation("editor.app", "", text, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(ctx, o, at); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, o)
	}
	date, zone := localDate(at)
	snapshot, err := today.Build(today.Input{Date: date, Zone: zone, GeneratedAt: at, GenerationID: "unrelated", Evidence: []today.Evidence{{SourceID: sources[2].ContentID(), At: at, Change: sources[2].Text()}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutTodaySnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	start := at.Truncate(time.Hour)
	summary := chronicle.Summary{HourID: "hour:" + start.Format("20060102T15Z"), Start: start, End: start.Add(time.Hour), Text: "Synthetic release recap.", SourceIDs: []string{sources[0].ContentID()}, FollowUp: &chronicle.FollowUp{SourceID: sources[0].ContentID(), Text: "Who owns the release?"}}
	if withInputs {
		summary.InputSourceIDs = []string{sources[0].ContentID(), sources[1].ContentID()}
		sort.Strings(summary.InputSourceIDs)
		summary.InputReceipt = strings.Repeat("a", 64)
	}
	if err := s.PutChronicle(ctx, summary); err != nil {
		t.Fatal(err)
	}
	return summary, sources[0], sources[1], sources[2]
}

func TestChronicleFollowUpDependsOnEveryInputAndDeletionSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xc1)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	summary, a, b, other := followupDependencyFixture(t, s, true)
	date, _ := localDate(summary.Start.Add(10 * time.Minute))
	snapshot, found, err := s.GetTodaySnapshot(ctx, date)
	if err != nil || !found || len(snapshot.Questions) != 1 {
		t.Fatal("followup missing", found, err)
	}
	if !slices.Equal(snapshot.Questions[0].SourceIDs, summary.DependencyIDs()) {
		t.Errorf("question omitted actual model input: got %v want %v", snapshot.Questions[0].SourceIDs, summary.DependencyIDs())
	}
	if !slices.Contains(snapshot.SourceIDs, b.ContentID()) {
		t.Error("Today reverse index omitted uncited input")
	}
	if err := s.AnswerQuestion(ctx, date, snapshot.Questions[0].ID, "Synthetic authored answer: Mira owns it.", summary.Start.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	snapshot, _, err = s.GetTodaySnapshot(ctx, date)
	if err != nil {
		t.Fatal(err)
	}
	answerID := snapshot.Questions[0].AnswerSourceID
	if err := s.DeleteObservationRecord(ctx, b.ContentID()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, found, err = s.GetTodaySnapshot(ctx, date)
	if err != nil || !found || len(snapshot.Questions) != 0 {
		t.Fatalf("uncited input deletion retained generated question: %+v %t %v", snapshot.Questions, found, err)
	}
	for _, id := range []string{a.ContentID(), other.ContentID(), answerID} {
		if _, found, err := s.GetObservation(ctx, id); err != nil || !found {
			t.Fatal("erased unrelated/authored source", id, found, err)
		}
	}
	if !slices.ContainsFunc(snapshot.RecentChanges, func(c today.Change) bool { return c.Text == other.Text() }) {
		t.Fatal("erased unrelated Today content")
	}
}

func TestChronicleFollowUpSameIDRefreshesDependenciesPreservingAnswer(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	summary, a, b, _ := followupDependencyFixture(t, s, false)
	date, _ := localDate(summary.Start.Add(10 * time.Minute))
	id := chronicleFollowUpID(summary.HourID, a.ContentID(), summary.FollowUp.Text)
	if err := s.AnswerQuestion(ctx, date, id, "Synthetic answer retained.", summary.Start.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	before, _, _ := s.GetTodaySnapshot(ctx, date)
	summary.InputSourceIDs = []string{a.ContentID(), b.ContentID()}
	sort.Strings(summary.InputSourceIDs)
	summary.InputReceipt = strings.Repeat("b", 64)
	if err := s.PutChronicle(ctx, summary); err != nil {
		t.Fatal(err)
	}
	after, _, err := s.GetTodaySnapshot(ctx, date)
	if err != nil || len(after.Questions) != 1 || !slices.Equal(after.Questions[0].SourceIDs, summary.DependencyIDs()) || after.Questions[0].AnswerSourceID != before.Questions[0].AnswerSourceID || !after.Questions[0].Resolved {
		t.Fatalf("existing question dependencies/answer not retained: %+v %v", after.Questions, err)
	}
}

func TestChronicleFollowUpReplacementAndLegacyDeletionCleanAllIndexedLocalDates(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "delete", true: "replace"}[replace], func(t *testing.T) {
			ctx := context.Background()
			s := openReplicaTestStore(t)
			summary, a, _, other := followupDependencyFixture(t, s, false)
			date, _ := localDate(summary.Start.Add(10 * time.Minute))
			original, _, err := s.GetTodaySnapshot(ctx, date)
			if err != nil {
				t.Fatal(err)
			}
			// A persisted local date can differ from the process's present time zone.
			copy := original.Clone()
			copy.Zone = "Pacific/Honolulu"
			loc, _ := time.LoadLocation(copy.Zone)
			copy.Date = copy.GeneratedAt.In(loc).Format("2006-01-02")
			if copy.Date == date {
				copy.Zone = "Pacific/Kiritimati"
				loc, _ = time.LoadLocation(copy.Zone)
				copy.Date = copy.GeneratedAt.In(loc).Format("2006-01-02")
			}
			copy.Questions = append(copy.Questions, today.Question{ID: "unrelated-question", Text: "Keep this unrelated question?", SourceIDs: []string{other.ContentID()}, CreatedAt: copy.GeneratedAt})
			if err := s.PutTodaySnapshot(ctx, copy); err != nil {
				t.Fatal(err)
			}
			if replace {
				summary.FollowUp = nil
				if err := s.PutChronicle(ctx, summary); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := s.DeleteChronicle(ctx, summary.HourID); err != nil {
					t.Fatal(err)
				}
			}
			for _, d := range []string{date, copy.Date} {
				got, _, err := s.GetTodaySnapshot(ctx, d)
				if err != nil {
					t.Fatal(err)
				}
				for _, q := range got.Questions {
					if q.ID == chronicleFollowUpID(summary.HourID, a.ContentID(), "Who owns the release?") {
						t.Fatal("retained old generated question on", d)
					}
				}
			}
			got, found, err := s.GetTodaySnapshot(ctx, copy.Date)
			if err != nil || !found || len(got.Questions) != 1 || got.Questions[0].ID != "unrelated-question" || len(got.RecentChanges) != 1 {
				t.Fatal("cleanup changed unrelated projection", got, found, err)
			}
		})
	}
}

func TestChronicleFollowUpAcceptsGenuinePublishedPartWithoutLoadingObservation(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	summary := publishIndexedFixture(t, s, strings.Repeat("Meeting: Discuss the synthetic release owner.\n", 1600), start.Add(time.Minute))
	drainCaptureIndex(t, s)
	ids, _, err := s.ListCaptureSourceIDs(ctx, summary.ContentID(), -1, 64)
	if err != nil || len(ids) < 2 {
		t.Fatal(ids, err)
	}
	dependencies := append([]string(nil), ids...)
	sort.Strings(dependencies)
	row := chronicle.Summary{HourID: "hour:20261106T10Z", Start: start, End: start.Add(time.Hour), Text: "Discussed release.", SourceIDs: ids[:1], InputSourceIDs: dependencies, InputReceipt: strings.Repeat("c", 64), FollowUp: &chronicle.FollowUp{SourceID: ids[0], Text: "Who owns the release?"}}
	if err := s.PutChronicle(ctx, row); err != nil {
		t.Fatal("genuine published part rejected by Today", err)
	}
	date, _ := localDate(start.Add(time.Minute))
	got, found, err := s.GetTodaySnapshot(ctx, date)
	if err != nil || !found || len(got.Questions) != 1 || !slices.Equal(got.Questions[0].SourceIDs, dependencies) {
		t.Fatal("part question missing", found, err)
	}
}

func TestChronicleFollowUpMaximumInputDependenciesFitAtomicPublication(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	var ids []string
	var meeting string
	for i := range chronicle.MaxEvidence {
		text := fmt.Sprintf("Synthetic model input %d", i)
		if i == 0 {
			text = "Meeting: discuss synthetic release."
		}
		source, err := capture.SanitizeObservation("editor.app", "", text, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(ctx, source, start.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, source.ContentID())
		if i == 0 {
			meeting = source.ContentID()
		}
	}
	sort.Strings(ids)
	row := chronicle.Summary{HourID: "hour:20261106T10Z", Start: start, End: start.Add(time.Hour), Text: "Synthetic recap.", SourceIDs: []string{meeting}, InputSourceIDs: ids, InputReceipt: strings.Repeat("a", 64), FollowUp: &chronicle.FollowUp{SourceID: meeting, Text: "Who owns the release?"}}
	if err := s.PutChronicle(ctx, row); err != nil {
		t.Fatal("maximum-input recap and question exceeded atomic publication budget", err)
	}
	date, _ := localDate(start.Add(time.Minute))
	snapshot, found, err := s.GetTodaySnapshot(ctx, date)
	if err != nil || !found || len(snapshot.Questions) != 1 || !slices.Equal(snapshot.Questions[0].SourceIDs, ids) {
		t.Fatal("maximum dependency receipt missing", found, err)
	}
	row.FollowUp.Text = "Who owns the revised release?"
	if err := s.PutChronicle(ctx, row); err != nil {
		t.Fatal("maximum-input recap exceeded atomic replacement budget", err)
	}
	snapshot, found, err = s.GetTodaySnapshot(ctx, date)
	if err != nil || !found || len(snapshot.Questions) != 1 || snapshot.Questions[0].Text != row.FollowUp.Text || !slices.Equal(snapshot.Questions[0].SourceIDs, ids) {
		t.Fatal("maximum dependency replacement receipt missing", found, err)
	}
	if err := s.DeleteChronicle(ctx, row.HourID); err != nil {
		t.Fatal("maximum-input recap exceeded cleanup budget", err)
	}
}

func TestChronicleFollowUpCleanupUsesAllBoundedReverseDates(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	row, _, _, other := followupDependencyFixture(t, s, false)
	at := row.Start.Add(10 * time.Minute)
	firstDate, _ := localDate(at)
	snapshot, found, err := s.GetTodaySnapshot(ctx, firstDate)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	var dates []string
	for i := range maxTodaySourceDates {
		copy := snapshot.Clone()
		copy.GeneratedAt = at.AddDate(0, 0, i)
		copy.Date, copy.Zone = localDate(copy.GeneratedAt)
		dates = append(dates, copy.Date)
		if err := s.PutTodaySnapshot(ctx, copy); err != nil {
			t.Fatal("seed bounded reverse dates", i, err)
		}
	}
	if err := s.DeleteChronicle(ctx, row.HourID); err != nil {
		t.Fatal("bounded local-date cleanup exceeded atomic budget", err)
	}
	for _, date := range dates {
		got, found, err := s.GetTodaySnapshot(ctx, date)
		if err != nil || !found || len(got.Questions) != 0 || len(got.RecentChanges) != 1 || got.RecentChanges[0].Text != other.Text() {
			t.Fatal("bounded cleanup missed or altered date", date, found, err)
		}
	}
}

func TestChronicleFollowUpMigrationRemovesHistoricalOrphansPreservingAuthoredAnswers(t *testing.T) {
	for _, scenario := range []string{"dropped_pointer", "changed_text", "changed_source", "already_deleted", "v1_complete_no_recaps", "current_known"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			path, key := durableTestPath(t), durableTestKey(t, 0xc7)
			s, err := OpenDurable(path, key, CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			row, _, _, other := followupDependencyFixture(t, s, scenario == "current_known")
			at := row.Start.Add(10 * time.Minute)
			date, _ := localDate(at)
			snapshot, _, err := s.GetTodaySnapshot(ctx, date)
			if err != nil {
				t.Fatal(err)
			}
			originalID := snapshot.Questions[0].ID
			if err := s.AnswerQuestion(ctx, date, originalID, "Synthetic authored answer must survive orphan migration.", at.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			snapshot, _, err = s.GetTodaySnapshot(ctx, date)
			if err != nil {
				t.Fatal(err)
			}
			answerID := snapshot.Questions[0].AnswerSourceID
			// A prefix alone does not make an authored question generated ownership.
			customID := "chronicle-question-" + strings.Repeat("f", 64)
			snapshot.Questions = append(snapshot.Questions, today.Question{ID: customID, Text: "Retain this unrelated question?", SourceIDs: []string{other.ContentID()}, CreatedAt: at})
			if scenario == "already_deleted" || scenario == "v1_complete_no_recaps" {
				if err := s.DeleteChronicle(ctx, row.HourID); err != nil {
					t.Fatal(err)
				}
			} else if scenario != "current_known" {
				priorSources := row.DependencyIDs()
				switch scenario {
				case "dropped_pointer":
					row.FollowUp = nil
				case "changed_text":
					row.FollowUp.Text = "What is the revised release date?"
				case "changed_source":
					replacement, err := capture.SanitizeObservation("editor.app", "", "Meeting: unrelated new planning discussion.", 1024)
					if err != nil {
						t.Fatal(err)
					}
					if err := s.PutObservationAt(ctx, replacement, at); err != nil {
						t.Fatal(err)
					}
					row.SourceIDs = []string{replacement.ContentID()}
					row.FollowUp.SourceID = replacement.ContentID()
				}
				// Reproduce the former replacement writer, before it removed the old
				// copied Today question. Replacing only this authenticated row is valid
				// legacy state for the following migration to inspect and erase.
				if err := s.Update(ctx, func(tx *Tx) error {
					for _, id := range priorSources {
						if !slices.Contains(row.DependencyIDs(), id) {
							if err := tx.removeChronicleSource(id, row.HourID); err != nil {
								return err
							}
						}
					}
					for _, id := range row.DependencyIDs() {
						if !slices.Contains(priorSources, id) {
							if err := tx.addChronicleSource(id, row.HourID); err != nil {
								return err
							}
						}
					}
					return tx.PutIndexRecord(chronicleNS, row.HourID, mustJSON(row))
				}); err != nil {
					t.Fatal(err)
				}
			}
			// Recreate the historical copy left by old recap replacement/deletion.
			if err := s.PutTodaySnapshot(ctx, snapshot); err != nil {
				t.Fatal(err)
			}
			beforeChanges, err := json.Marshal(snapshot.RecentChanges)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Update(ctx, func(tx *Tx) error {
				if scenario == "v1_complete_no_recaps" {
					return tx.PutIndexRecord(chronicleDependencyMigrationNS, chronicleDependencyMigrationID, []byte{1, 1})
				}
				return tx.deleteKnowledgeRecord(chronicleDependencyMigrationNS, chronicleDependencyMigrationID)
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenDurable(path, key, OpenExisting)
			if err != nil {
				t.Fatal("orphan migration failed", err)
			}
			got, found, err := s.GetTodaySnapshot(ctx, date)
			if err != nil || !found {
				t.Fatal("unrelated Today erased", found, err)
			}
			kept := slices.ContainsFunc(got.Questions, func(q today.Question) bool { return q.ID == originalID })
			if kept != (scenario == "current_known") {
				t.Fatal("historical generated question ownership mishandled", scenario, kept)
			}
			if !slices.ContainsFunc(got.Questions, func(q today.Question) bool { return q.ID == customID }) {
				t.Fatal("authored question removed by prefix alone")
			}
			afterChanges, err := json.Marshal(got.RecentChanges)
			if err != nil || !bytes.Equal(beforeChanges, afterChanges) {
				t.Fatal("unrelated/authored Today changes were rewritten", err)
			}
			if answer, found, err := s.GetObservation(ctx, answerID); err != nil || !found || !strings.Contains(answer.Text(), "Synthetic authored answer must survive") {
				t.Fatal("raw authored answer erased", found, err)
			}
		})
	}
}

func TestChronicleFollowUpMigrationPagesResumeAndRollbackAtomically(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xc8)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	row, _, _, other := followupDependencyFixture(t, s, false)
	at := row.Start.Add(10 * time.Minute)
	date, _ := localDate(at)
	snapshot, _, err := s.GetTodaySnapshot(ctx, date)
	if err != nil {
		t.Fatal(err)
	}
	row.FollowUp = nil
	if err := s.Update(ctx, func(tx *Tx) error {
		if err := tx.PutIndexRecord(chronicleNS, row.HourID, mustJSON(row)); err != nil {
			return err
		}
		// More than two metadata pages force persisted continuation, and a single
		// unrelated extension larger than the ordinary2MiB page must still advance.
		for i := range 70 {
			if err := tx.PutIndexRecord("orphan-fixture", fmt.Sprintf("row-%03d", i), []byte("synthetic")); err != nil {
				return err
			}
		}
		if err := tx.PutIndexRecord("orphan-fixture", "large", bytes.Repeat([]byte("x"), 3<<20)); err != nil {
			return err
		}
		return tx.putChronicleDependencyProgress(chronicleDependencyProgress{phase: chronicleDependencyQuestions})
	}); err != nil {
		t.Fatal(err)
	}
	sentinel := context.Canceled
	// Seek immediately before the actual Today key so this failed page must
	// have removed the question before the cursor callback rejects its commit.
	// HMAC ordering must not let a metadata-only first page weaken this proof.
	beforeToday := s.logicalKey(groupRecord, kindIndex, "index:"+todayNamespace, date)
	for i := len(beforeToday) - 1; i >= 2; i-- {
		if beforeToday[i] > 0 {
			beforeToday[i]--
			break
		}
		beforeToday[i] = 0xff
	}
	attemptedRemoval := false
	s.state.mu.Lock()
	_, _, err = s.migrateChronicleFollowUpPageLocked(ctx, beforeToday, func(tx *Tx, _ []byte, _ bool) error {
		current, found, readErr := tx.readChronicleFollowUpSnapshot(date)
		attemptedRemoval = readErr == nil && found && len(current.Questions) == 0
		return sentinel
	})
	s.state.mu.Unlock()
	clear(beforeToday)
	if !errors.Is(err, sentinel) || !attemptedRemoval {
		t.Fatal("cursor failure did not abort an actual question removal", attemptedRemoval, err)
	}
	before, found, err := s.GetTodaySnapshot(ctx, date)
	if err != nil || !found || !slices.EqualFunc(before.Questions, snapshot.Questions, func(a, b today.Question) bool { return a.ID == b.ID }) {
		t.Fatal("rolled-back page mutated Today", found, err)
	}
	s.state.mu.Lock()
	next, complete, err := s.migrateChronicleFollowUpPageLocked(ctx, nil, func(tx *Tx, next []byte, complete bool) error {
		if complete {
			return tx.putChronicleDependencyProgress(chronicleDependencyProgress{phase: chronicleDependencyComplete})
		}
		return tx.putChronicleDependencyProgress(chronicleDependencyProgress{phase: chronicleDependencyQuestions, afterIndex: next})
	})
	s.state.mu.Unlock()
	if err != nil || complete || !validChronicleDependencyIndexCursor(next) {
		t.Fatal("first page did not leave bounded continuation", complete, err)
	}
	clear(next)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal("committed page restart failed", err)
	}
	after, found, err := s.GetTodaySnapshot(ctx, date)
	if err != nil || !found || len(after.Questions) != 0 || !slices.ContainsFunc(after.RecentChanges, func(c today.Change) bool { return c.Text == other.Text() }) {
		t.Fatal("resumed migration left orphan or erased unrelated text", found, err)
	}
	// A completed migration is a fixed marker and performs no further writes.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	beforeDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	afterDisk, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(beforeDisk, afterDisk) {
		t.Fatal("completed orphan migration rewrote ready store", err)
	}
}
