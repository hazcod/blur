package client

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestAppointmentRecallUsesEventDateWithoutChangingCaptureProvenance(t *testing.T) {
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "fixture.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := NewClient(s)
	defer c.Close()
	observed := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	c.askNow = func() time.Time { return time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC) }
	o, err := capture.SanitizeObservation("com.apple.mail", "", "Appointment confirmation: your physiotherapy appointment with José is Monday, 23 November 2026 at 14:30. An end time was not provided.", capture.MaxObservationTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(ctx, o, observed); err != nil {
		t.Fatal(err)
	}
	for {
		more, err := s.IndexPendingExact(ctx, 32)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	for _, question := range []string{
		"What time is my physiotherapy appointment on Monday?",
		"What time is my physiotherapy appointment on Monday from the email I read on Thursday?",
		"What did I read about physiotherapy on Thursday?",
		"When will I see José on Monday?",
		"When do I see José on Monday?",
		"What time am I seeing José on Monday?",
	} {
		result, err := c.AskMemory(ctx, question)
		if err != nil || !slices.Contains(result.Answer.SourceIDs, o.ContentID()) || result.Answer.InsufficientEvidence {
			t.Fatalf("appointment confirmation disappeared: %q %+v %v", question, result, err)
		}
		if !strings.Contains(result.Answer.Text, "14:30") || !strings.Contains(result.Answer.Text, "end time was not provided") {
			t.Fatalf("extractive answer lost supported time or uncertainty: %+v", result)
		}
		for _, source := range result.Sources {
			if source.SourceID == o.ContentID() && !source.CapturedAt.Equal(observed) {
				t.Fatalf("event date replaced capture provenance: %+v", source)
			}
		}
	}
	result, err := c.AskMemory(ctx, "What did I read about physiotherapy on Monday?")
	if err != nil || !result.Answer.InsufficientEvidence || len(result.Answer.SourceIDs) != 0 {
		t.Fatalf("event date satisfied an observation-date question: %+v %v", result, err)
	}
	other, err := capture.SanitizeObservation("com.microsoft.Outlook", "", "Appointment confirmation: your physiotherapy appointment with José is Tuesday, 24 November 2026 at 09:45. An end time was not provided.", capture.MaxObservationTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(ctx, other, observed); err != nil {
		t.Fatal(err)
	}
	for {
		more, err := s.IndexPendingExact(ctx, 32)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	first, err := c.AskMemory(ctx, "What time is my physiotherapy appointment on Monday?")
	if err != nil {
		t.Fatal(err)
	}
	next, err := c.AskMemoryWithHistory(ctx, "And on Tuesday?", []assistant.Turn{{Question: first.Plan.Question, Plan: first.Plan, SourceIDs: first.Answer.SourceIDs}})
	if err != nil || !slices.Contains(next.Answer.SourceIDs, other.ContentID()) || !next.Plan.Scope.IsZero() || slices.Contains(next.Plan.Terms, "monday") || !strings.Contains(next.Answer.Text, "09:45") {
		t.Fatalf("event-date continuation lost the new appointment: %+v %v", next, err)
	}
}

func TestEventDateFollowupsKeepNumericTopicAndOriginalProvenance(t *testing.T) {
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "fixture.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := NewClient(s)
	defer c.Close()
	observed := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	c.askNow = func() time.Time { return time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC) }
	var ids []string
	for _, text := range []string{
		"Route 23 bus departure on Monday 23 November 2026 at 14:30. The boarding point is platform 8.",
		"Route 23 bus departure on Tuesday 24 November 2026 at 16:00. The boarding point is platform 8.",
		"Route 23 bus departure on Wednesday 25 November 2026 at 18:00. The boarding point is platform 8.",
		"Route 42 bus departure on Tuesday 24 November 2026 at 09:00. The boarding point is platform 2.",
	} {
		o, err := capture.SanitizeObservation("com.apple.mail", "", text, capture.MaxObservationTextBytes)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(ctx, o, observed); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, o.ContentID())
	}
	for {
		more, err := s.IndexPendingExact(ctx, 32)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	var history []assistant.Turn
	for _, tc := range []struct {
		question, weekday string
		source            int
	}{
		{"What time is my route 23 bus on Monday 23 November 2026?", "monday", 0},
		{"And in Mail?", "monday", 0},
		{"And on Tuesday 24 November 2026?", "tuesday", 1},
		{"Make it shorter", "tuesday", 1},
		{"And on Wednesday 25 November 2026?", "wednesday", 2},
	} {
		result, err := c.AskMemoryWithHistory(ctx, tc.question, history)
		if err != nil || result.Answer.InsufficientEvidence || !slices.Contains(result.Plan.Terms, "23") || !result.Plan.Scope.IsZero() || len(result.Sources) == 0 || result.Sources[0].SourceID != ids[tc.source] || !result.Sources[0].CapturedAt.Equal(observed) || !slices.Contains(result.Answer.SourceIDs, ids[tc.source]) {
			t.Fatalf("date continuation lost numeric topic or source provenance: %q %+v %v", tc.question, result, err)
		}
		for _, weekday := range []string{"monday", "tuesday", "wednesday"} {
			if slices.Contains(result.Plan.Terms, weekday) != (weekday == tc.weekday) {
				t.Fatalf("event follow-up retained an obsolete date: %q %+v", tc.question, result.Plan)
			}
		}
		history = append(history, assistant.Turn{Question: tc.question, Plan: result.Plan, SourceIDs: result.Answer.SourceIDs})
	}
}
