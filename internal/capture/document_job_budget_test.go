package capture

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func budgetDocumentJob(t *testing.T, guard func(context.Context) error) *axCaptureDocumentJob {
	t.Helper()
	_, node := documentTestRoot()
	node.text = "Synthetic complete source retained across final verification."
	cursor, err := newAXDocumentCursor(node.ref(), 32, func(context.Context) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewCaptureBuilder("editor.app", "", strings.Repeat("a", 64), time.Now().UTC())
	if err != nil {
		cursor.close()
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.MaxDuration = 15 * time.Millisecond
	job := &axCaptureDocumentJob{cursor: cursor, builder: builder, limits: limits, guard: guard}
	t.Cleanup(job.Close)
	return job
}

func TestDocumentFinishDeadlineRetriesIdenticalPartWithoutLosingText(t *testing.T) {
	job := budgetDocumentJob(t, func(context.Context) error { return nil })
	var attempted, saved []CapturePart
	sink := func(ctx context.Context, parts []CapturePart) error {
		if attempted == nil {
			attempted = append(attempted, parts...)
			<-ctx.Done()
			return ctx.Err()
		}
		saved = append(saved, parts...)
		return nil
	}
	first, err := job.Read(context.Background(), sink)
	if err != nil || first.Done || first.Summary.Validate() == nil || len(attempted) != 1 || len(saved) != 0 {
		t.Fatal("Finish timeout discarded resumable input or exposed uncommitted summary", err)
	}
	second, err := job.Read(context.Background(), sink)
	if err != nil || !second.Done || second.Summary.ValidateForAdmission() != nil || len(saved) != 1 || attempted[0].Digest() != saved[0].Digest() || saved[0].Text() != "Synthetic complete source retained across final verification." {
		t.Fatal("Finish retry lost text, changed part identity or repeated output", err)
	}
}

func TestDocumentOwnerCancellationIsNotRoundContinuation(t *testing.T) {
	owner, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := budgetDocumentJob(t, func(ctx context.Context) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	})
	progress, err := job.Read(owner, func(context.Context, []CapturePart) error { t.Fatal("canceled owner emitted content"); return nil })
	if !errors.Is(err, context.Canceled) || progress.Done || progress.Summary.Validate() == nil {
		t.Fatal("owner cancellation became hidden continuation", err)
	}
}

func TestDocumentWhitespaceOnlyYieldsNoContent(t *testing.T) {
	job := budgetDocumentJob(t, func(context.Context) error { return nil })
	job.cursor.stack[0].node.(*documentTestRef).node.text = " \n\t \n"
	progress, err := job.Read(context.Background(), func(context.Context, []CapturePart) error { return nil })
	var ce *CaptureError
	if !errors.As(err, &ce) || ce.Code != NoContent || progress.Done || progress.Summary.Validate() == nil {
		t.Fatal("whitespace-only document falsely suppressed the visual fallback", err)
	}
}

func TestDocumentRoundBudgetIncludesInitialGuardAndTerminatesNoProgress(t *testing.T) {
	guard := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	job := budgetDocumentJob(t, guard)
	owner, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	sink := func(context.Context, []CapturePart) error { t.Fatal("guard timeout acquired source text"); return nil }
	for i := range 3 {
		started := time.Now()
		progress, err := job.Read(owner, sink)
		if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
			t.Fatal("initial guard escaped the per-round deadline", elapsed)
		}
		if owner.Err() != nil || progress.Done || progress.Bytes != 0 {
			t.Fatal("round timeout canceled owner or exposed incomplete summary")
		}
		if i < 2 && err != nil {
			t.Fatal("round guard timeout lost continuation", err)
		}
		if i == 2 {
			var ce *CaptureError
			if !errors.As(err, &ce) || ce.Code != TimedOut {
				t.Fatal("permanent guard stall never became visible timeout", err)
			}
		}
	}
}

func TestDocumentFinalGuardYieldRetainsSummaryAndRevalidatesBeforePublication(t *testing.T) {
	for _, scenario := range []string{"resume", "excluded", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			checks := 0
			deny := false
			job := budgetDocumentJob(t, func(ctx context.Context) error {
				checks++
				if deny {
					return captureError(Excluded, nil)
				}
				if checks == 3 {
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			})
			writes := 0
			sink := func(context.Context, []CapturePart) error { writes++; return nil }
			owner, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			first, err := job.Read(owner, sink)
			if err != nil || first.Done || first.Summary.Validate() == nil || writes != 1 {
				t.Fatal("final verification deadline discarded summary or published it without authority", err)
			}
			if scenario == "excluded" {
				deny = true
			}
			if scenario == "cancel" {
				cancel()
			}
			second, err := job.Read(owner, sink)
			if scenario == "resume" {
				if err != nil || !second.Done || second.Summary.ValidateForAdmission() != nil || writes != 1 || checks != 4 {
					t.Fatal("resume lost completed summary, repeated Finish, or skipped fresh guard", err)
				}
			} else {
				if err == nil || second.Done || second.Summary.Validate() == nil || writes != 1 {
					t.Fatal("late canceled/excluded summary escaped", err)
				}
			}
		})
	}
}
