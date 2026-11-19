package capture

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/fake"
)

func titleObservation(t *testing.T) SanitizedObservation {
	t.Helper()
	o, err := SanitizeObservation("example.app", "example.com", "plain text", 128)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestObservationWithoutTitleKeepsEarlierEncodings(t *testing.T) {
	o := titleObservation(t)
	v1, err := MarshalAuthenticatedObservation(o)
	if err != nil || v1[0] != 1 {
		t.Fatalf("no title, no focus: version %v err=%v, want 1", v1[0], err)
	}
	v2, err := MarshalAuthenticatedObservation(o.WithFocus("AXTextField", "Subject", "Compose"))
	if err != nil || v2[0] != 2 {
		t.Fatalf("focus only: version %v err=%v, want 2", v2[0], err)
	}
	for _, encoded := range [][]byte{v1, v2} {
		restored, err := RestoreAuthenticatedObservation(encoded)
		if err != nil || restored.Title() != "" {
			t.Fatalf("old record: %v title=%q", err, restored.Title())
		}
	}
}

func TestObservationTitleRoundTripsAsVersionThree(t *testing.T) {
	o := titleObservation(t)
	titled := o.WithTitle("Dima, Dziga (DM) - Cresco - Slack")
	if titled.Title() != "Dima, Dziga (DM) - Cresco - Slack" || titled.ContentID() != o.ContentID() {
		t.Fatalf("title=%q; the title must not enter the content ID", titled.Title())
	}
	for name, value := range map[string]SanitizedObservation{
		"title only":       titled,
		"title and focus":  titled.WithFocus("AXTextField", "Subject é", "Compose > Header"),
		"non-ascii title":  o.WithTitle("CV_Youssef_résumé.pdf — Preview"),
		"maximum title":    o.WithTitle(strings.Repeat("x", MaxTitleBytes)),
		"focus keeps role": o.WithTitle("t").WithFocus("AXTextField", "", ""),
	} {
		encoded, err := MarshalAuthenticatedObservation(value)
		if err != nil || encoded[0] != 3 {
			t.Fatalf("%s: version %v err=%v, want 3", name, encoded[0], err)
		}
		restored, err := RestoreAuthenticatedObservation(encoded)
		if err != nil || !reflect.DeepEqual(restored, value) {
			t.Fatalf("%s: round trip %v\n got %+v\nwant %+v", name, err, restored, value)
		}
		// Truncation and a stray trailing byte are both rejected.
		for _, end := range []int{len(encoded) - 1, len(encoded) - 2} {
			if _, err := RestoreAuthenticatedObservation(encoded[:end]); err == nil {
				t.Fatalf("%s: truncated to %d accepted", name, end)
			}
		}
		if _, err := RestoreAuthenticatedObservation(append(bytes.Clone(encoded), 0)); err == nil {
			t.Fatalf("%s: trailing byte accepted", name)
		}
	}
}

func TestObservationTitleDecodeRejectsCorruptTrailer(t *testing.T) {
	o := titleObservation(t).WithTitle("Pentest planning Q4")
	good, err := MarshalAuthenticatedObservation(o)
	if err != nil {
		t.Fatal(err)
	}
	titleLengthAt := len(good) - len(o.Title()) - 2
	mutate := func(edit func([]byte) []byte) []byte { return edit(bytes.Clone(good)) }
	withTitle := func(title string) []byte {
		b := bytes.Clone(good[:titleLengthAt])
		b = binary.BigEndian.AppendUint16(b, uint16(len(title)))
		return append(b, title...)
	}
	for name, bad := range map[string][]byte{
		"zero title length": mutate(func(b []byte) []byte {
			binary.BigEndian.PutUint16(b[titleLengthAt:], 0)
			return b[:titleLengthAt+2]
		}),
		"title length too long": mutate(func(b []byte) []byte {
			binary.BigEndian.PutUint16(b[titleLengthAt:], uint16(len(o.Title())+1))
			return b
		}),
		"title length too short": mutate(func(b []byte) []byte {
			binary.BigEndian.PutUint16(b[titleLengthAt:], uint16(len(o.Title())-1))
			return b
		}),
		"over the title cap":              withTitle(strings.Repeat("y", MaxTitleBytes+1)),
		"invalid utf-8":                   mutate(func(b []byte) []byte { b[len(b)-1] = 0xff; return b }),
		"nul in title":                    mutate(func(b []byte) []byte { b[len(b)-1] = 0; return b }),
		"secret title":                    withTitle("password: hunter2hunter2"),
		"padded title":                    withTitle(" padded "),
		"title cut off before its length": good[:titleLengthAt+1],
		"focus trailer cut off":           good[:titleLengthAt-2],
		"version 2 with title bytes":      mutate(func(b []byte) []byte { b[0] = 2; return b }),
		"unknown version":                 mutate(func(b []byte) []byte { b[0] = 4; return b }),
	} {
		if _, err := RestoreAuthenticatedObservation(bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestObservationValidateRejectsUnsafeTitle(t *testing.T) {
	o := titleObservation(t)
	for name, bad := range map[string]SanitizedObservation{
		"too long":     o.withTitleRaw(strings.Repeat("x", MaxTitleBytes+1)),
		"secret":       o.withTitleRaw("password: hunter2hunter2"),
		"nul":          o.withTitleRaw("a\x00b"),
		"bad utf-8":    o.withTitleRaw("a\xffb"),
		"padded":       o.withTitleRaw(" padded "),
		"github token": o.withTitleRaw("ghp_" + strings.Repeat("a1B2", 10)),
	} {
		if bad.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
		if _, err := MarshalAuthenticatedObservation(bad); err == nil {
			t.Errorf("%s marshaled", name)
		}
	}
}

func TestWithTitleRedactsFlattensAndBounds(t *testing.T) {
	o := titleObservation(t)
	if got := o.WithTitle("  Inbox\n\t(3)  —  Mail  ").Title(); got != "Inbox (3) — Mail" {
		t.Fatalf("flattened = %q", got)
	}
	secret := o.WithTitle("login password: hunter2hunter2 page")
	if strings.Contains(secret.Title(), "hunter2") || secret.Validate() != nil {
		t.Fatalf("secret survived: %q %v", secret.Title(), secret.Validate())
	}
	if got := o.WithTitle(strings.Repeat("é", 300)).Title(); len(got) > MaxTitleBytes || !strings.HasPrefix(got, "é") || len(got)%2 != 0 {
		t.Fatalf("bounded title = %d bytes, want <= %d and rune-aligned", len(got), MaxTitleBytes)
	}
	for name, raw := range map[string]string{"blank": "   ", "nul": "a\x00b", "invalid utf-8": "a\xffb"} {
		if got := o.WithTitle(raw).Title(); got != "" {
			t.Fatalf("%s title kept: %q", name, got)
		}
	}
	if cleared := o.WithTitle("x").WithTitle(""); cleared.Title() != "" || cleared.hasTitle() {
		t.Fatal("WithTitle(\"\") must clear the title")
	}
}

func titlePipeline(app, text string, title func(context.Context) string) Pipeline {
	return Pipeline{
		Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "t", AppID: app}},
		Limits: Limits{MaxBytes: 4096, MaxNodes: 3, MaxDepth: 2, MaxDuration: time.Second},
		Read: func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
			return rawFrame{
				roots: []*rawNode{{declaredBytes: len(text), value: func(context.Context, int) (string, error) { return text, nil }}},
				title: title,
			}, nil
		},
	}
}

func TestCaptureStoresSanitizedWindowTitle(t *testing.T) {
	var budget time.Duration
	p := titlePipeline("example.app", "visible window text", func(ctx context.Context) string {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("title read has no deadline")
		}
		budget = time.Until(deadline)
		return "Tiiny Pocket | Personal AI Infrastructure for Local LLMs\n"
	})
	o, err := p.Capture(context.Background())
	if err != nil || o.Title() != "Tiiny Pocket | Personal AI Infrastructure for Local LLMs" {
		t.Fatalf("title = %q err=%v", o.Title(), err)
	}
	if budget <= 0 || budget > TitleReadBudget {
		t.Fatalf("title budget = %v, want within %v", budget, TitleReadBudget)
	}
	without, err := titlePipeline("example.app", "visible window text", nil).Capture(context.Background())
	if err != nil || without.ContentID() != o.ContentID() || without.Title() != "" {
		t.Fatalf("title must not change the content ID: %v", err)
	}
}

func TestCaptureRedactsSecretLookingWindowTitle(t *testing.T) {
	p := titlePipeline("example.app", "visible window text", func(context.Context) string {
		return "deploy token: ghp_" + strings.Repeat("a1B2", 10)
	})
	o, err := p.Capture(context.Background())
	if err != nil || strings.Contains(o.Title(), "ghp_") || o.Validate() != nil {
		t.Fatalf("secret survived: %q %v", o.Title(), err)
	}
}

func TestCaptureSurvivesEmptyPanickingOrSlowTitleRead(t *testing.T) {
	for name, read := range map[string]func(context.Context) string{
		"empty": func(context.Context) string { return "" },
		"panic": func(context.Context) string { panic("native title read blew up") },
		"slow": func(ctx context.Context) string {
			<-ctx.Done()
			return "late title"
		},
	} {
		start := time.Now()
		o, err := titlePipeline("example.app", "visible window text", read).Capture(context.Background())
		if err != nil || o.Title() != "" || o.Text() != "visible window text" {
			t.Fatalf("%s: err=%v title=%q", name, err, o.Title())
		}
		if elapsed := time.Since(start); elapsed > 600*time.Millisecond {
			t.Fatalf("%s: title read held the capture for %v", name, elapsed)
		}
	}
}

func TestCaptureNeverKeepsPrivateBrowserTitle(t *testing.T) {
	const private = "Private Browsing - Secret plans"
	if !isPrivateBrowserWindowTitle(private) {
		t.Skip("fixture title is not recognised as private on this build")
	}
	o, err := titlePipeline("com.apple.safari", "page text", func(context.Context) string { return private }).Capture(context.Background())
	if err != nil || o.Title() != "" {
		t.Fatalf("private browser title kept: %q %v", o.Title(), err)
	}
}

func TestCaptureDoesNotReadTitleOfSkippedWindows(t *testing.T) {
	called := false
	read := func(context.Context) string { called = true; return "must not be read" }
	for name, probe := range map[string]platform.CaptureProbe{
		"secure":   {TargetID: "t", AppID: "example.app", Secure: true},
		"private":  {TargetID: "t", AppID: "example.app", Private: true},
		"elevated": {TargetID: "t", AppID: "example.app", Elevated: true},
	} {
		p := titlePipeline("example.app", "text", read)
		p.Source = &fake.CaptureSource{Report: available(), ProbeResult: probe}
		if _, err := p.Capture(context.Background()); err == nil {
			t.Fatalf("%s window captured", name)
		}
	}
	if called {
		t.Fatal("a title was read for a secure, private or elevated window")
	}
}
