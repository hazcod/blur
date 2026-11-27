package client

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestAskTechnicalSubjectSurvivesCandidateAndRejectedByteCrowding(t *testing.T) {
	for _, tc := range []struct {
		name          string
		bytes, decoys int
	}{
		{"native-part-sized-candidate-crowding", 32 << 10, 70},
		{"rejected-source-byte-crowding", 64 << 10, 34},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "memory.db")
			key := securestore.OfflineFixtureKey()
			store, err := durable.OpenDurable(path, key, durable.CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
			text := func(marker string) string {
				const line = "Neutral process output.\n"
				prefix := strings.Repeat(line, (tc.bytes-len(marker))/len(line))
				return prefix + strings.Repeat(" ", tc.bytes-len(marker)-len(prefix)) + marker
			}
			// Exact index ties use source IDs. Keep the authoritative match behind
			// the distractors deterministically, independently of encrypted keys.
			var target capture.SanitizedObservation
			for nonce := 0; ; nonce++ {
				target, err = capture.SanitizeObservation("com.apple.terminal", "", text(fmt.Sprintf("agent_build_01 completed zephyr calibration successfully. Target fixture %d.", nonce)), tc.bytes)
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(target.ContentID(), "f") {
					break
				}
			}
			if err := store.PutObservationAt(ctx, target, now.Add(-2*time.Hour)); err != nil {
				t.Fatal(err)
			}
			for i, nonce := 0, 0; i < tc.decoys; nonce++ {
				marker := fmt.Sprintf("agent_build_01_extra_%03d completed unrelated cleanup successfully.", nonce)
				o, err := capture.SanitizeObservation("com.apple.terminal", "", text(marker), tc.bytes)
				if err != nil {
					t.Fatal(err)
				}
				if o.ContentID() >= target.ContentID() {
					continue
				}
				if err := store.PutObservationAt(ctx, o, now.Add(-time.Duration(tc.decoys-i)*time.Minute)); err != nil {
					t.Fatal(err)
				}
				i++
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = durable.OpenDurable(path, key, durable.OpenExisting)
			if err != nil {
				t.Fatal(err)
			}
			c := NewClient(store)
			c.askNow = func() time.Time { return now }
			defer c.Close()
			terms, err := search.NormalizeTokens("agent_build_01", search.MaxTerms)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := store.SearchExact(ctx, search.ExactQuery{Terms: terms, TopK: search.MaxResults, MaxRows: search.MaxIndexRows, MaxBytes: search.MaxIndexBytes})
			if err != nil || len(raw.Hits) != tc.decoys+1 || raw.HasMore || raw.Hits[len(raw.Hits)-1].SourceID != target.ContentID() {
				t.Fatalf("fixture did not keep the exact match behind indexed distractors: hits=%d more=%t err=%v", len(raw.Hits), raw.HasMore, err)
			}
			if tc.bytes*tc.decoys <= MaxSearchBytes {
				t.Fatal("fixture does not exceed the aggregate source budget")
			}
			got, err := c.AskMemory(ctx, "What did agent_build_01 do in Terminal today?")
			if err != nil || got.Answer.InsufficientEvidence || len(got.Sources) != 1 || got.Sources[0].SourceID != target.ContentID() || !strings.Contains(got.Answer.Text, "zephyr calibration") {
				t.Fatalf("indexed exact identifier was crowded out: sources=%d insufficient=%t limited=%t err=%v", len(got.Sources), got.Answer.InsufficientEvidence, got.CoverageLimited, err)
			}
		})
	}
}

func TestTechnicalSubjectCandidatesPreserveScopeAndBoundedCoverage(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)
	var want []string
	for _, tc := range []struct {
		name, app, domain string
		at                time.Time
		live              bool
	}{
		{"early", "com.apple.terminal", "logs.example", start.Add(time.Minute), true},
		{"late", "com.apple.terminal", "logs.example", end.Add(-time.Minute), true},
		{"revisited", "com.apple.terminal", "logs.example", start.AddDate(-1, 0, 0), true},
		{"outside", "com.apple.terminal", "logs.example", end, false},
		{"wrong-app", "com.apple.mail", "logs.example", start.AddDate(0, 6, 0), false},
		{"wrong-domain", "com.apple.terminal", "other.example", start.AddDate(0, 6, 0), false},
		{"blur", appidentity.BundleID, "logs.example", start.AddDate(0, 6, 0), false},
	} {
		o, err := capture.SanitizeObservation(tc.app, tc.domain, "agent_build_01 completed scoped calibration: "+tc.name, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservationAt(ctx, o, tc.at); err != nil {
			t.Fatal(err)
		}
		if tc.live {
			want = append(want, o.ContentID())
		}
		if tc.name == "revisited" {
			at := start.AddDate(0, 6, 0)
			if err := store.PutActivitySegment(ctx, activity.Segment{ID: "technical-old-source-visit", AppID: tc.app, Domain: tc.domain, WallStart: at, WallEnd: at.Add(time.Minute), Duration: int64(time.Minute), SourceIDs: []string{o.ContentID()}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	probe := &longSearchProbe{Store: store}
	c := NewClient(probe)
	defer c.Close()
	plan := assistant.Plan{Kind: assistant.KindLookup, TechnicalSubjects: []string{"agent_build_01"}, Terms: []string{"unrelated"}, AppIDs: []string{"com.apple.terminal"}, Domains: []string{"logs.example"}, Scope: assistant.Scope{Start: start, End: end}}
	page, err := c.technicalSubjectPage(ctx, plan, true, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, hit := range page.Hits {
		got = append(got, hit.SourceID)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) || page.HasMore || page.CoverageLimited {
		t.Fatalf("technical subject changed requested scope: got=%v want=%v more=%t limited=%t", got, want, page.HasMore, page.CoverageLimited)
	}
	for _, query := range probe.queries {
		if query.TopK != search.MaxResults || query.MaxRows != search.MaxIndexRows || query.MinTermMatches != 0 || query.PartialOnly || query.CapturedEnd.Sub(query.CapturedStart) > searchWindowSpan || !slices.Equal(query.ExcludeAppIDs, []string{appidentity.BundleID}) {
			t.Fatalf("technical subject widened or weakened physical query: %+v", query)
		}
	}
	page, err = c.technicalSubjectPage(ctx, plan, true, nil, 2)
	if err != nil || len(page.Hits) != 2 || !page.HasMore {
		t.Fatalf("technical result truncation lost coverage: hits=%d more=%t err=%v", len(page.Hits), page.HasMore, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	probe.afterQuery = cancel
	page, err = c.technicalSubjectPage(canceled, plan, true, nil, 8)
	if !errors.Is(err, context.Canceled) || len(page.Hits) != 0 {
		t.Fatalf("cancelled candidate query published evidence: hits=%d err=%v", len(page.Hits), err)
	}
}

func TestTechnicalSubjectCandidatesValidateEachScopePageBeforeMergedPrefix(t *testing.T) {
	for _, split := range []string{"filters", "time-windows"} {
		t.Run(split, func(t *testing.T) {
			ctx := context.Background()
			store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			var target capture.SanitizedObservation
			for nonce := 0; ; nonce++ {
				target, err = capture.SanitizeObservation("com.apple.terminal", "wanted.example", fmt.Sprintf("agent_build_01 completed zephyr calibration successfully. Target fixture %d.", nonce), 1024)
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(target.ContentID(), "f") {
					break
				}
			}
			if err := store.PutObservationAt(ctx, target, time.Date(2024, 1, 2, 10, 0, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
			domain := "distractors.example"
			if split == "time-windows" {
				domain = "wanted.example"
			}
			for i, nonce := 0, 0; i < search.MaxResults; nonce++ {
				o, err := capture.SanitizeObservation("com.apple.terminal", domain, fmt.Sprintf("agent_build_01_extra_%03d completed unrelated cleanup.", nonce), 1024)
				if err != nil {
					t.Fatal(err)
				}
				if o.ContentID() >= target.ContentID() {
					continue
				}
				if err := store.PutObservationAt(ctx, o, time.Date(2025, 11, 22, 11, 0, 0, 0, time.UTC)); err != nil {
					t.Fatal(err)
				}
				i++
			}
			probe := &longSearchProbe{Store: store}
			c := NewClient(probe)
			defer c.Close()
			plan := assistant.Plan{TechnicalSubjects: []string{"agent_build_01"}, AppIDs: []string{"com.apple.terminal"}, Domains: []string{"wanted.example", "distractors.example"}}
			bounded := split == "time-windows"
			if bounded {
				plan.Domains = []string{"wanted.example"}
				plan.Scope = assistant.Scope{Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			}
			page, err := c.technicalSubjectPage(ctx, plan, bounded, nil, 8)
			if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != target.ContentID() {
				t.Fatalf("another bounded scope page's rejected labels displaced exact evidence: hits=%d more=%t limited=%t err=%v", len(page.Hits), page.HasMore, page.CoverageLimited, err)
			}
			physicalMore := false
			for _, query := range probe.queries {
				raw, err := store.SearchExact(ctx, query)
				if err != nil {
					t.Fatal(err)
				}
				physicalMore = physicalMore || raw.HasMore
			}
			if page.CoverageLimited || page.HasMore != physicalMore {
				t.Fatalf("validated merge changed physical coverage: more=%t physical-more=%t limited=%t", page.HasMore, physicalMore, page.CoverageLimited)
			}
		})
	}
}
