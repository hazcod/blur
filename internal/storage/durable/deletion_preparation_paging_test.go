package durable

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/tasks"
)

func TestDeletionPreparationPagesEveryAcceptedCapturePart(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	summary := publishIndexedFixture(t, s, strings.Repeat(" ", 16<<20)+"tailword", time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC))
	if summary.PartCount() != 513 {
		t.Fatal("fixture did not exceed one preparation transaction", summary.PartCount())
	}
	prepared := 0
	if err := s.DeleteSourceRecordWithPrepare(ctx, summary.ContentID(), func(string, []tasks.Item) error { prepared++; return nil }); err != nil {
		t.Fatal("accepted source cannot finish native preparation", err)
	}
	if int64(prepared) != 1+summary.PartCount() {
		t.Fatal("preparation skipped accepted part identities", prepared, summary.PartCount())
	}
	if _, found, err := s.GetCapture(ctx, summary.ContentID()); err != nil || found {
		t.Fatal("prepared capture was not erased", found, err)
	}
}

func TestDeletionPreparationPagesIndexedRangesAndFullTaskSnapshots(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	summary := publishIndexedFixture(t, s, strings.Repeat(" ", capture.MaxCapturePartTextBytes-5)+"seamquartzword finished calibration.", time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC))
	drainCaptureIndex(t, s)
	want := []string{summary.ContentID()}
	var ranges []string
	if err := s.view(ctx, func(reader bolt.Reader) error {
		edition, _, _, err := s.captureEditionReader(reader, summary.ContentID())
		if err != nil {
			return err
		}
		for part := int64(0); part < summary.PartCount(); part++ {
			head, err := s.captureRangeHead(reader, summary.ContentID(), edition.Publication, part)
			if err != nil {
				return err
			}
			for head != "" {
				ranges = append(ranges, head)
				mapping, _, err := s.captureIndexPartReader(reader, head)
				if err != nil {
					return err
				}
				head = mapping.Next
			}
			want = append(want, capturePartSourceID(summary.ContentID(), edition.Publication, part))
		}
		return nil
	}); err != nil || len(ranges) == 0 {
		t.Fatal("missing indexed range fixture", ranges, err)
	}
	want = append(want, ranges...)
	for n := 1; n <= tasks.MaxSourceTasks; n++ {
		cleanupTask(t, s, n, ranges[0])
	}
	cleanupTask(t, s, tasks.MaxSourceTasks+1, want[1])
	seen := map[string]bool{}
	if err := s.DeleteSourceRecordWithPrepare(ctx, ranges[0], func(id string, items []tasks.Item) error {
		if !s.state.deletionPending.Load() || !slices.Contains(want, id) || seen[id] || len(items) > tasks.MaxSourceTasks {
			t.Fatal("preparation escaped the gate, duplicated, or invented a source", id, len(items))
		}
		if id == ranges[0] && len(items) != tasks.MaxSourceTasks || id == want[1] && len(items) != 1 {
			t.Fatal("preparation lost authenticated task references", id, len(items))
		}
		for _, item := range items {
			if !item.References(id) {
				t.Fatal("snapshot crossed source authority", id)
			}
		}
		seen[id] = true
		return nil
	}); err != nil || len(seen) != len(want) {
		t.Fatal("paged preparation omitted original/range authority", len(seen), len(want), err)
	}
}

func TestDeletionPreparationFailureAndCancellationDoNotCommitErasure(t *testing.T) {
	for _, kind := range []string{"source", "conditional", "family", "note"} {
		for _, canceled := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/native-failure", true: "/cancellation"}[canceled], func(t *testing.T) {
				s := openReplicaTestStore(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				at := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
				refusal := errors.New("native cancellation unavailable")
				calls := 0
				prepare := func(string, []tasks.Item) error {
					calls++
					if !s.state.deletionPending.Load() {
						t.Fatal("preparation released its exclusive deletion gate")
					}
					if calls == 2 {
						if canceled {
							cancel()
							return nil
						}
						return refusal
					}
					return nil
				}
				var err error
				var sourceID string
				if kind == "note" {
					first, e := s.CreateNoteDocument(ctx, "Original note revision", at)
					if e != nil {
						t.Fatal(e)
					}
					second, e := s.UpdateNoteDocument(ctx, first.ID, first.Revision, "Current note revision", at.Add(time.Minute))
					if e != nil {
						t.Fatal(e)
					}
					sourceID = second.SourceID
					err = s.DeleteNoteDocumentWithPrepare(ctx, second.ID, second.Revision, prepare)
				} else {
					summary := publishIndexedFixture(t, s, strings.Repeat(" ", capture.MaxCapturePartTextBytes)+"Original capture remains", at)
					sourceID = capturePartSourceID(summary.ContentID(), capturePublicationFixture(t, s, summary.ContentID()), 0)
					if kind == "family" {
						cutoff := at.Add(time.Hour)
						_, err = s.ForgetCaptureFamily(ctx, summary.Header().FamilyID(), cutoff, prepare)
					} else if kind == "conditional" {
						_, err = s.DeleteSourceRecordBeforeWithPrepare(ctx, summary.ContentID(), at.Add(time.Hour), true, prepare)
					} else {
						err = s.DeleteSourceRecordWithPrepare(ctx, summary.ContentID(), prepare)
					}
				}
				wantErr := refusal
				if canceled {
					wantErr = context.Canceled
				}
				if !errors.Is(err, wantErr) || calls != 2 {
					t.Fatal("preparation did not propagate the exact failure", calls, err)
				}
				if _, found, err := s.GetSourceEvidence(context.Background(), sourceID); err != nil || !found {
					t.Fatal("failed preparation fenced or erased original source", found, err)
				}
				if err := s.view(context.Background(), func(reader bolt.Reader) error {
					_, pending, err := s.readIndexValueReader(reader, erasureNamespace, erasureJournalID, 2048)
					if pending && err == nil {
						return ErrCorrupt
					}
					return err
				}); err != nil {
					t.Fatal("failed preparation committed an erasure intent", err)
				}
			})
		}
	}
}

func capturePublicationFixture(t *testing.T, s *Store, id string) string {
	t.Helper()
	var publication string
	if err := s.view(context.Background(), func(reader bolt.Reader) error {
		edition, _, found, err := s.captureEditionReader(reader, id)
		if err == nil && !found {
			return ErrCorrupt
		}
		publication = edition.Publication
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return publication
}

func TestDeletionPreparationRetainsExclusiveGateBetweenSnapshots(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	summary := publishIndexedFixture(t, s, strings.Repeat(" ", capture.MaxCapturePartTextBytes)+"Saved original", time.Now().UTC())
	entered, resume := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	refusal := errors.New("stop before journal admission")
	go func() {
		calls := 0
		done <- s.DeleteSourceRecordWithPrepare(ctx, summary.ContentID(), func(string, []tasks.Item) error {
			calls++
			if calls == 2 {
				close(entered)
				<-resume
				return refusal
			}
			return nil
		})
	}()
	<-entered
	if acquired := s.state.mu.TryRLock(); acquired {
		s.state.mu.RUnlock()
		close(resume)
		<-done
		t.Fatal("preparation released exclusive authority between bounded reads")
	}
	_, _, readErr := s.GetCapture(ctx, summary.ContentID())
	o, _ := capture.SanitizeObservation("editor.app", "other.example", "Concurrent source", 4096)
	writeErr := s.PutObservation(ctx, o)
	close(resume)
	err := <-done
	if !errors.Is(readErr, ErrDeletionPending) || !errors.Is(writeErr, ErrDeletionPending) || !errors.Is(err, refusal) {
		t.Fatal("concurrent publication escaped native preparation authority", readErr, writeErr, err)
	}
}

func TestDeletionPreparationRejectsCyclicRangeDirectoryWithoutJournal(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	summary := publishIndexedFixture(t, s, strings.Repeat(" ", capture.MaxCapturePartTextBytes-5)+"seamquartzword ends.", time.Now().UTC())
	drainCaptureIndex(t, s)
	if err := s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		edition, _, _, err := s.captureEditionReader(tx.writer, summary.ContentID())
		if err != nil {
			return err
		}
		head, err := s.captureRangeHead(tx.writer, summary.ContentID(), edition.Publication, 0)
		if err != nil || head == "" {
			return ErrCorrupt
		}
		first, _, err := s.captureIndexPartReader(tx.writer, head)
		if err != nil || first.Start == 0 {
			return ErrCorrupt
		}
		second := first
		second.Start--
		second.Bytes++
		second.Next = head
		first.Next = second.sourceID()
		if err := tx.capturePutJSONLocked("index-part:"+head, first, 768); err != nil {
			return err
		}
		return tx.capturePutJSONLocked("index-part:"+second.sourceID(), second, 768)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSourceRecordWithPrepare(ctx, summary.ContentID(), func(string, []tasks.Item) error { return nil }); !errors.Is(err, ErrCorrupt) {
		t.Fatal("cyclic metadata did not fail before journal admission", err)
	}
	if _, found, err := s.GetCapture(ctx, summary.ContentID()); err != nil || !found {
		t.Fatal("cyclic preparation erased the original", found, err)
	}
}
