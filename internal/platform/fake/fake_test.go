package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nielsgroen/blur/internal/platform"
	"github.com/nielsgroen/blur/internal/platform/fake"
)

func unavailable(t *testing.T, err error, calls int) {
	t.Helper()
	var typed *platform.UnavailableError
	if !errors.As(err, &typed) {
		t.Fatalf("error = %v, want UnavailableError", err)
	}
	if typed.Report.State != platform.PermissionRequired {
		t.Fatalf("state = %q", typed.Report.State)
	}
	if calls != 0 {
		t.Fatalf("unavailable operation recorded %d side effects", calls)
	}
}

func TestUnavailablePortsFailClosed(t *testing.T) {
	ctx := context.Background()
	r := platform.CapabilityReport{State: platform.PermissionRequired, Reason: "denied", Evidence: "permission probe"}
	capture := fake.CaptureSource{Report: r}
	_, err := capture.Probe(ctx)
	unavailable(t, err, capture.Calls)
	_, err = capture.Next(ctx)
	unavailable(t, err, capture.Calls)
	targets := fake.TextTargets{Report: r}
	_, err = targets.Lease(ctx)
	unavailable(t, err, targets.Calls)
	err = targets.Replace(ctx, platform.TargetLease{}, "draft")
	unavailable(t, err, targets.Calls)
	hotkeys := fake.GestureHotkeys{Report: r}
	_, err = hotkeys.Next(ctx)
	unavailable(t, err, hotkeys.Calls)
	surfaces := fake.SurfaceManager{Report: r}
	err = surfaces.Show(ctx, platform.SurfaceRequest{})
	unavailable(t, err, surfaces.Calls)
	audio := fake.AudioRecorder{Report: r}
	err = audio.Start(ctx)
	unavailable(t, err, audio.Calls)
	_, err = audio.Read(ctx, 32)
	unavailable(t, err, audio.Calls)
	err = audio.Stop(ctx)
	unavailable(t, err, audio.Calls)
	secrets := fake.SecretStore{Report: r}
	_, err = secrets.Get(ctx, "key", 32)
	unavailable(t, err, secrets.Calls)
	err = secrets.Put(ctx, "key", []byte("secret"))
	unavailable(t, err, secrets.Calls)
	err = secrets.Delete(ctx, "key")
	unavailable(t, err, secrets.Calls)
	transport := fake.LocalTransport{Report: r}
	err = transport.Start(ctx)
	unavailable(t, err, transport.Calls)
	err = transport.Stop(ctx)
	unavailable(t, err, transport.Calls)
	paths := fake.PrivatePath{Report: r}
	_, err = paths.Ensure(ctx, "data")
	unavailable(t, err, paths.Calls)
	autostart := fake.Autostart{Report: r}
	err = autostart.SetEnabled(ctx, true)
	unavailable(t, err, autostart.Calls)
	session := fake.SessionState{Report: r}
	_, err = session.Current(ctx)
	unavailable(t, err, session.Calls)
}

func TestAvailableFakesReturnConfiguredResults(t *testing.T) {
	ctx := context.Background()
	r := platform.CapabilityReport{State: platform.Available, Reason: "ready", Evidence: "fixture"}
	c := fake.CaptureSource{Report: r, ProbeResult: platform.CaptureProbe{TargetID: "opaque", AppID: "org.example.Editor", Domain: "example.test", Private: true, Secure: true}, NextResult: platform.CaptureEvent{Kind: "focus_changed"}}
	if p, err := c.Probe(ctx); err != nil || p.TargetID != "opaque" || p.AppID != "org.example.Editor" || p.Domain != "example.test" || !p.Private {
		t.Fatalf("probe = %+v, %v", p, err)
	}
	if e, err := c.Next(ctx); err != nil || e.Kind != "focus_changed" {
		t.Fatalf("event = %+v, %v", e, err)
	}
	targets := fake.TextTargets{Report: r, LeaseResult: platform.TargetLease{ID: "lease"}}
	if l, err := targets.Lease(ctx); err != nil || l.ID != "lease" {
		t.Fatalf("lease = %+v, %v", l, err)
	}
	if err := targets.Replace(ctx, platform.TargetLease{ID: "lease"}, "draft"); err != nil || targets.Calls != 2 {
		t.Fatalf("replace: %v, calls %d", err, targets.Calls)
	}
	h := fake.GestureHotkeys{Report: r, NextResult: platform.GestureEvent{Kind: "tap"}}
	if e, err := h.Next(ctx); err != nil || e.Kind != "tap" {
		t.Fatalf("hotkey = %+v, %v", e, err)
	}
	s := fake.SurfaceManager{Report: r}
	if err := s.Show(ctx, platform.SurfaceRequest{Kind: "chat"}); err != nil || s.Calls != 1 {
		t.Fatalf("surface: %v", err)
	}
	a := fake.AudioRecorder{Report: r, Chunk: []byte("audio")}
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if chunk, err := a.Read(ctx, 5); err != nil || string(chunk) != "audio" {
		t.Fatalf("audio read = %q, %v", chunk, err)
	}
	if err := a.Stop(ctx); err != nil || a.Calls != 3 {
		t.Fatalf("audio: %v", err)
	}
	sec := fake.SecretStore{Report: r}
	if err := sec.Put(ctx, "key", []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if v, err := sec.Get(ctx, "key", 7); err != nil || string(v) != "fixture" {
		t.Fatalf("secret: %q, %v", v, err)
	}
	if err := sec.Put(ctx, "key", []byte("updated")); err != nil {
		t.Fatal(err)
	}
	if err := sec.Delete(ctx, "key"); err != nil || sec.Calls != 4 {
		t.Fatalf("secret lifecycle: %v", err)
	}
	tr := fake.LocalTransport{Report: r}
	if err := tr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tr.Stop(ctx); err != nil || tr.Calls != 2 {
		t.Fatalf("transport: %v", err)
	}
	p := fake.PrivatePath{Report: r, Path: "/fixture/private"}
	if v, err := p.Ensure(ctx, "data"); err != nil || v != "/fixture/private" {
		t.Fatalf("path = %q, %v", v, err)
	}
	au := fake.Autostart{Report: r}
	if err := au.SetEnabled(ctx, true); err != nil || au.Calls != 1 {
		t.Fatalf("autostart: %v", err)
	}
	ss := fake.SessionState{Report: r, Snapshot: platform.SessionSnapshot{Locked: true}}
	if v, err := ss.Current(ctx); err != nil || !v.Locked {
		t.Fatalf("session = %+v, %v", v, err)
	}
}

func TestCaptureProbeErrorHasNoResult(t *testing.T) {
	denied := errors.New("synthetic denied probe")
	c := fake.CaptureSource{Report: platform.CapabilityReport{State: platform.Available, Reason: "ready", Evidence: "fixture"}, ProbeResult: platform.CaptureProbe{TargetID: "t"}, ProbeError: denied}
	probe, err := c.Probe(context.Background())
	if !errors.Is(err, denied) || probe != (platform.CaptureProbe{}) {
		t.Fatalf("probe=%+v err=%v", probe, err)
	}
}

func TestBoundedReadsRejectOversizeWithoutEffects(t *testing.T) {
	r := platform.CapabilityReport{State: platform.Available, Reason: "ready", Evidence: "fixture"}
	a := fake.AudioRecorder{Report: r, Chunk: []byte("audio")}
	if _, err := a.Read(context.Background(), 4); !errors.Is(err, platform.ErrReadLimit) || a.Calls != 0 {
		t.Fatalf("audio limit = %v, calls %d", err, a.Calls)
	}
	s := fake.SecretStore{Report: r}
	if err := s.Put(context.Background(), "key", []byte("secret")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), "key", 5); !errors.Is(err, platform.ErrReadLimit) || s.Calls != 1 {
		t.Fatalf("secret limit = %v, calls %d", err, s.Calls)
	}
}

func TestCancelledOperationsHaveNoEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := platform.CapabilityReport{State: platform.Available, Reason: "ready", Evidence: "fixture"}
	c := fake.CaptureSource{Report: r}
	_, err := c.Next(ctx)
	if !errors.Is(err, context.Canceled) || c.Calls != 0 {
		t.Fatalf("capture cancellation = %v, calls %d", err, c.Calls)
	}
	s := fake.SecretStore{Report: r}
	err = s.Put(ctx, "key", []byte("secret"))
	if !errors.Is(err, context.Canceled) || s.Calls != 0 {
		t.Fatalf("secret cancellation = %v, calls %d", err, s.Calls)
	}
}

func TestWaitingCaptureEventStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := platform.CapabilityReport{State: platform.Available, Reason: "ready", Evidence: "fixture"}
	c := fake.CaptureSource{Report: r, NextWait: make(chan platform.CaptureEvent)}
	done := make(chan error, 1)
	go func() { _, err := c.Next(ctx); done <- err }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting Next = %v", err)
	}
	if c.Calls != 0 {
		t.Fatalf("cancelled wait recorded %d effects", c.Calls)
	}
}

func TestTextTargetsRejectsUnissuedAndStaleLeases(t *testing.T) {
	ctx := context.Background()
	f := fake.TextTargets{Report: platform.CapabilityReport{State: platform.Available, Reason: "ready", Evidence: "fixture"}, LeaseResult: platform.TargetLease{ID: "first"}}
	for _, lease := range []platform.TargetLease{{}, {ID: "first"}, {ID: "other"}} {
		if err := f.Replace(ctx, lease, "draft"); !errors.Is(err, platform.ErrInvalidLease) || f.Calls != 0 {
			t.Fatalf("unissued replace = %v, calls %d", err, f.Calls)
		}
	}
	issued, err := f.Lease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Replace(ctx, issued, "draft"); err != nil || f.Calls != 2 {
		t.Fatalf("valid replace = %v, calls %d", err, f.Calls)
	}
	f.LeaseResult = platform.TargetLease{ID: "second"}
	if err := f.Replace(ctx, issued, "draft"); !errors.Is(err, platform.ErrInvalidLease) || f.Calls != 2 {
		t.Fatalf("stale replace = %v, calls %d", err, f.Calls)
	}
}

func TestSecretStoreIsolatesKeysAndCopiesValues(t *testing.T) {
	ctx := context.Background()
	f := fake.SecretStore{Report: platform.CapabilityReport{State: platform.Available, Reason: "ready", Evidence: "fixture"}}
	input := []byte("alpha")
	if err := f.Put(ctx, "one", input); err != nil {
		t.Fatal(err)
	}
	input[0] = 'X'
	if err := f.Put(ctx, "two", []byte("beta")); err != nil {
		t.Fatal(err)
	}
	one, err := f.Get(ctx, "one", 5)
	if err != nil || string(one) != "alpha" {
		t.Fatalf("key one = %q, %v", one, err)
	}
	one[0] = 'X'
	again, err := f.Get(ctx, "one", 5)
	if err != nil || string(again) != "alpha" {
		t.Fatalf("aliased key one = %q, %v", again, err)
	}
	if err := f.Delete(ctx, "two"); err != nil {
		t.Fatal(err)
	}
	if other, err := f.Get(ctx, "two", 5); err != nil || other != nil {
		t.Fatalf("deleted key two = %q, %v", other, err)
	}
	if remaining, err := f.Get(ctx, "one", 5); err != nil || string(remaining) != "alpha" {
		t.Fatalf("remaining key one = %q, %v", remaining, err)
	}
}

func TestInvalidFakeReportsFailClosed(t *testing.T) {
	ctx := context.Background()
	for _, configured := range []platform.CapabilityReport{{}, {State: "unexpected", Reason: "ready", Evidence: "fixture"}, {State: platform.Available, Reason: "not a code", Evidence: "fixture"}, {ID: "wrong", State: platform.Available, Reason: "ready", Evidence: "fixture"}} {
		f := fake.CaptureSource{Report: configured}
		report, err := f.Capability(ctx)
		if !errors.Is(err, platform.ErrInvalidCapabilityReport) || report.Validate() != nil || report.State == platform.Available {
			t.Fatalf("invalid report = %+v, %v", report, err)
		}
		_, err = f.Probe(ctx)
		var unavailable *platform.UnavailableError
		if !errors.As(err, &unavailable) || unavailable.Report.Validate() != nil || unavailable.Report.State == platform.Available || f.Calls != 0 {
			t.Fatalf("invalid operation = %v, calls %d", err, f.Calls)
		}
	}
}

func TestValidReasonNamedInvalidReportRemainsAvailable(t *testing.T) {
	f := fake.CaptureSource{Report: platform.CapabilityReport{State: platform.Available, Reason: "invalid_report", Evidence: "fixture"}}
	r, err := f.Capability(context.Background())
	if err != nil || r.State != platform.Available {
		t.Fatalf("valid report = %+v, %v", r, err)
	}
}

func TestEveryPortRejectsInvalidReports(t *testing.T) {
	ctx := context.Background()
	for _, configured := range []platform.CapabilityReport{{}, {State: "unknown", Reason: "ready", Evidence: "fixture"}, {State: platform.Available, Reason: "not a code", Evidence: "fixture"}, {ID: "wrong", State: platform.Available, Reason: "ready", Evidence: "fixture"}} {
		c := &fake.CaptureSource{Report: configured}
		tt := &fake.TextTargets{Report: configured}
		h := &fake.GestureHotkeys{Report: configured}
		s := &fake.SurfaceManager{Report: configured}
		a := &fake.AudioRecorder{Report: configured}
		sec := &fake.SecretStore{Report: configured}
		tr := &fake.LocalTransport{Report: configured}
		p := &fake.PrivatePath{Report: configured}
		au := &fake.Autostart{Report: configured}
		ss := &fake.SessionState{Report: configured}
		ports := []struct {
			name     string
			reporter platform.CapabilityReporter
			op       func() (error, int)
		}{
			{"capture", c, func() (error, int) { _, err := c.Probe(ctx); return err, c.Calls }},
			{"text_targets", tt, func() (error, int) { _, err := tt.Lease(ctx); return err, tt.Calls }},
			{"gesture_hotkeys", h, func() (error, int) { _, err := h.Next(ctx); return err, h.Calls }},
			{"surface_manager", s, func() (error, int) { err := s.Show(ctx, platform.SurfaceRequest{}); return err, s.Calls }},
			{"audio_recorder", a, func() (error, int) { err := a.Start(ctx); return err, a.Calls }},
			{"secret_store", sec, func() (error, int) { _, err := sec.Get(ctx, "key", 32); return err, sec.Calls }},
			{"local_transport", tr, func() (error, int) { err := tr.Start(ctx); return err, tr.Calls }},
			{"private_path", p, func() (error, int) { _, err := p.Ensure(ctx, "data"); return err, p.Calls }},
			{"autostart", au, func() (error, int) { err := au.SetEnabled(ctx, true); return err, au.Calls }},
			{"session_state", ss, func() (error, int) { _, err := ss.Current(ctx); return err, ss.Calls }},
		}
		for _, port := range ports {
			t.Run(port.name+"/"+string(configured.State), func(t *testing.T) {
				report, err := port.reporter.Capability(ctx)
				if !errors.Is(err, platform.ErrInvalidCapabilityReport) || report.Validate() != nil || report.State == platform.Available {
					t.Fatalf("report = %+v, %v", report, err)
				}
				err, calls := port.op()
				var unavailable *platform.UnavailableError
				if !errors.As(err, &unavailable) || unavailable.Report.Validate() != nil || unavailable.Report.State == platform.Available || calls != 0 {
					t.Fatalf("operation = %v, calls %d", err, calls)
				}
			})
		}
	}
}
