package client

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

func queuedVoiceCallback(t *testing.T, app *pausedAskApp) func() {
	t.Helper()
	select {
	case fn := <-app.posted:
		return fn
	case <-time.After(5 * time.Second):
		t.Fatal("voice result did not queue")
		return nil
	}
}

// Reading an original and installing a recap can overlap without a mutation.
// Hold one tiny installation at a controlled boundary to expose that overlap.
func TestVoiceReadAllowsConcurrentReadPublication(t *testing.T) {
	c, _, _, _ := summaryFixture(t, 1)
	read, stable := c.beginVoicePublication()
	if !stable {
		t.Fatal("fixture read was unstable")
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan bool, 1)
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	go func() {
		done <- read.publish(c, func() { close(entered); <-release })
	}()
	<-entered
	next, stable := c.beginVoicePublication()
	if !stable || !next.publish(c, func() {}) {
		unblock()
		<-done
		t.Fatal("an unchanged concurrent reader was classified as a memory mutation")
	}
	unblock()
	if !<-done {
		t.Fatal("first unchanged read was rejected")
	}
}

func TestVoiceReadDoesNotBlockAskRegistration(t *testing.T) {
	c, _, _, _ := summaryFixture(t, 1)
	read, stable := c.beginVoicePublication()
	if !stable {
		t.Fatal("fixture read was unstable")
	}
	entered, release, published := make(chan struct{}), make(chan struct{}), make(chan bool, 1)
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	go func() { published <- read.publish(c, func() { close(entered); <-release }) }()
	<-entered
	registered := make(chan bool, 1)
	go func() {
		id, ok := c.registerAskEpoch(func() {}, &read.memoryEpoch)
		if ok {
			c.unregisterAsk(id)
		}
		registered <- ok
	}()
	select {
	case ok := <-registered:
		if !ok {
			t.Error("unchanged read prevented Ask registration")
		}
	case <-time.After(200 * time.Millisecond):
		unblock()
		<-registered
		t.Error("Ask bookkeeping contends with evidence publication")
	}
	unblock()
	if !<-published {
		t.Fatal("unchanged original was rejected")
	}
}

func TestVoiceArchiveQueuedOriginalPageKeepsReadAuthority(t *testing.T) {
	for _, mutation := range []string{"unchanged", "unrelated-capture", "erase", "replace", "another-note", "new-request", "close"} {
		t.Run(mutation, func(t *testing.T) {
			count := 1
			if mutation == "replace" {
				count = 2
			} // Keep the session live after removing one immutable record.
			c, s, _, m := summaryFixture(t, count)
			page, err := c.MeetingTranscript(context.Background(), m.ID, 0)
			if err != nil || len(page.Records) != count {
				t.Fatalf("fixture: %+v %v", page, err)
			}
			app := &pausedAskApp{posted: make(chan func(), 16)}
			d := &desktop{client: c, app: app, page: "Ask"}
			d.meetingUI.selected = m.ID
			d.loadMeetingTranscript(0)
			publish := queuedVoiceCallback(t, app)
			switch mutation {
			case "unrelated-capture":
				putUnrelatedVoiceCapture(t, s, m.StartedAt)
			case "erase", "replace":
				if err := s.DeleteObservationRecord(context.Background(), page.Records[0].SourceID); err != nil {
					t.Fatal(err)
				}
				if mutation == "replace" {
					r := meetings.Record{Key: m.ID + "/transcript/microphone/replacement", Kind: meetings.TranscriptFinal, SessionID: m.ID, Event: meetings.Event{Kind: meetings.Final, SessionID: m.ID, TrackID: "microphone", SegmentID: "replacement", Revision: 1, Start: 10 * time.Second, End: 11 * time.Second, Text: "Current replacement words name a different release decision."}}
					if _, err := s.Append(context.Background(), r); err != nil {
						t.Fatal(err)
					}
				}
			case "another-note":
				d.meetingUI.selected = "another-note"
			case "new-request":
				d.meetingUI.request++
			case "close":
				c.Close()
			}
			publish()
			if mutation == "unchanged" || mutation == "unrelated-capture" {
				if d.meetingUI.reading || len(d.meetingUI.transcript.Records) != 1 || d.meetingUI.transcript.Records[0].SourceID != page.Records[0].SourceID {
					t.Fatalf("current original was lost: %+v", &d.meetingUI)
				}
			} else if len(d.meetingUI.transcript.Records) != 0 {
				t.Fatalf("stale original page installed after %s: %+v", mutation, d.meetingUI.transcript)
			}
			if mutation == "erase" || mutation == "replace" {
				if d.meetingUI.reading || d.meetingUI.status == "" || strings.Contains(d.meetingUI.status, "Recording stopped") {
					t.Fatalf("changed read did not recover honestly: %q", d.meetingUI.status)
				}
			}
		})
	}
}

func putUnrelatedVoiceCapture(t *testing.T, s *durable.Store, at time.Time) {
	t.Helper()
	o, err := capture.SanitizeObservation("com.apple.Safari", "garden.example", "The gardening checklist tracks fertilizer and watering dates.", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(context.Background(), o, at); err != nil {
		t.Fatal(err)
	}
}

func TestVoiceArchiveGenerationSurvivesUnrelatedDesktopCapture(t *testing.T) {
	for _, operation := range []string{"page-summary", "native-ask"} {
		t.Run(operation, func(t *testing.T) {
			c, s, g, m := summaryFixture(t, 1)
			c.askNow = func() time.Time { return m.StartedAt.Add(6 * time.Hour) }
			g.started = make(chan context.Context, 4)
			g.release = make(chan struct{})
			release := sync.OnceFunc(func() { close(g.release) })
			defer release()
			type response struct {
				result assistant.Result
				err    error
			}
			done := make(chan response, 1)
			go func() {
				var result assistant.Result
				var err error
				if operation == "page-summary" {
					result, err = c.SummarizeMeetingPage(context.Background(), m.ID, 0)
				} else {
					result, err = c.AskMemory(context.Background(), "What did we discuss in the meeting about launch?")
				}
				done <- response{result, err}
			}()
			select {
			case <-g.started:
			case <-time.After(5 * time.Second):
				t.Fatal("generation did not start")
			}
			putUnrelatedVoiceCapture(t, s, m.StartedAt)
			release()
			select {
			case got := <-done:
				if got.err != nil || len(got.result.Sources) != 1 || got.result.Answer.InsufficientEvidence {
					t.Fatalf("background desktop capture invalidated unchanged voice words: %+v %v", got.result, got.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("generation did not finish")
			}
		})
	}
}

func TestVoiceArchiveBusyWriterHasHonestNonblockingRecovery(t *testing.T) {
	for _, action := range []string{"original-page", "source-link", "recap"} {
		t.Run(action, func(t *testing.T) {
			c, s, _, m := summaryFixture(t, 1)
			page, err := c.MeetingTranscript(context.Background(), m.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			app := &pausedAskApp{posted: make(chan func(), 16)}
			d := &desktop{client: c, app: app, page: "Ask"}
			d.meetingUI.selected = m.ID
			d.memoryUI.meetingDetail = true
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			finish := sync.OnceFunc(func() { close(release) })
			defer finish()
			go func() {
				done <- s.Update(context.Background(), func(tx *durable.Tx) error {
					close(entered)
					<-release
					return tx.PutIndexRecord("sentinel", "voice-busy", []byte("committed"))
				})
			}()
			<-entered
			// beginMemoryPublication must decline a busy writer without reading
			// storage or waiting for that writer on the native event thread.
			switch action {
			case "original-page":
				d.loadMeetingTranscript(0)
				queuedVoiceCallback(t, app)()
			case "source-link":
				d.readMemoryVoiceSource(page.Records[0].SourceID)
			case "recap":
				d.recapSelectedVoiceNote()
			}
			status := d.meetingUI.status
			if action == "source-link" {
				status = d.status
			}
			if status == "" || strings.Contains(status, "Recording stopped") || d.meetingUI.reading || d.memoryUI.recap.loading {
				t.Errorf("busy read gave no honest recovery: %q reading=%v recapLoading=%v", status, d.meetingUI.reading, d.memoryUI.recap.loading)
			}
			finish()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVoiceArchiveQueuedSourceLookupRejectsErasedHandle(t *testing.T) {
	for _, erase := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "erased"}[erase], func(t *testing.T) {
			c, s, _, m := summaryFixture(t, 1)
			page, err := c.MeetingTranscript(context.Background(), m.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			app := &pausedAskApp{posted: make(chan func(), 16)}
			d := &desktop{client: c, app: app, page: "Ask"}
			d.readMemoryVoiceSource(page.Records[0].SourceID)
			publish := queuedVoiceCallback(t, app)
			if erase {
				if err := s.DeleteObservationRecord(context.Background(), page.Records[0].SourceID); err != nil {
					t.Fatal(err)
				}
			}
			publish()
			if erase {
				if d.meetingUI.selected != "" || d.memoryUI.meetingDetail || d.meetingUI.reading || d.status == "" {
					t.Fatalf("erased handle reopened stale voice metadata: %+v %q", &d.meetingUI, d.status)
				}
			} else {
				queuedVoiceCallback(t, app)()
				if d.meetingUI.selected != m.ID || len(d.meetingUI.transcript.Records) != 1 {
					t.Fatalf("live handle failed to open: %+v", &d.meetingUI)
				}
			}
		})
	}
}

func TestVoiceArchiveQueuedAskRejectsErasedSpeech(t *testing.T) {
	for _, tc := range []struct {
		name            string
		erase, followup bool
	}{
		{"unchanged-own-history", false, false}, {"erased", true, false},
		{"followup-unchanged-own-history", false, true}, {"followup-erased", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, s, _, m := summaryFixture(t, 1)
			c.localAI = nil
			c.askNow = func() time.Time { return m.StartedAt.Add(6 * time.Hour) }
			page, err := c.MeetingTranscript(context.Background(), m.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			app := &pausedAskApp{posted: make(chan func(), 16)}
			d := &desktop{client: c, app: app, page: "Ask", askQuestion: "What did we discuss in the meeting about launch?"}
			if tc.followup {
				initial, err := c.AskMemory(context.Background(), d.askQuestion)
				if err != nil {
					t.Fatal(err)
				}
				d.askConversation = appendQuickConversation(nil, d.askQuestion, initial)
				d.askQuestion = "Make it shorter"
			}
			previousTurns := len(d.askConversation)
			d.askMemory()
			queuedVoiceCallback(t, app)() // initial quick-panel resize
			publish := queuedVoiceCallback(t, app)
			if tc.erase {
				if err := s.DeleteObservationRecord(context.Background(), page.Records[0].SourceID); err != nil {
					t.Fatal(err)
				}
			}
			publish()
			if d.askLoading {
				t.Fatal("queued completion left Ask loading")
			}
			if tc.erase {
				if d.askResult != nil || len(d.askConversation) != previousTurns || d.status == "" {
					t.Fatalf("erased speech became a native answer: %+v %q", d.askResult, d.status)
				}
			} else {
				if d.askResult == nil || len(d.askResult.Sources) != 1 || d.askResult.Sources[0].SourceID != page.Records[0].SourceID {
					t.Fatalf("unchanged voice answer rejected own history write: %+v %q", d.askResult, d.status)
				}
				if history := c.RecentQuestions(context.Background(), 10); len(history) != previousTurns+1 || history[len(history)-1] != d.askQuestion {
					t.Fatalf("question history lost: %v", history)
				}
			}
		})
	}
}

type voiceArchiveReadProbe struct {
	*durable.Store
	afterExact         func()
	beforeHistory      func()
	exactCalls, failAt int
}

func (p *voiceArchiveReadProbe) SearchExact(ctx context.Context, q search.ExactQuery) (search.ExactPage, error) {
	p.exactCalls++
	if p.failAt > 0 && p.exactCalls == p.failAt {
		return search.ExactPage{}, context.DeadlineExceeded
	}
	page, err := p.Store.SearchExact(ctx, q)
	if p.afterExact != nil {
		fn := p.afterExact
		p.afterExact = nil
		fn()
	}
	return page, err
}

func (p *voiceArchiveReadProbe) AppendQuestionAtRead(ctx context.Context, q storage.QuestionRecord, epoch uint64) (uint64, bool, error) {
	if p.beforeHistory != nil {
		p.beforeHistory()
	}
	return p.Store.AppendQuestionAtRead(ctx, q, epoch)
}

func (p *voiceArchiveReadProbe) AppendQuestionAtVoiceRead(ctx context.Context, q storage.QuestionRecord, epoch uint64) (uint64, bool, error) {
	if p.beforeHistory != nil {
		p.beforeHistory()
	}
	return p.Store.AppendQuestionAtVoiceRead(ctx, q, epoch)
}

func TestVoiceArchiveSplitReadNeverPublishesChangedOrFailedPrefix(t *testing.T) {
	for _, mutation := range []string{"erase-after-first-window", "mandatory-later-failure"} {
		t.Run(mutation, func(t *testing.T) {
			base, s, ids, _ := voiceArchiveFixture(t)
			probe := &voiceArchiveReadProbe{Store: s}
			if mutation == "erase-after-first-window" {
				probe.afterExact = func() {
					if err := s.DeleteObservationRecord(context.Background(), ids["late"]); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				probe.failAt = 2
			}
			c := NewClient(probe)
			c.askNow = base.askNow
			t.Cleanup(c.Close)
			plan, err := assistant.PlanQuestion("What did we discuss in the meeting about Orchid in 2024?", c.AskTime(), nil)
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.RetrievePlan(context.Background(), plan)
			want := error(ErrMemoryChanging)
			if mutation == "mandatory-later-failure" {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) || len(result.Evidence) != 0 {
				t.Fatalf("partial/changed voice evidence escaped: %+v %v", result, err)
			}
		})
	}
}

func TestVoiceArchiveOwnHistoryCannotRefreshErasedSpeech(t *testing.T) {
	base, s, ids, _ := voiceArchiveFixture(t)
	called := false
	probe := &voiceArchiveReadProbe{Store: s, beforeHistory: func() {
		called = true
		if err := s.DeleteObservationRecord(context.Background(), ids["older"]); err != nil {
			t.Fatal(err)
		}
	}}
	c := NewClient(probe)
	c.askNow = base.askNow
	t.Cleanup(c.Close)
	answer, err := c.AskMemory(context.Background(), "What did I say in my voice note about Cobalt?")
	if !called || !errors.Is(err, ErrMemoryChanging) || answer.Answer.Text != "" || len(answer.Sources) != 0 {
		t.Fatalf("history adopted erased evidence: called=%v answer=%+v err=%v", called, answer, err)
	}
}

func TestVoiceArchiveQueuedPageSummaryRejectsErasure(t *testing.T) {
	for _, erase := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "erased"}[erase], func(t *testing.T) {
			c, s, _, m := summaryFixture(t, 1)
			page, err := c.MeetingTranscript(context.Background(), m.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			app := &pausedAskApp{posted: make(chan func(), 16)}
			d := &desktop{client: c, app: app, page: "Ask"}
			d.meetingUI.selected = m.ID
			d.meetingUI.transcript = page
			d.summarizeMeetingPage()
			publish := queuedVoiceCallback(t, app)
			if erase {
				if err := s.DeleteObservationRecord(context.Background(), page.Records[0].SourceID); err != nil {
					t.Fatal(err)
				}
			}
			publish()
			if d.meetingUI.summaryLoading {
				t.Fatal("summary stayed loading")
			}
			if erase {
				if d.meetingUI.summary != nil || d.meetingUI.status == "" {
					t.Fatalf("erased speech survived in queued summary: %+v %q", d.meetingUI.summary, d.meetingUI.status)
				}
			} else if d.meetingUI.summary == nil || len(d.meetingUI.summary.Sources) != 1 {
				t.Fatal("unchanged summary not installed")
			}
		})
	}
}

func TestVoiceArchiveQueuedFullRecapRejectsErasure(t *testing.T) {
	for _, erase := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "erased"}[erase], func(t *testing.T) {
			c, s, _, m := summaryFixture(t, 1)
			page, err := c.MeetingTranscript(context.Background(), m.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			app := &pausedAskApp{posted: make(chan func(), 16)}
			d := &desktop{client: c, app: app, page: "Ask"}
			d.meetingUI.selected = m.ID
			d.meetingUI.selectedTitle = m.Title
			d.meetingUI.selectedAt = m.StartedAt
			d.meetingUI.selectedRecordCount = 1
			d.memoryUI.meetingDetail = true
			d.recapSelectedVoiceNote()
			publish := queuedVoiceCallback(t, app)
			if erase {
				if err := s.DeleteObservationRecord(context.Background(), page.Records[0].SourceID); err != nil {
					t.Fatal(err)
				}
			}
			publish()
			u := d.memoryUI.recap
			if erase {
				if len(u.parts) != 0 || u.complete || u.loading || u.status == "" {
					t.Fatalf("erased speech survived in queued full recap: %+v", u)
				}
			} else if len(u.parts) != 1 || !u.complete || u.loading {
				t.Fatalf("unchanged recap failed: %+v", u)
			}
		})
	}
}

func TestVoiceArchiveCancelledOriginalReadReturnsNoPage(t *testing.T) {
	c, _, _, m := summaryFixture(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	page, err := c.MeetingTranscript(ctx, m.ID, 0)
	if !errors.Is(err, context.Canceled) || len(page.Records) != 0 {
		t.Fatalf("cancelled original read escaped: %+v %v", page, err)
	}
}

func TestVoiceArchivePublicationDoesNotWaitForClientWriter(t *testing.T) {
	for _, operation := range []string{"begin", "publish"} {
		t.Run(operation, func(t *testing.T) {
			c, _, _, _ := summaryFixture(t, 1)
			read, stable := c.beginVoicePublication()
			if !stable {
				t.Fatal("fixture read unstable")
			}
			c.memoryChangeMu.Lock()
			unlock := sync.OnceFunc(c.memoryChangeMu.Unlock)
			defer unlock()
			done := make(chan bool, 1)
			go func() {
				if operation == "begin" {
					_, stable := c.beginVoicePublication()
					done <- stable
				} else {
					done <- read.publish(c, func() {})
				}
			}()
			select {
			case accepted := <-done:
				if accepted {
					t.Error("client writer admitted publication")
				}
			case <-time.After(time.Second):
				unlock()
				<-done
				t.Fatal("native voice publication waited for a client writer")
			}
		})
	}
}

func TestReviewQueuedSourceLookupRespectsNewSelection(t *testing.T) {
	c, s, _, m := summaryFixture(t, 1)
	page, err := c.MeetingTranscript(context.Background(), m.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := appendArchiveVoice(t, s, "review-other", "Other saved recording", m.StartedAt, []string{"Other saved words."})
	app := &pausedAskApp{posted: make(chan func(), 16)}
	d := &desktop{client: c, app: app, page: "Ask"}
	d.readMemoryVoiceSource(page.Records[0].SourceID)
	publish := queuedVoiceCallback(t, app)
	d.selectMemoryMeeting(other)
	publish()
	if d.meetingUI.selected != other.ID {
		t.Fatalf("queued source lookup replaced newer selection: want=%s got=%s", other.ID, d.meetingUI.selected)
	}
}
func TestReviewRecapErrorAfterEraseRetiresEarlierParts(t *testing.T) {
	c, s, _, m := summaryFixture(t, 1)
	page, err := c.MeetingTranscript(context.Background(), m.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	app := &pausedAskApp{posted: make(chan func(), 16)}
	d := &desktop{client: c, app: app, page: "Ask"}
	d.meetingUI.transcript = page
	d.memoryUI.recap = voiceRecapViewState{id: m.ID, request: 1, loading: true, parts: []voiceRecapPart{{first: 1, last: 1}}}
	read, stable := c.beginVoicePublication()
	if !stable {
		t.Fatal("unstable")
	}
	if err := s.DeleteObservationRecord(context.Background(), page.Records[0].SourceID); err != nil {
		t.Fatal(err)
	}
	d.finishVoiceRecap(m.ID, read, 1, 0, assistant.ErrAnswer)
	queuedVoiceCallback(t, app)()
	if len(d.memoryUI.recap.parts) != 0 || len(d.meetingUI.transcript.Records) != 0 {
		t.Fatalf("terminal model error retained erased earlier recap: parts=%d originals=%d status=%q", len(d.memoryUI.recap.parts), len(d.meetingUI.transcript.Records), d.memoryUI.recap.status)
	}
}
func TestReviewContextualFind(t *testing.T) {
	c, _, _, _ := singleSearchFixture(t)
	for _, input := range []string{"Find that note again", "Find the same recording", "Find it again"} {
		got, err := c.resolveMemoryEntry(context.Background(), input, EntryState{HasAnswer: true}, "UTC")
		if err != nil {
			t.Fatal(err)
		}
		if got.SearchQuery != "" {
			t.Errorf("contextual request lost answer context: %q -> %q", input, got.SearchQuery)
		}
	}
}

func TestReviewForgedMeetingAppMustNotSelectVoiceOnlyFence(t *testing.T) {
	c, s, _, m := summaryFixture(t, 1)
	c.localAI = nil
	c.askNow = func() time.Time { return m.StartedAt.Add(time.Hour) }
	o, err := capture.SanitizeObservation("sh.blur.meetings", "forged.meeting.local", "Quasar containment requires a violet checklist.", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(context.Background(), o, m.StartedAt); err != nil {
		t.Fatal(err)
	}
	got, err := c.AskMemory(context.Background(), "Find Quasar")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 1 || got.Sources[0].SourceID != o.ContentID() || got.Publication == nil {
		t.Fatalf("fixture did not select synthetic captured source: %+v", got)
	}
	if err := s.DeleteObservationRecord(context.Background(), o.ContentID()); err != nil {
		t.Fatal(err)
	}
	if got.Publication.Publish(func() {}) {
		t.Fatal("erased unauthenticated meeting-app capture still authorizes queued answer")
	}
}

func TestReviewSummaryNativeAdmissionDoesNotWaitForClientWriter(t *testing.T) {
	c, _, _, m := summaryFixture(t, 1)
	page, err := c.MeetingTranscript(context.Background(), m.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	d := &desktop{client: c, app: &pausedAskApp{posted: make(chan func(), 16)}, page: "Ask"}
	d.meetingUI.selected = m.ID
	d.meetingUI.transcript = page
	c.memoryChangeMu.Lock()
	unlock := sync.OnceFunc(c.memoryChangeMu.Unlock)
	defer unlock()
	done := make(chan struct{})
	go func() { d.summarizeMeetingPage(); close(done) }()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		unlock()
		<-done
		t.Fatal("native page-summary action waited for Client memory writer")
	}
}

func TestReviewManualProvenanceSurvivesFollowup(t *testing.T) {
	c, s, _, m := summaryFixture(t, 1)
	c.localAI = nil
	c.askNow = func() time.Time { return m.StartedAt.Add(time.Hour) }
	record := meetings.Record{Key: m.ID + "/note/review-manual", Kind: meetings.UserNote, SessionID: m.ID, Note: meetings.ManualNoteInput{ID: "review-manual", At: time.Second, Text: "Juniper delivery is Thursday."}}
	receipt, err := s.Append(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	question := "What was said in the meeting about Juniper?"
	first, err := c.AskMemory(context.Background(), question)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Sources) != 1 || first.Sources[0].SourceID != receipt.SourceID || !strings.Contains(first.Sources[0].AppName, "manual note") {
		t.Fatalf("fixture lost manual source: %+v", first)
	}
	next, err := c.AskMemoryWithHistory(context.Background(), "Who said that?", []assistant.Turn{{Question: question, Answer: first.Answer.Text, SourceIDs: first.Answer.SourceIDs, Plan: first.Plan}})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Sources) != 1 {
		t.Fatalf("followup had no retained source: %+v", next)
	}
	for _, source := range next.Sources {
		if source.SourceID == receipt.SourceID && !strings.Contains(source.AppName, "manual note") {
			t.Fatalf("manual-note provenance lost in speaker follow-up: %+v", source)
		}
	}
}
