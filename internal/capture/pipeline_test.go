package capture

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nielsgroen/blur/internal/platform"
	"github.com/nielsgroen/blur/internal/platform/fake"
)

func available() platform.CapabilityReport {
	return platform.CapabilityReport{State: platform.Available, Reason: "ready", Evidence: "fixture"}
}
func limits() Limits {
	return Limits{MaxBytes: 256, MaxNodes: 3, MaxDepth: 2, MaxDuration: time.Second}
}
func reader(text string, calls *int) readBounded {
	return func(_ context.Context, _ platform.CaptureProbe, _ Limits) (rawFrame, error) {
		*calls++
		return rawFrame{roots: []*rawNode{{declaredBytes: len(text), value: func(_ context.Context, _ int) (string, error) { return text, nil }}}}, nil
	}
}

func TestDenialsDoNotInvokeReader(t *testing.T) {
	for _, tc := range []struct {
		name       string
		report     platform.CapabilityReport
		probe      platform.CaptureProbe
		ctx        context.Context
		lim        Limits
		exclusions Exclusions
	}{
		{"unavailable", platform.CapabilityReport{State: platform.PermissionRequired, Reason: "denied", Evidence: "fixture"}, platform.CaptureProbe{}, context.Background(), limits(), Exclusions{}},
		{"invalid report", platform.CapabilityReport{State: platform.Available}, platform.CaptureProbe{}, context.Background(), limits(), Exclusions{}},
		{"secure", available(), platform.CaptureProbe{TargetID: "t", Secure: true}, context.Background(), limits(), Exclusions{}},
		{"elevated", available(), platform.CaptureProbe{TargetID: "t", Elevated: true}, context.Background(), limits(), Exclusions{}},
		{"private", available(), platform.CaptureProbe{TargetID: "t", Private: true}, context.Background(), limits(), Exclusions{}},
		{"app", available(), platform.CaptureProbe{TargetID: "t", AppID: "org.secret"}, context.Background(), limits(), Exclusions{Apps: []string{"org.secret"}}},
		{"domain", available(), platform.CaptureProbe{TargetID: "t", Domain: "bank.test"}, context.Background(), limits(), Exclusions{Domains: []string{"bank.test"}}},
		{"bad limits", available(), platform.CaptureProbe{TargetID: "t"}, context.Background(), Limits{}, Exclusions{}},
		{"bad probe", available(), platform.CaptureProbe{TargetID: "t", AppID: strings.Repeat("x", 300)}, context.Background(), limits(), Exclusions{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			src := &fake.CaptureSource{Report: tc.report, ProbeResult: tc.probe}
			p := Pipeline{Source: src, Read: reader("secret", &calls), Limits: tc.lim, Exclusions: tc.exclusions}
			_, err := p.Capture(tc.ctx)
			if err == nil || calls != 0 {
				t.Fatalf("err=%v reader calls=%d", err, calls)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "bank.test") {
				t.Fatalf("leaking error: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "t"}}, Read: reader("secret", &calls), Limits: limits()}
	_, err := p.Capture(ctx)
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("cancel: %v calls=%d", err, calls)
	}
}

func TestDeniedProbeDoesNotInvokeReader(t *testing.T) {
	reads := 0
	p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeError: errors.New("raw-secret")}, Read: reader("secret", &reads), Limits: limits()}
	_, err := p.Capture(context.Background())
	var typed *CaptureError
	if !errors.As(err, &typed) || typed.Code != ProbeFailed || reads != 0 || strings.Contains(err.Error(), "raw-secret") {
		t.Fatalf("denied probe: %v reads=%d", err, reads)
	}
}

func TestStructuralOverflowNeverReadsValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		root *rawNode
	}{
		{"nodes", &rawNode{children: []*rawNode{{}, {}, {}}}},
		{"depth", &rawNode{children: []*rawNode{{children: []*rawNode{{children: []*rawNode{{}}}}}}}},
		{"declared bytes", &rawNode{declaredBytes: 257}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := 0
			tc.root.value = func(context.Context, int) (string, error) { values++; return "secret", nil }
			p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "t"}}, Limits: limits(), Read: func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
				return rawFrame{roots: []*rawNode{tc.root}}, nil
			}}
			_, err := p.Capture(context.Background())
			if err == nil || values != 0 {
				t.Fatalf("err=%v values=%d", err, values)
			}
		})
	}
}

func TestRemainingByteBudgetAndOversizeFailure(t *testing.T) {
	budgets := []int{}
	mk := func(s string) *rawNode {
		return &rawNode{declaredBytes: len(s), value: func(_ context.Context, n int) (string, error) { budgets = append(budgets, n); return s, nil }}
	}
	p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "t"}}, Limits: Limits{MaxBytes: 5, MaxNodes: 2, MaxDepth: 1, MaxDuration: time.Second}, Read: func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
		return rawFrame{roots: []*rawNode{mk("ab"), mk("cde")}}, nil
	}}
	obs, err := p.Capture(context.Background())
	if err != nil || obs.Text != "ab\ncd" || !obs.Truncated || len(budgets) != 2 || budgets[0] != 5 || budgets[1] != 3 {
		t.Fatalf("obs=%+v budgets=%v err=%v", obs, budgets, err)
	}
	budgets = nil
	p.Read = func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
		return rawFrame{roots: []*rawNode{{declaredBytes: 1, value: func(_ context.Context, n int) (string, error) { budgets = append(budgets, n); return "raw-secret", nil }}}}, nil
	}
	obs, err = p.Capture(context.Background())
	if err == nil || obs.Text != "" || len(budgets) != 1 {
		t.Fatalf("oversize obs=%+v budgets=%v err=%v", obs, budgets, err)
	}
}

func TestSanitizedObservationAndStableHash(t *testing.T) {
	calls := 0
	src := &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "target-a", AppID: "  ORG.EXAMPLE.Editor  ", Domain: " Example.TEST "}}
	p := Pipeline{Source: src, Read: reader("Note password: hunter2", &calls), Limits: limits()}
	a, err := p.Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(a.Text, "hunter2") || a.AppID != "org.example.editor" || a.Domain != "example.test" || a.ContentID == "" || a.RedactionVersion == 0 {
		t.Fatalf("bad observation: %+v", a)
	}
	src.ProbeResult.TargetID = "target-b"
	b, err := p.Capture(context.Background())
	if err != nil || a.ContentID != b.ContentID {
		t.Fatalf("unstable ID: %v %+v %+v", err, a, b)
	}
	p.Read = reader("Note password: changed", &calls)
	c, err := p.Capture(context.Background())
	if err != nil || c.ContentID != a.ContentID {
		t.Fatalf("hash reveals redacted value: %v", err)
	}
	p.Read = reader("Different note password: changed", &calls)
	d, err := p.Capture(context.Background())
	if err != nil || d.ContentID == a.ContentID {
		t.Fatalf("hash ignores sanitized text: %v", err)
	}
	encoded, _ := json.Marshal(a)
	for _, raw := range []string{"hunter2", "target-a", "TargetID", "Secure", "Elevated", "Private"} {
		if strings.Contains(string(encoded), raw) {
			t.Fatalf("JSON leaks %s: %s", raw, encoded)
		}
	}
}

func TestUTF8SafeOutputTruncation(t *testing.T) {
	text, truncated := truncateUTF8("🙂🙂", 6)
	if !truncated || text != "🙂" {
		t.Fatalf("UTF-8 boundary: %q %t", text, truncated)
	}
	calls := 0
	p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "t"}}, Read: reader("🙂 password:x", &calls), Limits: Limits{MaxBytes: 15, MaxNodes: 1, MaxDepth: 1, MaxDuration: time.Second}}
	obs, err := p.Capture(context.Background())
	if err != nil || !obs.Truncated || !strings.HasPrefix(obs.Text, "🙂") || strings.Contains(obs.Text, "�") || strings.Contains(obs.Text, "password:x") {
		t.Fatalf("truncation: %+v %v", obs, err)
	}
}

func TestDeadlineStopsBeforeValueAndErrorsStayContentFree(t *testing.T) {
	values := 0
	p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "target-secret"}}, Limits: Limits{MaxBytes: 20, MaxNodes: 1, MaxDepth: 1, MaxDuration: time.Millisecond}, Read: func(ctx context.Context, _ platform.CaptureProbe, _ Limits) (rawFrame, error) {
		<-ctx.Done()
		return rawFrame{roots: []*rawNode{{declaredBytes: 6, value: func(context.Context, int) (string, error) { values++; return "secret", nil }}}}, nil
	}}
	_, err := p.Capture(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || values != 0 || strings.Contains(err.Error(), "secret") {
		t.Fatalf("deadline: %v values=%d", err, values)
	}
	p.Limits.MaxDuration = time.Second
	p.Read = func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
		return rawFrame{}, errors.New("raw-secret")
	}
	_, err = p.Capture(context.Background())
	var typed *CaptureError
	if !errors.As(err, &typed) || typed.Code != ReadFailed || strings.Contains(err.Error(), "raw-secret") {
		t.Fatalf("unsafe read error: %v", err)
	}
}

func TestRawFrameCannotSerializeContent(t *testing.T) {
	encoded, err := json.Marshal(rawFrame{roots: []*rawNode{{declaredBytes: 6, value: func(context.Context, int) (string, error) { return "secret", nil }}}})
	if err != nil || string(encoded) != "{}" {
		t.Fatalf("raw frame JSON: %q %v", encoded, err)
	}
}

func TestMalformedMetadataAndValueFailClosed(t *testing.T) {
	for _, probe := range []platform.CaptureProbe{
		{TargetID: "t", AppID: "editor?password=secret"},
		{TargetID: "t", Domain: "example.test/path"},
		{TargetID: "t", AppID: string([]byte{0xff})},
	} {
		reads := 0
		p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: probe}, Read: reader("secret", &reads), Limits: limits()}
		_, err := p.Capture(context.Background())
		if err == nil || reads != 0 {
			t.Fatalf("malformed probe: %+v err=%v reads=%d", probe, err, reads)
		}
	}
	p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "t"}}, Limits: limits(), Read: func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
		return rawFrame{roots: []*rawNode{{declaredBytes: 1, value: func(context.Context, int) (string, error) { return string([]byte{0xff}), nil }}}}, nil
	}}
	obs, err := p.Capture(context.Background())
	if err == nil || obs.Text != "" {
		t.Fatalf("malformed value: %+v %v", obs, err)
	}
}
