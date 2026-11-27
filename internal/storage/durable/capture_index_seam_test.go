package durable

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// The expected word and source bytes are authored independently of the index.
// Moving a transport boundary into a word must not change literal recall.
func TestCaptureExactSearchPreservesWordsAcrossOriginalPartBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, word, query string
		left              int
	}{
		{"ascii", "seamquartzproof", "seamquartzproof", 8},
		{"unicode", "日本語résumé", "日本語resume", 6},
		{"combining", "cafe\u0301proof", "cafeproof", 4},
		{"coarse-unicode-after-starter", "Wᆨ́ᆨ", "wᆨ ᆨ", 1},
		{"coarse-unicode-after-jamo", "Wᆨ́ᆨ", "wᆨ ᆨ", 4},
		{"coarse-unicode-after-accent", "Wᆨ́ᆨ", "wᆨ ᆨ", 6},
		{"overlapping-unicode-spans", "prefixWᆨ́ᆨ", "prefixwᆨ", 7},
		{"possessive", "Dima's", "dima", 4},
		{"identifier-delimiter", "agent_build_01", "01 agent build", 6},
		{"identifier-hyphen", "agent-build-01", "01 agent build", 6},
		{"identifier-dot", "agent.build.01", "01 agent build", 6},
		{"identifier-path", "agent/build/01", "01 agent build", 6},
		{"three-parts", strings.Repeat("q", 2*capture.MaxCapturePartTextBytes+19), strings.Repeat("q", 2*capture.MaxCapturePartTextBytes+19), 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := openReplicaTestStore(t)
			prefix := strings.Repeat(" ", capture.MaxCapturePartTextBytes-tc.left)
			input := prefix + tc.word + " ordinary ending."
			h, parts, summary := makeStoredCapture(t, input, time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC))
			if len(parts) < 2 {
				t.Fatal("fixture did not split the word")
			}
			stageStoredCapture(t, s, h, parts)
			if _, err := s.PublishCapture(ctx, summary); err != nil {
				t.Fatal(err)
			}
			drainCaptureIndex(t, s)
			query := scopedQuery(tc.query)
			query.Terms = strings.Fields(tc.query)
			page, err := s.SearchExact(ctx, query)
			if err != nil || len(page.Hits) != 1 || page.IndexingPending {
				t.Fatalf("complete cross-part word not found: hits=%d pending=%t err=%v", len(page.Hits), page.IndexingPending, err)
			}
			e, found, err := s.GetSourceEvidence(ctx, page.Hits[0].SourceID)
			if err != nil || !found {
				t.Fatal("cross-part hit has no inspectable original evidence", found, err)
			}
			p, multipart := e.Provenance()
			if !multipart || p.Start < 0 || p.End > int64(len(input)) || e.Text() != input[p.Start:p.End] || !strings.Contains(e.Text(), strings.TrimSuffix(tc.word, "'s")) {
				t.Fatal("citation does not preserve its exact original span", p)
			}
			if _, found, err := s.ResolveCaptureSource(ctx, e.SourceID()); err != nil || !found {
				t.Fatal("range citation lost deletion identity", found, err)
			}
			if err := s.DeleteObservationRecord(ctx, e.SourceID()); err != nil {
				t.Fatal("range deletion failed", err)
			}
			if page, err := s.SearchExact(ctx, query); err != nil || len(page.Hits) != 0 {
				t.Fatal("deleted range remained searchable", page, err)
			}
		})
	}
}

func TestCaptureRangeIndexResumesUnfinishedWordAndDeletesInterruptedRange(t *testing.T) {
	for _, deletePartial := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume-word", true: "delete-range"}[deletePartial], func(t *testing.T) {
			ctx := context.Background()
			path, key := durableTestPath(t), durableTestKey(t, 0x97)
			s, err := OpenDurable(path, key, CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			word := strings.Repeat("q", 2*capture.MaxCapturePartTextBytes+19)
			summary := publishIndexedFixture(t, s, word+" finished calibration.", time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC))
			var rangeID string
			reached := false
			for round := 0; round < 100 && !reached; round++ {
				if _, err := s.IndexPendingCaptures(ctx, 1); err != nil {
					t.Fatal(err)
				}
				if err := s.view(ctx, func(reader bolt.Reader) error {
					r, _, _, err := s.captureEditionReader(reader, summary.ContentID())
					if err != nil {
						return err
					}
					if deletePartial {
						if r.IndexRangeWork != nil && r.IndexRangeWork.Stage == 2 {
							w := r.IndexRangeWork
							rangeID = capture.CaptureRangeSourceID(summary.ContentID(), r.Publication, w.Start, w.End)
							reached = true
						}
					} else {
						reached = r.IndexPart > 0 && r.IndexStream != nil && r.IndexStream.Word
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if !reached {
				t.Fatal("fixture did not reach the intended durable continuation")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenDurable(path, key, OpenExisting)
			if err != nil {
				t.Fatal(err)
			}
			if deletePartial {
				if err := s.DeleteObservationRecord(ctx, rangeID); err != nil {
					t.Fatal("interrupted range could not be erased", err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = OpenDurable(path, key, OpenExisting)
				if err != nil {
					t.Fatal(err)
				}
				if _, found, err := s.GetCapture(ctx, summary.ContentID()); err != nil || found {
					t.Fatal("interrupted range deletion restored original", found, err)
				}
				if _, found, err := s.GetSourceEvidence(ctx, rangeID); err != nil || found {
					t.Fatal("interrupted range evidence survived", found, err)
				}
				if pending, err := s.CaptureIndexPending(ctx); err != nil || pending {
					t.Fatal("erased range retained resumable work", pending, err)
				}
			} else {
				drainCaptureIndex(t, s)
				page, err := s.SearchExact(ctx, scopedQuery(word))
				if err != nil || len(page.Hits) != 1 || page.IndexingPending {
					t.Fatal("unfinished word lost its exact continuation", page, err)
				}
			}
		})
	}
}

func TestCaptureExactSearchReportsOversizeCrossPartReadBudget(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	word := strings.Repeat("z", search.MaxIndexBytes+17)
	publishIndexedFixture(t, s, word, time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC))
	drainCaptureIndex(t, s)
	_, err := s.SearchExact(ctx, scopedQuery(word))
	if !errors.Is(err, search.ErrIndexBudget) {
		t.Fatal("oversize original range must report a budget limit, not false absence", err)
	}
}
