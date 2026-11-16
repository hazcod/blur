package client

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

type meetingSummaryModel struct {
	mu                   sync.Mutex
	packed               assistant.PackedContext
	calls                int
	started              chan context.Context
	release              chan struct{}
	invalidCitation      bool
	invalidCitationCalls int
	insufficient         bool
	insufficientCalls    int
}

func (*meetingSummaryModel) CloseIdleConnections() {}
func (*meetingSummaryModel) CountInputTokens(context.Context, string, string) (int, error) {
	return 100, nil
}
func (g *meetingSummaryModel) Generate(ctx context.Context, p assistant.PackedContext, _ int) (assistant.ModelAnswer, error) {
	g.mu.Lock()
	g.calls++
	call := g.calls
	g.packed = p
	g.mu.Unlock()
	if g.started != nil {
		g.started <- ctx
	}
	if g.release != nil {
		<-g.release
	}
	if g.insufficient || call <= g.insufficientCalls {
		return assistant.ModelAnswer{InsufficientEvidence: true}, nil
	}
	ids := append([]string(nil), p.SourceIDs...)
	if g.invalidCitation || call <= g.invalidCitationCalls {
		ids = []string{strings.Repeat("f", 64)}
	}
	return assistant.ModelAnswer{Text: "This page records a launch decision and a manual follow-up.", SourceIDs: ids}, nil
}

func TestMeetingSummaryPackedSuffixContinuesWithoutSkippingSavedRows(t *testing.T) {
	t.Run("insufficient-prefix", func(t *testing.T) { testMeetingSummaryPackedSuffix(t, false) })
	t.Run("invalid-prefix", func(t *testing.T) { testMeetingSummaryPackedSuffix(t, true) })
}

func testMeetingSummaryPackedSuffix(t *testing.T, invalid bool) {
	t.Helper()
	c, store, g, m := summaryFixture(t, 0)
	if invalid {
		g.invalidCitationCalls = 2
	} else {
		g.insufficientCalls = 1
	}
	for i := 0; i < 50; i++ {
		text := fmt.Sprintf("Passage %d. ", i) + strings.Repeat("TCP congestion control adapts the sending rate to network capacity. ", 3)
		r := meetings.Record{Key: fmt.Sprintf("%s/transcript/microphone/packed-%d", m.ID, i), Kind: meetings.TranscriptFinal, SessionID: m.ID, Event: meetings.Event{Kind: meetings.Final, SessionID: m.ID, TrackID: "microphone", SegmentID: fmt.Sprintf("packed-%d", i), Revision: 1, Start: time.Duration(i) * time.Second, End: time.Duration(i+1) * time.Second, Text: text}}
		if _, err := store.Append(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	var after uint64
	pages := 0
	for pages < 10 {
		result, page, err := c.SummarizeLocalMeetingPage(t.Context(), m.ID, after)
		if pages == 0 {
			want := assistant.ErrSummaryInsufficientEvidence
			if invalid {
				want = assistant.ErrSummaryInvalidResponse
			}
			if !errors.Is(err, want) || result.Answer.Text != "" || len(result.Sources) != 0 || result.SentItems >= 50 {
				t.Fatal("insufficient tight prefix lost admitted-row metadata or retained a refusal", result.SentItems, err)
			}
		} else if err != nil || result.CoverageLimited || len(result.Sources) != result.SentItems {
			t.Fatal("meaningful suffix failed after insufficient tight prefix", err)
		}
		if result.Omitted != 0 || result.Truncated != 0 || len(page.Records) != result.SentItems || page.Next <= after {
			t.Fatalf("packed prefix cursor is dishonest: sent=%d rows=%d next=%d omitted=%d truncated=%d limited=%t err=%v", result.SentItems, len(page.Records), page.Next, result.Omitted, result.Truncated, result.CoverageLimited, err)
		}
		for i, row := range page.Records {
			if seen[row.SourceID] || row.Sequence != after+uint64(i)+1 {
				t.Fatal("packed suffix was skipped, duplicated or reordered")
			}
			seen[row.SourceID] = true
		}
		pages++
		if !page.More {
			break
		}
		after = page.Next
	}
	if len(seen) != 50 || pages < 2 {
		t.Fatal("fixture did not prove suffix continuation", len(seen), pages)
	}
}

func TestMeetingSummaryNeverPresentsAskRefusalAsRecap(t *testing.T) {
	for _, localOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("local-only-%t", localOnly), func(t *testing.T) {
			c, store, g, m := summaryFixture(t, 3)
			g.insufficient = true
			result, _, err := c.summarizeMeetingPage(context.Background(), m.ID, 0, localOnly)
			if err == nil || result.Answer.Text != "" || len(result.Sources) != 0 {
				t.Fatalf("general Ask refusal presented as successful recap: insufficient=%v err=%v", result.Answer.InsufficientEvidence, err)
			}
			page, readErr := store.MeetingRecords(context.Background(), m.ID, meetings.PageRequest{})
			if readErr != nil || len(page.Records) != 3 {
				t.Fatal("recap failure changed the saved transcript", readErr)
			}
		})
	}
}

func TestMeetingSummaryPacksFiftyConsecutiveTranscriptFragments(t *testing.T) {
	c, _, g, m := summaryFixture(t, 51)
	result, page, err := c.SummarizeLocalMeetingPage(context.Background(), m.ID, 0)
	if err != nil || result.SentItems != 50 || result.CoverageLimited || len(result.Sources) != 50 || len(page.Records) != 50 || !page.More || page.Next != 50 {
		t.Fatalf("fragmented transcript lost coherent context: sent=%d sources=%d page=%d next=%d limited=%v err=%v", result.SentItems, len(result.Sources), len(page.Records), page.Next, result.CoverageLimited, err)
	}
	if !strings.Contains(g.packed.User, "Saved decision 49") || strings.Contains(g.packed.User, "Saved decision 50") {
		t.Fatal("transcript context did not retain the exact consecutive page")
	}
}
func summaryFixture(t *testing.T, n int) (*Client, *durable.Store, *meetingSummaryModel, meetings.Meeting) {
	t.Helper()
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	m := meetings.Meeting{ID: "summary-fixture", Title: "Private launch planning", Locale: "en-US", StartedAt: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC), State: meetings.Running, Tracks: []string{"microphone"}, Revision: 1}
	if err := store.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		r := meetings.Record{Key: fmt.Sprintf("%s/transcript/microphone/segment-%d", m.ID, i), Kind: meetings.TranscriptFinal, SessionID: m.ID, Event: meetings.Event{Kind: meetings.Final, SessionID: m.ID, TrackID: "microphone", SegmentID: fmt.Sprintf("segment-%d", i), Revision: 2, Start: time.Duration(i) * time.Second, End: time.Duration(i+1) * time.Second, Text: fmt.Sprintf("Saved decision %d: launch after review.", i)}}
		if _, err := store.Append(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	g := &meetingSummaryModel{}
	c := NewClient(store)
	c.localAI = &localAIController{path: filepath.Join(t.TempDir(), "ai.json"), settings: LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}, askModel: g, service: notes.NewService(store, nil, nil)}
	t.Cleanup(c.Close)
	return c, store, g, m
}
func TestMeetingSummaryUsesSavedPageAndManualProvenance(t *testing.T) {
	c, store, g, m := summaryFixture(t, 1)
	ctx := context.Background()
	note := meetings.Record{Key: m.ID + "/note/manual", Kind: meetings.UserNote, SessionID: m.ID, Note: meetings.ManualNoteInput{ID: "manual", At: 2 * time.Second, Text: "Manual note: ask Mira to review; no reminder is authorized."}}
	if _, err := store.Append(ctx, note); err != nil {
		t.Fatal(err)
	}
	result, err := c.SummarizeMeetingPage(ctx, m.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.General || result.Direct || result.SentItems != 2 || len(result.Sources) != 2 || result.CoverageLimited || g.calls != 1 {
		t.Fatalf("not an actual cited page summary: sent=%d sources=%d calls=%d err=%v", result.SentItems, len(result.Sources), g.calls, err)
	}
	if !strings.Contains(g.packed.User, "Saved decision 0") || !strings.Contains(g.packed.User, note.Note.Text) || !strings.Contains(g.packed.User, "Meeting manual note") || !strings.Contains(g.packed.User, "microphone") {
		t.Fatal("saved page text or authorship provenance missing")
	}
	if !result.Sources[0].CapturedAt.Equal(m.StartedAt) || !result.Sources[0].EndAt.Equal(m.StartedAt.Add(time.Second)) || !result.Sources[1].CapturedAt.Equal(m.StartedAt.Add(2*time.Second)) {
		t.Fatal("exact saved event spans lost")
	}
	metadata, found, err := store.GetMeeting(ctx, m.ID)
	if err != nil || !found || metadata.RecordCount != 2 || metadata.Revision != 1 {
		t.Fatal("summary altered saved meeting")
	}
}
func TestMeetingSummaryUnavailableIsHonestAndDoesNotLoseTranscript(t *testing.T) {
	for _, scenario := range []string{"disabled", "missing-model", "remote-no-consent", "needs-repair", "empty-page"} {
		t.Run(scenario, func(t *testing.T) {
			c, store, g, m := summaryFixture(t, 1)
			after := uint64(0)
			switch scenario {
			case "disabled":
				c.localAI.settings.Enabled = false
			case "missing-model":
				c.localAI.askModel = nil
			case "remote-no-consent":
				c.localAI.settings.Provider = ProviderClaude
			case "needs-repair":
				c.localAI.needsRepair = true
			case "empty-page":
				after = 1
			}
			result, err := c.SummarizeMeetingPage(context.Background(), m.ID, after)
			want := assistant.ErrAskUnavailable
			if scenario == "empty-page" {
				want = assistant.ErrSummaryInsufficientEvidence
			}
			if !errors.Is(err, want) || result.Answer.Text != "" || g.calls != 0 {
				t.Fatalf("unavailable summary was presented as generated: calls=%d err=%v", g.calls, err)
			}
			page, err := store.MeetingRecords(context.Background(), m.ID, meetings.PageRequest{})
			if err != nil || len(page.Records) != 1 {
				t.Fatal("unavailable summary affected raw recording")
			}
		})
	}
}
func TestMeetingSummaryMarksPageAndPackingCoverage(t *testing.T) {
	for _, n := range []int{2, 22} {
		t.Run(fmt.Sprintf("records-%d", n), func(t *testing.T) {
			c, _, _, m := summaryFixture(t, n)
			after := uint64(0)
			if n == 2 {
				after = 1
			}
			result, err := c.SummarizeMeetingPage(context.Background(), m.ID, after)
			if err != nil || !result.CoverageLimited || result.SentItems == 0 {
				t.Fatalf("partial page claimed full coverage: sent=%d err=%v", result.SentItems, err)
			}
		})
	}
	c, store, _, m := summaryFixture(t, 0)
	r := meetings.Record{Key: m.ID + "/note/large", Kind: meetings.UserNote, SessionID: m.ID, Note: meetings.ManualNoteInput{ID: "large", Text: strings.Repeat("Meeting context and discussion. ", 1000)}}
	if _, err := store.Append(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	result, err := c.SummarizeMeetingPage(context.Background(), m.ID, 0)
	if err != nil || !result.CoverageLimited || result.Truncated == 0 {
		t.Fatalf("model excerpt not disclosed: truncated=%d err=%v", result.Truncated, err)
	}
}
func TestMeetingSummaryRejectsUnknownModelCitation(t *testing.T) {
	c, _, g, m := summaryFixture(t, 1)
	g.invalidCitation = true
	result, err := c.SummarizeMeetingPage(context.Background(), m.ID, 0)
	if !errors.Is(err, assistant.ErrAnswer) || result.Answer.Text != "" {
		t.Fatalf("unknown source citation published: %v", err)
	}
}
func TestMeetingSummaryRejectsLateGenerationAfterPolicyOrMemoryChange(t *testing.T) {
	for _, change := range []string{"policy", "memory", "close"} {
		t.Run(change, func(t *testing.T) {
			c, _, g, m := summaryFixture(t, 1)
			g.started = make(chan context.Context, 1)
			g.release = make(chan struct{})
			done := make(chan error, 1)
			go func() {
				result, err := c.SummarizeMeetingPage(context.Background(), m.ID, 0)
				if result.Answer.Text != "" {
					done <- errors.New("stale summary published")
					return
				}
				done <- err
			}()
			var inference context.Context
			select {
			case inference = <-g.started:
			case <-time.After(2 * time.Second):
				close(g.release)
				t.Fatal("summary did not reach synthetic model")
			}
			var closed chan struct{}
			switch change {
			case "policy":
				if err := c.ConfigureLocalAI(context.Background(), LocalAISettings{}); err != nil {
					close(g.release)
					t.Fatal(err)
				}
			case "memory":
				c.NotifyMemoryChanging()
				c.NotifyMemoryStable()
			case "close":
				closed = make(chan struct{})
				go func() { c.Close(); close(closed) }()
			}
			select {
			case <-inference.Done():
			case <-time.After(time.Second):
				close(g.release)
				t.Fatal("inference was not canceled")
			}
			close(g.release)
			if err := <-done; !errors.Is(err, context.Canceled) && !errors.Is(err, ErrMemoryChanging) {
				t.Fatalf("stale summary error=%v", err)
			}
			if closed != nil {
				<-closed
			}
		})
	}
}

func TestMeetingSummaryRemoteConsentUsesSameSelectedModel(t *testing.T) {
	c, _, g, m := summaryFixture(t, 1)
	c.localAI.settings.Provider = ProviderClaude
	c.localAI.settings.RemoteAskEnabled = true
	result, err := c.SummarizeMeetingPage(context.Background(), m.ID, 0)
	if err != nil || g.calls != 1 || result.General || result.SentItems != 1 {
		t.Fatalf("consented selected model not used: calls=%d err=%v", g.calls, err)
	}
}
func TestMeetingSummaryRejectsDeletedSourceEvenWithoutClientNotification(t *testing.T) {
	c, store, g, m := summaryFixture(t, 1)
	g.started = make(chan context.Context, 1)
	g.release = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		result, err := c.SummarizeMeetingPage(context.Background(), m.ID, 0)
		if result.Answer.Text != "" {
			done <- errors.New("erased source summary published")
			return
		}
		done <- err
	}()
	select {
	case <-g.started:
	case <-time.After(time.Second):
		close(g.release)
		t.Fatal("summary did not reach fixture")
	}
	page, err := store.MeetingRecords(context.Background(), m.ID, meetings.PageRequest{})
	if err != nil || len(page.Records) != 1 {
		close(g.release)
		t.Fatal("fixture record missing")
	}
	if err := store.DeleteObservationRecord(context.Background(), page.Records[0].SourceID); err != nil {
		close(g.release)
		t.Fatal(err)
	}
	close(g.release)
	if err := <-done; !errors.Is(err, ErrMemoryChanging) {
		t.Fatalf("deleted source was not revalidated: %v", err)
	}
}

func TestMeetingSummaryPreservesSeparateRecordingAndManualWallTimes(t *testing.T) {
	c, store, _, m := summaryFixture(t, 0)
	ctx := context.Background()
	// Only an unrecorded Ready agenda may acquire a recording anchor.
	m.State = meetings.Ready
	m.Revision = 2
	if err := store.PutMeeting(ctx, m, 1); err != nil {
		t.Fatal(err)
	}
	m.RecordingStartedAt = m.StartedAt.Add(24 * time.Hour)
	m.Revision = 3
	if err := store.PutMeeting(ctx, m, 2); err != nil {
		t.Fatal(err)
	}
	m.State = meetings.Running
	m.Revision = 4
	if err := store.PutMeeting(ctx, m, 3); err != nil {
		t.Fatal(err)
	}
	final := meetings.Record{Key: m.ID + "/transcript/microphone/today", SessionID: m.ID, Kind: meetings.TranscriptFinal, Event: meetings.Event{Kind: meetings.Final, SessionID: m.ID, TrackID: "microphone", SegmentID: "today", Revision: 1, Start: time.Second, End: 2 * time.Second, Text: "Today's recording follows yesterday's agenda."}}
	if _, err := store.Append(ctx, final); err != nil {
		t.Fatal(err)
	}
	note := meetings.Record{Key: m.ID + "/note/later", SessionID: m.ID, Kind: meetings.UserNote, Note: meetings.ManualNoteInput{ID: "later", RecordedAt: m.StartedAt.Add(72 * time.Hour), Text: "Later independent manual note."}}
	if _, err := store.Append(ctx, note); err != nil {
		t.Fatal(err)
	}
	result, err := c.SummarizeMeetingPage(ctx, m.ID, 0)
	if err != nil || len(result.Sources) != 2 {
		t.Fatalf("new clocks not admitted by summary: sources=%d err=%v", len(result.Sources), err)
	}
	if !result.Sources[0].CapturedAt.Equal(m.RecordingStartedAt.Add(time.Second)) || !result.Sources[0].EndAt.Equal(m.RecordingStartedAt.Add(2*time.Second)) || !result.Sources[1].CapturedAt.Equal(note.Note.RecordedAt) || !result.Sources[1].EndAt.IsZero() {
		t.Fatal("summary rewrote original transcript/manual provenance")
	}
}
