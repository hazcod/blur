package client

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/preferences"
	"github.com/hazcod/blur/internal/storage/durable"
)

type nativeDocumentSpy struct {
	nativePolicyStore
	begins, appends, publishes, discards int
}

func (s *nativeDocumentSpy) BeginCapture(context.Context, capture.CaptureHeader) error {
	s.begins++
	return nil
}
func (s *nativeDocumentSpy) AppendCaptureParts(context.Context, capture.CaptureHeader, []capture.CapturePart) error {
	s.appends++
	return nil
}
func (s *nativeDocumentSpy) PublishCaptureWithSourceReceipt(context.Context, capture.CaptureSummary) (durable.CapturePublicationReceipt, error) {
	s.publishes++
	return durable.CapturePublicationReceipt{Committed: true, New: false, SourceID: strings.Repeat("b", 64)}, nil
}
func (s *nativeDocumentSpy) DiscardCaptureStage(context.Context, string) error {
	s.discards++
	return nil
}

func TestNativeDocumentCommitRechecksPolicyAndPreservesCanonicalReceipt(t *testing.T) {
	ctx := context.Background()
	prefs, err := preferences.NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	config := preferences.DefaultConfig()
	config.CaptureEnabled = true
	if err = prefs.Save(ctx, config); err != nil {
		t.Fatal(err)
	}
	spy := &nativeDocumentSpy{}
	sink := &nativeCaptureCommitSink{store: spy, preferences: prefs}
	builder, err := capture.NewCaptureBuilder("editor.app", "example.test", strings.Repeat("a", 64), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer builder.Abort()
	var parts []capture.CapturePart
	emit := func(_ context.Context, p []capture.CapturePart) error { parts = append(parts, p...); return nil }
	if err = builder.Write(ctx, "The rollback owner is Mira.", emit); err != nil {
		t.Fatal(err)
	}
	summary, err := builder.Finish(ctx, capture.CaptureComplete, emit)
	if err != nil {
		t.Fatal(err)
	}
	h := builder.Header()
	if err = sink.BeginCapture(ctx, h); err != nil {
		t.Fatal(err)
	}
	if err = sink.AppendCaptureParts(ctx, h, parts); err != nil {
		t.Fatal(err)
	}
	receipt, err := sink.PublishDocumentCapture(ctx, summary)
	if err != nil || !receipt.Committed || receipt.New || receipt.SourceID != strings.Repeat("b", 64) {
		t.Fatal("canonical duplicate identity changed", receipt, err)
	}
	config.CaptureEnabled = false
	if err = prefs.Save(ctx, config); err != nil {
		t.Fatal(err)
	}
	writes := []func() error{func() error { return sink.BeginCapture(ctx, h) }, func() error { return sink.AppendCaptureParts(ctx, h, parts) }, func() error { _, err := sink.PublishDocumentCapture(ctx, summary); return err }}
	for _, write := range writes {
		var ce *capture.CaptureError
		if err = write(); !errors.As(err, &ce) || ce.Code != capture.Excluded {
			t.Fatal("paused capture reached document store", err)
		}
	}
	if err = sink.DiscardCaptureStage(ctx, h.CaptureID()); err != nil {
		t.Fatal(err)
	}
	if spy.begins != 1 || spy.appends != 1 || spy.publishes != 1 || spy.discards != 1 {
		t.Fatal("document policy/cleanup mismatch", spy)
	}
}
