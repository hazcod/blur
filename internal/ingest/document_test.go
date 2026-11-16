package ingest

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage/durable"
)

type documentFixtureCapturer struct {
	raw    string
	legacy int
	opened []*documentFixtureJob
}

func (c *documentFixtureCapturer) Capture(context.Context) (capture.SanitizedObservation, error) {
	c.legacy++
	return capture.SanitizedObservation{}, ErrCaptureFailed
}
func (c *documentFixtureCapturer) OpenDocument(_ context.Context, at time.Time) (capture.DocumentJob, error) {
	builder, err := capture.NewCaptureBuilder("org.example.editor", "", strings.Repeat("b", 64), at)
	if err != nil {
		return nil, err
	}
	job := &documentFixtureJob{builder: builder, raw: c.raw}
	c.opened = append(c.opened, job)
	return job, nil
}

type documentFixtureJob struct {
	builder *capture.CaptureBuilder
	raw     string
	offset  int
	closed  bool
}

func (j *documentFixtureJob) Header() capture.CaptureHeader { return j.builder.Header() }
func (j *documentFixtureJob) Close()                        { j.closed = true; j.builder.Abort() }
func (j *documentFixtureJob) Read(ctx context.Context, sink func(context.Context, []capture.CapturePart) error) (capture.DocumentProgress, error) {
	end := min(j.offset+(64<<10), len(j.raw))
	for end < len(j.raw) && !utf8.RuneStart(j.raw[end]) {
		end--
	}
	if err := j.builder.Write(ctx, j.raw[j.offset:end], sink); err != nil {
		return capture.DocumentProgress{}, err
	}
	j.offset = end
	p := capture.DocumentProgress{Bytes: 64 << 10}
	if end == len(j.raw) {
		summary, err := j.builder.Finish(ctx, capture.CaptureComplete, sink)
		p.Done = true
		p.Summary = summary
		return p, err
	}
	return p, nil
}

type documentFixtureSink struct {
	*durable.Store
	receipts           []durable.CapturePublicationReceipt
	appends, publishes int
}

func (s *documentFixtureSink) AppendCaptureParts(ctx context.Context, h capture.CaptureHeader, p []capture.CapturePart) error {
	s.appends++
	return s.Store.AppendCaptureParts(ctx, h, p)
}
func (s *documentFixtureSink) PublishDocumentCapture(ctx context.Context, summary capture.CaptureSummary) (DocumentPublication, error) {
	s.publishes++
	receipt, err := s.PublishCaptureWithSourceReceipt(ctx, summary)
	s.receipts = append(s.receipts, receipt)
	return DocumentPublication{Committed: receipt.Committed, New: receipt.New, SourceID: receipt.SourceID}, err
}
func TestDocumentIngestionPublishesCompleteLargeSourceBeforeIndexAndKeepsDuplicateIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := openTestStore(t)
	source := &eventSource{probe: platform.CaptureProbe{TargetID: "fixture", AppID: "org.example.editor"}, events: make(chan platform.CaptureEvent, 1)}
	raw := strings.Repeat("A complete technical document with Unicode 日本語.\n", 26000) + "\nTAIL_UNIQUE_RECALL_MARKER password: syntheticprivatefixture"
	capturer := &documentFixtureCapturer{raw: raw}
	sink := &documentFixtureSink{Store: store}
	service, err := newService(source, capturer, sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.SetPeriodicRefresh(time.Second)
	count := 0
	service.SetDocumentNotify(func(summary capture.CaptureSummary, isNew bool) {
		count++
		if summary.Bytes() <= capture.MaxObservationTextBytes || summary.Coverage() != capture.CaptureComplete {
			t.Error("source published incomplete")
		}
		if isNew != (count == 1) {
			t.Error("duplicate publication changed identity")
		}
		if count == 2 {
			cancel()
		}
	})
	if err := service.Run(ctx); err != context.Canceled {
		t.Fatal(err)
	}
	if capturer.legacy != 0 || count != 2 || sink.publishes != 2 || sink.appends < 10 {
		t.Fatalf("wrong runtime path: legacy=%d count=%d appends=%d", capturer.legacy, count, sink.appends)
	}
	if service.Stats().Persisted != 1 || service.Stats().Duplicates != 1 {
		t.Fatal(service.Stats())
	}
	if sink.receipts[0].SourceID != sink.receipts[1].SourceID {
		t.Fatal("duplicate activity identity changed")
	}
	for _, job := range capturer.opened {
		if !job.closed {
			t.Fatal("native lease not closed")
		}
	}
	summary := sink.receipts[0].Edition.Summary
	var text strings.Builder
	for after := int64(-1); ; {
		page, err := store.ReadCaptureParts(context.Background(), summary.ContentID(), after, 8)
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range page.Parts {
			text.WriteString(part.Text())
		}
		if !page.HasMore {
			break
		}
		after = page.Next
	}
	if text.Len() <= capture.MaxObservationTextBytes || !strings.Contains(text.String(), "TAIL_UNIQUE_RECALL_MARKER") || strings.Contains(text.String(), "syntheticprivatefixture") {
		t.Fatal("full redacted tail lost")
	}
	for i := 0; i < 1000; i++ {
		pending, err := store.IndexPendingCaptures(context.Background(), 16)
		if err != nil {
			t.Fatal(err)
		}
		if !pending {
			break
		}
		if i == 999 {
			t.Fatal("index did not drain")
		}
	}
	page, err := store.SearchExact(context.Background(), search.ExactQuery{Terms: []string{"marker", "recall", "tail", "unique"}, MaxRows: search.MaxIndexRows, MaxBytes: search.MaxIndexBytes})
	if err != nil || len(page.Hits) != 1 {
		t.Fatalf("whole source absent from public search: %+v %v", page, err)
	}
	evidence, found, err := store.GetSourceEvidence(context.Background(), page.Hits[0].SourceID)
	if err != nil || !found || !strings.Contains(evidence.Text(), "TAIL_UNIQUE_RECALL_MARKER") {
		t.Fatal("public evidence did not resolve full tail", err)
	}
}

type documentFallbackCapturer struct {
	observation      capture.SanitizedObservation
	openErr, readErr error
	legacy           int
	job              *documentEmptyJob
}
type documentEmptyJob struct {
	builder *capture.CaptureBuilder
	err     error
	closed  bool
}

func (j *documentEmptyJob) Header() capture.CaptureHeader { return j.builder.Header() }
func (j *documentEmptyJob) Read(context.Context, func(context.Context, []capture.CapturePart) error) (capture.DocumentProgress, error) {
	return capture.DocumentProgress{}, j.err
}
func (j *documentEmptyJob) Close() { j.closed = true; j.builder.Abort() }
func (c *documentFallbackCapturer) Capture(context.Context) (capture.SanitizedObservation, error) {
	c.legacy++
	return c.observation, nil
}
func (c *documentFallbackCapturer) OpenDocument(_ context.Context, at time.Time) (capture.DocumentJob, error) {
	if c.openErr != nil {
		return nil, c.openErr
	}
	builder, err := capture.NewCaptureBuilder("org.example.editor", "", strings.Repeat("d", 64), at)
	if err != nil {
		return nil, err
	}
	c.job = &documentEmptyJob{builder: builder, err: c.readErr}
	return c.job, nil
}
func TestDocumentIngestionFreshFallbackPreservesVisualRouteAndBlocksPolicyFailure(t *testing.T) {
	for _, tc := range []struct {
		name             string
		openErr, readErr error
		legacy           int
		state            HealthState
	}{
		{name: "unsupported", openErr: capture.ErrDocumentUnsupported, legacy: 1, state: HealthSaved},
		{name: "empty", readErr: &capture.CaptureError{Code: capture.NoContent}, legacy: 1, state: HealthSaved},
		{name: "unreadable", readErr: &capture.CaptureError{Code: capture.ReadFailed}, legacy: 1, state: HealthSaved},
		{name: "excluded", readErr: &capture.CaptureError{Code: capture.Excluded}, legacy: 0, state: HealthExcluded},
		{name: "permission", readErr: &capture.CaptureError{Code: capture.PermissionRequired}, legacy: 0, state: HealthPermission},
		{name: "timeout", readErr: &capture.CaptureError{Code: capture.TimedOut}, legacy: 0, state: HealthTimedOut},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openTestStore(t)
			observation, err := capture.SanitizeObservation("org.example.editor", "", "Visible OCR fallback evidence", 4096)
			if err != nil {
				t.Fatal(err)
			}
			capturer := &documentFallbackCapturer{observation: observation, openErr: tc.openErr, readErr: tc.readErr}
			service, err := newService(&eventSource{events: make(chan platform.CaptureEvent)}, capturer, &documentFixtureSink{Store: store}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := service.captureCurrent(context.Background()); err != nil {
				t.Fatal(err)
			}
			if capturer.legacy != tc.legacy || service.health.State != tc.state {
				t.Fatal("wrong fallback", capturer.legacy, service.health.State)
			}
			if capturer.job != nil && !capturer.job.closed {
				t.Fatal("failed job retained native lease")
			}
		})
	}
}
