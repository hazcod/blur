package durable

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
)

func sourceWindowForTest(t *testing.T, s *Store, request storage.ObservationWindowRequest) storage.SourceEvidenceWindow {
	t.Helper()
	r, ok := any(s).(storage.SourceEvidenceWindowReader)
	if !ok {
		t.Fatal("published capture parts have no bounded time-window reader")
	}
	page, err := r.ListSourceEvidenceWindow(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func TestCaptureWindowFiltersRealSameHourVisitsAndStableMixedContinuation(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	at := time.Date(2026, 11, 13, 10, 10, 0, 0, time.UTC)
	summary := publishIndexedFixture(t, s, "First complete document source.", at)
	drainCaptureIndex(t, s)
	publishIndexedFixture(t, s, "First complete document source.", at.Add(20*time.Minute))
	drainCaptureIndex(t, s)
	legacy, _ := capture.SanitizeObservation("legacy.app", "other.test", "Legacy same time source", 512)
	if err := s.PutObservationAt(ctx, legacy, at); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ start, end, want time.Time }{
		{at.Add(-time.Minute), at.Add(time.Minute), at},
		{at.Add(time.Minute), at.Add(19 * time.Minute), time.Time{}},
		{at.Add(19 * time.Minute), at.Add(21 * time.Minute), at.Add(20 * time.Minute)},
	} {
		p := sourceWindowForTest(t, s, storage.ObservationWindowRequest{Start: tc.start, End: tc.end, AppID: "com.example.reader", Limit: 8, MaxBytes: storage.MaxObservationPageBytes})
		if tc.want.IsZero() {
			if len(p.Sources) != 0 {
				t.Fatal("invented intermediate visit")
			}
		} else if len(p.Sources) != 1 || !p.CapturedAt[0].Equal(tc.want) {
			t.Fatal("real visit lost or replaced by latest clock", p.CapturedAt, tc.want)
		}
	}
	for _, request := range []storage.ObservationWindowRequest{
		{AppID: "wrong.app"}, {Domain: "wrong.test"}, {ExcludeAppIDs: []string{"com.example.reader", "legacy.app"}},
	} {
		request.Start, request.End, request.Limit, request.MaxBytes = at.Add(-time.Minute), at.Add(time.Minute), 8, storage.MaxObservationPageBytes
		if p := sourceWindowForTest(t, s, request); len(p.Sources) != 0 {
			t.Fatal("window escaped requested scope")
		}
	}
	request := storage.ObservationWindowRequest{Start: at.Add(-time.Minute), End: at.Add(time.Minute), Limit: 1, MaxBytes: storage.MaxObservationPageBytes}
	seen := map[string]bool{}
	for rounds := 0; rounds < 3; rounds++ {
		p := sourceWindowForTest(t, s, request)
		for _, e := range p.Sources {
			if seen[e.SourceID()] {
				t.Fatal("mixed continuation repeated source")
			}
			seen[e.SourceID()] = true
		}
		if !p.HasMore {
			break
		}
		request.BeforeCapturedAt, request.BeforeSourceID = p.NextCapturedAt, p.NextSourceID
	}
	if len(seen) != 2 {
		t.Fatal("mixed cursor omitted legacy or generic source", len(seen))
	}
	ids, _, err := s.ListCaptureSourceIDs(ctx, summary.ContentID(), -1, 1)
	if err != nil || len(ids) != 1 {
		t.Fatal(err)
	}
	if err := s.DeleteObservationRecord(ctx, summary.ContentID()); err != nil {
		t.Fatal(err)
	}
	_, err = s.ListSourceEvidenceWindow(ctx, storage.ObservationWindowRequest{Start: at.Add(-time.Minute), End: at.Add(time.Minute), BeforeCapturedAt: at, BeforeSourceID: ids[0], Limit: 1, MaxBytes: storage.MaxObservationPageBytes})
	if !errors.Is(err, ErrCaptureCursorChanged) {
		t.Fatal("deleted cursor silently changed ordering", err)
	}
}

func TestCaptureTimelineBackfillsAlreadyIndexedStoreWithoutReindexingText(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xea)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 13, 10, 10, 0, 0, time.UTC)
	summary := publishIndexedFixture(t, s, "Previously indexed complete source", at)
	drainCaptureIndex(t, s)
	before := partIndexCiphertext(t, s)
	if err := s.captureUpdate(ctx, func(tx *Tx) error {
		for _, ref := range []struct{ ns, id string }{{captureNS, captureTimelineMarkerID}, {captureTimelineNS, observationTimelineHourID(at)}, {captureTimelineNS, observationTimelinePageID(observationTimelineHourID(at), 0)}, {captureTimelineRefNS, summary.ContentID() + ":" + observationTimelineHourID(at)}} {
			if err := tx.deleteIndexValueLocked(ref.ns, ref.id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	request := storage.ObservationWindowRequest{Start: at.Truncate(time.Hour), End: at.Truncate(time.Hour).Add(time.Hour), Limit: 8, MaxBytes: storage.MaxObservationPageBytes}
	if p := sourceWindowForTest(t, s, request); !p.Pending || len(p.Sources) != 0 {
		t.Fatal("missing time directory claimed complete coverage")
	}
	drainCaptureIndex(t, s)
	if p := sourceWindowForTest(t, s, request); p.Pending || len(p.Sources) != 1 || !p.CapturedAt[0].Equal(at) {
		t.Fatal("bounded metadata replay did not recover old indexed source", p.Pending, len(p.Sources))
	}
	if !equalCiphertext(before, partIndexCiphertext(t, s)) {
		t.Fatal("timeline migration rewrote immutable text postings")
	}
}

func TestCaptureWindowPendingAndHourLimitDescribeActualCoverage(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	at := time.Date(2026, 11, 13, 10, 10, 0, 0, time.UTC)
	_ = publishIndexedFixture(t, s, "Pending complete document source", at)
	request := storage.ObservationWindowRequest{Start: at.Truncate(time.Hour), End: at.Truncate(time.Hour).Add(time.Hour), Limit: 64, MaxBytes: storage.MaxObservationPageBytes}
	if p := sourceWindowForTest(t, s, request); !p.Pending || !p.HasMore || len(p.Sources) != 0 {
		t.Fatal("unindexed capture was silently treated as empty ready window", p.Pending, p.HasMore, len(p.Sources))
	}
	drainCaptureIndex(t, s)
	for i := 0; i < 63; i++ {
		o, _ := capture.SanitizeObservation("legacy.app", "", fmt.Sprintf("Unique evidence %d", i), 512)
		if err := s.PutObservationAt(ctx, o, at.Add(time.Duration(i+1)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	hour, err := s.CaptureHourEvidence(ctx, request.Start)
	if err != nil || len(hour.Evidence) != 64 || hour.SourceLimitReached {
		t.Fatal("exactly64 complete sources guessed an overflow", len(hour.Evidence), hour.SourceLimitReached, err)
	}
	o, _ := capture.SanitizeObservation("legacy.app", "", "The genuine sixty-fifth source", 512)
	if err := s.PutObservationAt(ctx, o, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	hour, err = s.CaptureHourEvidence(ctx, request.Start)
	if err != nil || len(hour.Evidence) != 64 || !hour.SourceLimitReached {
		t.Fatal("additional source coverage was omitted without flag", len(hour.Evidence), hour.SourceLimitReached, err)
	}
}

func TestCaptureWindowAndHourRecoverOriginalAndActualRevisitParts(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xe9)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 13, 10, 10, 0, 0, time.UTC)
	text := strings.Repeat("Original ordered evidence for compiler work. ", 2400) + "TAIL Mira owns rollback."
	summary := publishIndexedFixture(t, s, text, at)
	drainCaptureIndex(t, s)
	visit := at.Add(2*time.Hour + 15*time.Minute)
	publishIndexedFixture(t, s, text, visit)
	drainCaptureIndex(t, s)
	legacy, _ := capture.SanitizeObservation("reader.app", "", "Legacy terminal work", 512)
	if err := s.PutObservationAt(ctx, legacy, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	drainCaptureIndex(t, s)
	for _, observed := range []time.Time{at, visit} {
		request := storage.ObservationWindowRequest{Start: observed.Truncate(time.Hour), End: observed.Truncate(time.Hour).Add(time.Hour), Limit: 1, MaxBytes: storage.MaxObservationPageBytes}
		seen := map[string]bool{}
		var joined strings.Builder
		for rounds := 0; rounds < 16; rounds++ {
			page := sourceWindowForTest(t, s, request)
			if page.Pending || len(page.Sources) != len(page.CapturedAt) {
				t.Fatal("ready window reported incomplete metadata", page)
			}
			for i, e := range page.Sources {
				if seen[e.SourceID()] {
					t.Fatal("continuation repeated source")
				}
				seen[e.SourceID()] = true
				if e.SourceID() != legacy.ContentID() {
					if !page.CapturedAt[i].Equal(observed) {
						t.Fatal("part received invented/index time", page.CapturedAt[i], observed)
					}
					joined.WriteString(e.Text())
				}
			}
			if !page.HasMore {
				break
			}
			if len(page.Sources) == 0 || page.NextSourceID == request.BeforeSourceID {
				t.Fatal("time window did not advance")
			}
			request.BeforeCapturedAt, request.BeforeSourceID = page.NextCapturedAt, page.NextSourceID
		}
		if joined.String() != text {
			t.Fatal("full indexed document lost part order/tail", len(joined.String()), len(text), summary.PartCount())
		}
		hour, err := s.CaptureHourEvidence(ctx, observed.Truncate(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		want := int(summary.PartCount())
		if observed.Equal(at) {
			want++
		}
		if len(hour.Evidence) != want || hour.SourceLimitReached {
			t.Fatal("hour recovery omitted generic parts or guessed overflow", len(hour.Evidence), want, hour.SourceLimitReached)
		}
	}
	gap := sourceWindowForTest(t, s, storage.ObservationWindowRequest{Start: at.Add(time.Hour), End: at.Add(2 * time.Hour), Limit: 8, MaxBytes: storage.MaxObservationPageBytes})
	if len(gap.Sources) != 0 {
		t.Fatal("capture was invented between real visits")
	}
}
