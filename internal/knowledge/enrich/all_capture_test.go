package enrich_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

type allCaptureGenerator struct{ inputs map[string][]string }

func (*allCaptureGenerator) Identity(context.Context) (string, error) {
	return "all-capture-coverage", nil
}
func (*allCaptureGenerator) CountInputTokens(context.Context, []derive.Evidence) (int, error) {
	return 512, nil
}
func (g *allCaptureGenerator) Generate(_ context.Context, in []derive.Evidence) ([]derive.Proposal, error) {
	for _, item := range in {
		g.inputs[item.SourceID] = append(g.inputs[item.SourceID], item.Text)
	}
	return nil, nil
}

func TestDerivationCoversEveryCapturedAppAndVoiceAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path, key := filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey()
	s, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	originals := map[string]string{}
	for _, app := range []string{"com.apple.terminal", "editor.app", "browser.app", "com.tinyspeck.slackmacgap", "com.apple.mail"} {
		var body strings.Builder
		for i := 0; i < 90; i++ {
			fmt.Fprintf(&body, "%s captured line %03d describes project state and decisions.\n", app, i)
		}
		o, err := capture.SanitizeObservation(app, "", body.String(), capture.MaxObservationTextBytes)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(ctx, o, time.Now()); err != nil {
			t.Fatal(err)
		}
		originals[o.ContentID()] = o.Text()
	}
	m := meetings.Meeting{ID: "all-capture-voice", Title: "Recorded project discussion", Locale: "en-US", StartedAt: time.Now(), State: meetings.Running, Tracks: []string{"microphone"}, Revision: 1}
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	r := meetings.Record{Key: m.ID + "/transcript/microphone/phrase", Kind: meetings.TranscriptFinal, SessionID: m.ID, Event: meetings.Event{Kind: meetings.Final, SessionID: m.ID, TrackID: "microphone", SegmentID: "phrase", Revision: 1, End: time.Second, Text: "The recorded project discussion has a searchable transcript."}}
	receipt, err := s.Append(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	originals[receipt.SourceID] = r.Event.Text
	g := &allCaptureGenerator{inputs: map[string][]string{}}
	w, err := enrich.NewService(s, s, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Backfill(ctx); !errors.Is(err, enrich.ErrMoreWork) {
		t.Fatal("long capture did not yield resumable work", err)
	}
	w.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	w, err = enrich.NewService(s, s, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	complete(t, w)
	for id, original := range originals {
		covered, after := make([]bool, len(original)), 0
		for _, text := range g.inputs[id] {
			at := strings.Index(original[after:], text)
			if at < 0 {
				t.Fatal("derivation changed source text", id)
			}
			at += after
			for i := at; i < at+len(text); i++ {
				covered[i] = true
			}
			after = at + 1
		}
		for i, r := range original {
			if !unicode.IsSpace(r) && !covered[i] {
				t.Fatalf("capture middle or tail omitted at byte %d, source %s", i, id)
			}
		}
		p, found, err := s.GetEnrichmentProgress(ctx, id)
		if err != nil || !found || !p.Complete || p.Declined || p.Next != len(original) {
			t.Fatal("incomplete capture claimed covered", p, err)
		}
		saved, found, err := s.GetSourceEvidence(ctx, id)
		if err != nil || !found || saved.Text() != original {
			t.Fatal("derivation changed original", id, err)
		}
	}
	calls := 0
	for _, inputs := range g.inputs {
		calls += len(inputs)
	}
	complete(t, w)
	again := 0
	for _, inputs := range g.inputs {
		again += len(inputs)
	}
	if again != calls {
		t.Fatal("completed captures were reprocessed")
	}
}
