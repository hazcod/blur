package client

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/preferences"
)

type nativePolicyStore struct{ writes, visits, segments int }

func (*nativePolicyStore) EncryptedAtRest() bool { return true }
func (s *nativePolicyStore) PutObservation(context.Context, capture.SanitizedObservation) error {
	s.writes++
	return nil
}
func (s *nativePolicyStore) PutObservationAt(context.Context, capture.SanitizedObservation, time.Time) error {
	s.writes++
	return nil
}
func (s *nativePolicyStore) RecordObservationSighting(context.Context, capture.SanitizedObservation, time.Time) error {
	s.visits++
	return nil
}
func (s *nativePolicyStore) PutActivitySegment(context.Context, activity.Segment) error {
	s.segments++
	return nil
}

func TestNativeCaptureCommitRechecksLatestPreferencesForEveryWrite(t *testing.T) {
	for _, mode := range []string{"disabled", "app", "domain", "default-disabled", "unknown-domain"} {
		t.Run(mode, func(t *testing.T) {
			prefs, err := preferences.NewStore(filepath.Join(t.TempDir(), "preferences.json"))
			if err != nil {
				t.Fatal(err)
			}
			config := preferences.DefaultConfig()
			config.CaptureEnabled = true
			if err := prefs.Save(context.Background(), config); err != nil {
				t.Fatal(err)
			}
			spy := &nativePolicyStore{}
			sink := &nativeCaptureCommitSink{store: spy, preferences: prefs}
			domain := "example.test"
			if mode == "unknown-domain" {
				domain = ""
			}
			observation, err := capture.SanitizeObservation("com.kagi.kagimacos", domain, "Synthetic visible text", 1024)
			if err != nil {
				t.Fatal(err)
			}
			if err := sink.PutObservation(context.Background(), observation); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "disabled":
				config.CaptureEnabled = false
			case "app":
				config.ExcludedApps = []string{"com.kagi.kagimacos"}
			case "domain":
				config.ExcludedDomains = []string{"example.test"}
			case "default-disabled":
				config.CaptureDefaultDisabled = true
			case "unknown-domain":
				config.ExcludedDomains = []string{"private.test"}
			}
			if err := prefs.Save(context.Background(), config); err != nil {
				t.Fatal(err)
			}
			for _, write := range []func() error{
				func() error { return sink.PutObservation(context.Background(), observation) },
				func() error { return sink.PutObservationAt(context.Background(), observation, time.Now()) },
				func() error { return sink.RecordObservationSighting(context.Background(), observation, time.Now()) },
			} {
				var rejected *capture.CaptureError
				if err := write(); !errors.As(err, &rejected) || rejected.Code != capture.Excluded {
					t.Fatal("latest policy not enforced", err)
				}
			}
			if spy.writes != 1 || spy.visits != 0 {
				t.Fatal("policy-rejected capture reached storage")
			}
			if err := sink.PutActivitySegment(context.Background(), activity.Segment{AppID: observation.AppID(), Domain: domain}); err == nil || spy.segments != 0 {
				t.Fatal("policy-rejected pending segment reached storage")
			}
		})
	}
}

type blockingNativePolicyStore struct {
	nativePolicyStore
	entered chan struct{}
	release chan struct{}
}

func (s *blockingNativePolicyStore) PutObservationAt(ctx context.Context, _ capture.SanitizedObservation, _ time.Time) error {
	close(s.entered)
	select {
	case <-s.release:
		s.writes++
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestNativeCaptureCommitLinearizesPreferenceSaveWithWrite(t *testing.T) {
	ctx := context.Background()
	prefs, err := preferences.NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	config := preferences.DefaultConfig()
	config.CaptureEnabled = true
	if err := prefs.Save(ctx, config); err != nil {
		t.Fatal(err)
	}
	spy := &blockingNativePolicyStore{entered: make(chan struct{}), release: make(chan struct{})}
	sink := &nativeCaptureCommitSink{store: spy, preferences: prefs}
	observation, err := capture.SanitizeObservation("dev.herdr.app", "", "Synthetic", 1024)
	if err != nil {
		t.Fatal(err)
	}
	writeDone := make(chan error, 1)
	go func() { writeDone <- sink.PutObservationAt(ctx, observation, time.Now()) }()
	<-spy.entered
	config.ExcludedApps = []string{"dev.herdr.app"}
	saveDone := make(chan error, 1)
	go func() { saveDone <- prefs.Save(ctx, config) }()
	select {
	case err := <-saveDone:
		t.Fatal("preference Save passed in-flight commit", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(spy.release)
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-saveDone; err != nil {
		t.Fatal(err)
	}
	if err := sink.PutObservation(ctx, observation); err == nil || spy.writes != 1 {
		t.Fatal("old admission survived successful preference Save")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := sink.PutObservation(canceled, observation); !errors.Is(err, context.Canceled) {
		t.Fatal("commit lost cancellation", err)
	}
}

func TestNativeVisualAcquisitionLinearizesPreferenceSaveAndHonorsCancellation(t *testing.T) {
	prefs, err := preferences.NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	config := preferences.DefaultConfig()
	config.CaptureEnabled = true
	if err := prefs.Save(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	admit := nativeCaptureVisualAdmission(prefs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	released := make(chan error, 1)
	go func() {
		released <- admit(ctx, "dev.herdr.app", "", func(readCtx context.Context) error { close(entered); <-readCtx.Done(); return readCtx.Err() })
	}()
	<-entered
	config.CaptureEnabled = false
	saveDone := make(chan error, 1)
	go func() { saveDone <- prefs.Save(context.Background(), config) }()
	select {
	case err := <-saveDone:
		t.Fatal("Save overtook admitted pixel acquisition", err)
	case <-time.After(20 * time.Millisecond):
	}
	// Cancellation is independent of the preference lock, so pause can abort OCR
	// immediately rather than waiting for the policy write that is queued behind it.
	cancel()
	if err := <-released; !errors.Is(err, context.Canceled) {
		t.Fatal("visual acquisition ignored cancellation", err)
	}
	if err := <-saveDone; err != nil {
		t.Fatal(err)
	}
	reads := 0
	err = admit(context.Background(), "dev.herdr.app", "", func(context.Context) error { reads++; return nil })
	var rejected *capture.CaptureError
	if !errors.As(err, &rejected) || rejected.Code != capture.Excluded || reads != 0 {
		t.Fatal("old preference allowed pixel acquisition after Save", err)
	}
}
