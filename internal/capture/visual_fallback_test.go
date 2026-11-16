package capture

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/darwin"
	"github.com/hazcod/blur/internal/platform/fake"
)

func visualFixture(text string, calls *int) Pipeline {
	return Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "fixture", AppID: "dev.herdr.app"}}, Limits: limits(), Read: func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
		return rawFrame{roots: []*rawNode{{role: "AXGroup", declaredBytes: len(text), value: func(context.Context, int) (string, error) { return text, nil }}}, validate: func(context.Context) error { return nil }, visual: func(_ context.Context, max int, _ visualAdmission) (string, bool, error) {
			*calls++
			if max != limits().MaxBytes {
				return "", false, errors.New("wrong visual byte budget")
			}
			return "Visible canvas text\napi_key=synthetic-secret", false, nil
		}}, nil
	}}
}

func TestVisualFallbackUsesExistingSanitizerAfterVerifiedEmptyAX(t *testing.T) {
	calls := 0
	p := visualFixture("", &calls)
	got, err := p.Capture(context.Background())
	if err != nil || calls != 1 || !strings.Contains(got.Text(), "Visible canvas text") || strings.Contains(got.Text(), "synthetic-secret") || !got.Truncated() || got.AppID() != "dev.herdr.app" {
		t.Fatalf("verified visual text was not bounded/redacted/marked partial: calls=%d err=%v", calls, err)
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestVisualFallbackRecoversWhenAXTextValueCannotComplete(t *testing.T) {
	for _, stage := range []ReadStage{ReadStageText, ReadStageRange} {
		t.Run(string(stage), func(t *testing.T) {
			calls := 0
			p := visualFixture("", &calls)
			base := p.Read
			p.Read = func(ctx context.Context, probe platform.CaptureProbe, limits Limits) (rawFrame, error) {
				frame, err := base(ctx, probe, limits)
				frame.roots[0].value = func(context.Context, int) (string, error) {
					return "", NativeReadError(stage, -25202)
				}
				return frame, err
			}
			got, err := p.Capture(context.Background())
			if err != nil || calls != 1 || !strings.Contains(got.Text(), "Visible canvas text") || !got.Truncated() {
				t.Fatalf("eligible AX text failure did not use guarded visual fallback: calls=%d text=%q err=%v", calls, got.Text(), err)
			}
		})
	}
}

func TestVisualFallbackGetsSeparateBudgetAfterAXTextTimeout(t *testing.T) {
	calls := 0
	p := visualFixture("", &calls)
	p.Limits.MaxDuration = 25 * time.Millisecond
	base := p.Read
	p.Read = func(ctx context.Context, probe platform.CaptureProbe, limits Limits) (rawFrame, error) {
		frame, err := base(ctx, probe, limits)
		frame.roots[0].value = func(ctx context.Context, _ int) (string, error) {
			<-ctx.Done()
			return "", contextualReadError(ReadStageText, ctx.Err())
		}
		frame.visual = func(ctx context.Context, _ int, _ visualAdmission) (string, bool, error) {
			if _, ok := ctx.Deadline(); !ok || ctx.Err() != nil {
				t.Fatal("visual fallback inherited the expired AX deadline")
			}
			calls++
			return "Visible canvas text", false, nil
		}
		return frame, err
	}
	got, err := p.Capture(context.Background())
	if err != nil || calls != 1 || !strings.Contains(got.Text(), "Visible canvas text") || !got.Truncated() {
		t.Fatalf("AX text timeout did not transition to bounded visual read: calls=%d text=%q err=%v", calls, got.Text(), err)
	}
}

func TestVisualFallbackNeverReplacesReadableAXAcrossAppFamilies(t *testing.T) {
	for _, app := range []string{"com.apple.finder", "com.tinyspeck.slackmacgap", "com.microsoft.outlook", "net.whatsapp.whatsapp", "org.whispersystems.signal-desktop", "com.microsoft.teams2", "com.apple.terminal", "com.microsoft.vscode", "dev.herdr.app", "com.kagi.kagimacos", "com.apple.safari", "com.google.chrome"} {
		t.Run(app, func(t *testing.T) {
			calls := 0
			p := visualFixture("Native visible text", &calls)
			p.Source = &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "fixture", AppID: app}}
			got, err := p.Capture(context.Background())
			if err != nil || got.Text() != "Native visible text" || calls != 0 {
				t.Fatalf("generic AX coverage regressed: visual_calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestVisualFallbackCannotBypassPrivateSensitiveUnknownOrExcludedFrames(t *testing.T) {
	for _, scenario := range []string{"app", "private", "secure", "elevated", "sensitive-node", "incomplete-tree", "no-validation", "stale-window", "domain", "latest-policy"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			p := visualFixture("", &calls)
			probe := platform.CaptureProbe{TargetID: "fixture", AppID: "dev.herdr.app"}
			base := p.Read
			p.Read = func(ctx context.Context, probe platform.CaptureProbe, limits Limits) (rawFrame, error) {
				f, err := base(ctx, probe, limits)
				switch scenario {
				case "sensitive-node":
					f.roots[0].sensitive = true
				case "incomplete-tree":
					f.truncated = true
				case "no-validation":
					f.validate = nil
				case "stale-window":
					f.validate = func(context.Context) error { return errors.New("stale") }
				case "domain":
					f.domain, f.domainKnown = "private.example", true
				}
				return f, err
			}
			switch scenario {
			case "app":
				p.Exclusions.Apps = []string{probe.AppID}
			case "private":
				probe.Private = true
			case "secure":
				probe.Secure = true
			case "elevated":
				probe.Elevated = true
			case "domain":
				p.Exclusions.Domains = []string{"private.example"}
			case "latest-policy":
				checks := 0
				p.CurrentExclusions = func(context.Context) (Exclusions, error) {
					checks++
					if checks > 1 {
						return Exclusions{Apps: []string{probe.AppID}}, nil
					}
					return Exclusions{}, nil
				}
			}
			p.Source = &fake.CaptureSource{Report: available(), ProbeResult: probe}
			got, err := p.Capture(context.Background())
			if err == nil || got.Text() != "" || calls != 0 {
				t.Fatalf("unsafe frame entered visual capture: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestVisualFallbackRevalidatesAfterOCRAndHonorsOriginalCancellation(t *testing.T) {
	for _, scenario := range []string{"window", "policy", "cancel", "oversize"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls, reads := 0, 0
			p := visualFixture("", &calls)
			base := p.Read
			p.Read = func(ctx context.Context, probe platform.CaptureProbe, limits Limits) (rawFrame, error) {
				f, err := base(ctx, probe, limits)
				f.validate = func(context.Context) error {
					if scenario == "window" && reads != 0 {
						return errors.New("changed window")
					}
					return nil
				}
				f.visual = func(ctx context.Context, max int, _ visualAdmission) (string, bool, error) {
					reads++
					if scenario == "cancel" {
						cancel()
						return "late private text", false, nil
					}
					if scenario == "oversize" {
						return strings.Repeat("x", max+1), false, nil
					}
					return "late private text", false, ctx.Err()
				}
				return f, err
			}
			p.CurrentExclusions = func(context.Context) (Exclusions, error) {
				if scenario == "policy" && reads > 0 {
					return Exclusions{Apps: []string{"dev.herdr.app"}}, nil
				}
				return Exclusions{}, nil
			}
			got, err := p.Capture(ctx)
			if err == nil || got.Text() != "" || reads != 1 {
				t.Fatalf("late/oversized visual text escaped: reads=%d err=%v", reads, err)
			}
			if scenario == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("parent cancellation was detached", err)
			}
		})
	}
}

func TestVisualFallbackHasSeparateBoundedStageWithoutGrowingAXDeadline(t *testing.T) {
	calls := 0
	p := visualFixture("", &calls)
	p.Limits.MaxDuration = 25 * time.Millisecond
	base := p.Read
	p.Read = func(ctx context.Context, probe platform.CaptureProbe, limits Limits) (rawFrame, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Millisecond {
			t.Fatal("visual fallback expanded AX deadline")
		}
		f, err := base(ctx, probe, limits)
		f.visual = func(ctx context.Context, _ int, _ visualAdmission) (string, bool, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) <= 25*time.Millisecond || time.Until(deadline) > 3*time.Second {
				t.Fatal("visual stage lacks separate bounded deadline")
			}
			return "Bounded visual text", true, nil
		}
		return f, err
	}
	if _, err := p.Capture(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestVisualFallbackNeverReadsBrowserChrome(t *testing.T) {
	calls := 0
	p := visualFixture("", &calls)
	p.Source = &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "fixture", AppID: "com.google.chrome", Domain: "allowed.example"}}
	p.Exclusions.Domains = []string{"private.example"}
	got, err := p.Capture(context.Background())
	var classified *CaptureError
	if !errors.As(err, &classified) || classified.Code != BrowserUnavailable || got.Text() != "" || calls != 0 {
		t.Fatal("whole-window OCR bypassed browser WebArea/chrome boundary", err)
	}
}

func TestVisualFallbackChecksFinalAcquisitionAfterSlowWindowValidation(t *testing.T) {
	calls := 0
	p := visualFixture("", &calls)
	p.Limits.MaxBytes = 4096
	checks := 0
	denied := false
	p.CurrentExclusions = func(context.Context) (Exclusions, error) {
		if denied {
			return Exclusions{Apps: []string{"dev.herdr.app"}}, nil
		}
		return Exclusions{}, nil
	}
	reader := &syntheticVisualReader{window: visualWindow(), text: "private late text"}
	base := p.Read
	p.Read = func(ctx context.Context, probe platform.CaptureProbe, l Limits) (rawFrame, error) {
		frame, err := base(ctx, probe, l)
		frame.validate = func(context.Context) error {
			checks++
			if checks == 3 {
				denied = true
			}
			return nil
		}
		frame.visual = func(ctx context.Context, max int, admit visualAdmission) (string, bool, error) {
			return readVisualText(ctx, reader, func(context.Context, darwin.ScreenTextWindow) error { return nil }, "late-policy", max, admit)
		}
		return frame, err
	}
	got, err := p.Capture(context.Background())
	var classified *CaptureError
	if !errors.As(err, &classified) || classified.Code != Excluded || got.Text() != "" || reader.reads != 0 {
		t.Fatal("excluded pixel acquisition began after slow AX verification", err)
	}
}
