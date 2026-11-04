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
	"github.com/nielsgroen/blur/internal/privacy"
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
	p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "t"}}, Limits: Limits{MaxBytes: 6, MaxNodes: 2, MaxDepth: 1, MaxDuration: time.Second}, Read: func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
		return rawFrame{roots: []*rawNode{mk("ab"), mk("cde")}}, nil
	}}
	obs, err := p.Capture(context.Background())
	if err != nil || obs.text != "ab\ncde" || obs.truncated || len(budgets) != 2 || budgets[0] != 6 || budgets[1] != 3 {
		t.Fatalf("obs=%+v budgets=%v err=%v", obs, budgets, err)
	}
	p.Limits.MaxBytes = 5
	budgets = nil
	obs, err = p.Capture(context.Background())
	if err == nil || obs.text != "" || len(budgets) != 2 || budgets[0] != 5 || budgets[1] != 2 {
		t.Fatalf("separator budget: obs=%+v budgets=%v err=%v", obs, budgets, err)
	}
	p.Limits.MaxBytes = 2
	budgets = nil
	p.Read = func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
		return rawFrame{roots: []*rawNode{mk("ab"), {declaredBytes: 0, value: func(context.Context, int) (string, error) { budgets = append(budgets, -1); return "", nil }}}}, nil
	}
	obs, err = p.Capture(context.Background())
	if err == nil || obs.text != "" || len(budgets) != 1 {
		t.Fatalf("separator with no room: obs=%+v budgets=%v err=%v", obs, budgets, err)
	}
	p.Limits.MaxBytes = 5
	budgets = nil
	p.Read = func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
		return rawFrame{roots: []*rawNode{{declaredBytes: 1, value: func(_ context.Context, n int) (string, error) { budgets = append(budgets, n); return "raw-secret", nil }}}}, nil
	}
	obs, err = p.Capture(context.Background())
	if err == nil || obs.text != "" || len(budgets) != 1 {
		t.Fatalf("oversize obs=%+v budgets=%v err=%v", obs, budgets, err)
	}
}

func TestExcludedCanonicalAppAndDomainNeverReachReader(t *testing.T) {
	for _, tc := range []struct {
		name       string
		probe      platform.CaptureProbe
		exclusions Exclusions
	}{
		{"domain terminal dot", platform.CaptureProbe{TargetID: "target-1", Domain: "BANK.TEST."}, Exclusions{Domains: []string{" bank.test. "}}},
		{"subdomain", platform.CaptureProbe{TargetID: "target-1", Domain: "login.bank.test."}, Exclusions{Domains: []string{"BANK.TEST."}}},
		{"app case and space", platform.CaptureProbe{TargetID: "target-1", AppID: "Org.Secret"}, Exclusions{Apps: []string{" org.secret "}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: tc.probe}, Read: reader("secret", &reads), Limits: limits(), Exclusions: tc.exclusions}
			_, err := p.Capture(context.Background())
			var typed *CaptureError
			if !errors.As(err, &typed) || typed.Code != Excluded || reads != 0 {
				t.Fatalf("exclusion: %v reads=%d", err, reads)
			}
		})
	}
}

func TestInvalidExclusionConfigurationFailsBeforeProbe(t *testing.T) {
	for _, tc := range []Exclusions{{Apps: []string{""}}, {Apps: []string{"org.example/secret"}}, {Domains: []string{"https://bank.test"}}, {Domains: []string{strings.Repeat("a", 254)}}} {
		src := &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "target-1"}}
		reads := 0
		p := Pipeline{Source: src, Read: reader("secret", &reads), Limits: limits(), Exclusions: tc}
		_, err := p.Capture(context.Background())
		var typed *CaptureError
		if !errors.As(err, &typed) || typed.Code != InvalidExclusions || reads != 0 || src.Calls != 0 {
			t.Fatalf("invalid exclusion: %v reads=%d probes=%d", err, reads, src.Calls)
		}
	}
}

func TestMalformedOpaqueTargetIDsNeverReachReader(t *testing.T) {
	for _, id := range []string{"", "has space", "path/part", "target?token=secret", "https://example.test", "line\nbreak", string([]byte{0xff}), strings.Repeat("x", 257)} {
		reads := 0
		p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: id}}, Read: reader("secret", &reads), Limits: limits()}
		_, err := p.Capture(context.Background())
		var typed *CaptureError
		if !errors.As(err, &typed) || typed.Code != InvalidProbe || reads != 0 {
			t.Fatalf("target ID %q: %v reads=%d", id, err, reads)
		}
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
	if strings.Contains(a.text, "hunter2") || a.appID != "org.example.editor" || a.domain != "example.test" || a.contentID == "" || a.redactionVersion == 0 {
		t.Fatalf("bad observation: %+v", a)
	}
	src.ProbeResult.TargetID = "target-b"
	b, err := p.Capture(context.Background())
	if err != nil || a.contentID != b.contentID {
		t.Fatalf("unstable ID: %v %+v %+v", err, a, b)
	}
	p.Read = reader("Note password: changed", &calls)
	c, err := p.Capture(context.Background())
	if err != nil || c.contentID != a.contentID {
		t.Fatalf("hash reveals redacted value: %v", err)
	}
	p.Read = reader("Different note password: changed", &calls)
	d, err := p.Capture(context.Background())
	if err != nil || d.contentID == a.contentID {
		t.Fatalf("hash ignores sanitized text: %v", err)
	}
	encoded, _ := json.Marshal(a)
	for _, raw := range []string{"hunter2", "target-a", "TargetID", "Secure", "Elevated", "Private"} {
		if strings.Contains(string(encoded), raw) {
			t.Fatalf("JSON leaks %s: %s", raw, encoded)
		}
	}
}

func TestSanitizedObservationValidationRejectsForgery(t *testing.T) {
	reads := 0
	p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "t", AppID: "org.example"}}, Read: reader("password: hunter2", &reads), Limits: limits()}
	valid, err := p.Capture(context.Background())
	if err != nil || valid.Validate() != nil {
		t.Fatalf("genuine observation rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*SanitizedObservation)
	}{
		{"raw labelled secret", func(o *SanitizedObservation) { o.text = "password: hunter2"; o.contentID = contentID(*o) }},
		{"mismatched id", func(o *SanitizedObservation) { o.contentID = strings.Repeat("a", 64) }},
		{"metadata mismatch", func(o *SanitizedObservation) { o.domain = "other.test" }},
		{"count mismatch", func(o *SanitizedObservation) { o.redactionCounts[0].Count = -1; o.contentID = contentID(*o) }},
		{"secret shaped app", func(o *SanitizedObservation) {
			o.appID = "sk-abcdefghijklmnopqrstuvwxyz123456"
			o.contentID = contentID(*o)
		}},
		{"invalid utf8", func(o *SanitizedObservation) { o.text = "\xff"; o.contentID = contentID(*o) }},
		{"oversized text", func(o *SanitizedObservation) {
			o.text = strings.Repeat("x", MaxObservationTextBytes+1)
			o.contentID = contentID(*o)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := valid
			o.redactionCounts = append([]privacy.Count(nil), o.redactionCounts...)
			tc.mutate(&o)
			err := o.Validate()
			if err == nil || strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("validation error=%v", err)
			}
		})
	}
}

func TestLiteralRedactedMarkersSurviveCapture(t *testing.T) {
	for _, literal := range []string{"[REDACTED:FAKE]", "unfinished [REDACTED", "the exact [REDACTED:SECRET] string"} {
		reads := 0
		p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "t"}}, Read: reader(literal, &reads), Limits: limits()}
		o, err := p.Capture(context.Background())
		if err != nil || o.text != literal || len(o.redactionCounts) != 0 {
			t.Fatalf("literal changed: err=%v", err)
		}
	}
}

func TestSanitizerOwnsCountsAndID(t *testing.T) {
	o, err := SanitizeObservation("example.app", "example.test", "password: hunter2", 256)
	if err != nil || o.Validate() != nil || len(o.redactionCounts) != 1 || o.redactionCounts[0].Count != 1 || o.contentID == "" || strings.Contains(o.text, "hunter2") {
		t.Fatalf("sanitizer result invalid: %v", err)
	}
}

func TestOversizedMetadataRejectedBeforeHashOrRedaction(t *testing.T) {
	for _, field := range []string{"app", "domain"} {
		o := SanitizedObservation{contentID: strings.Repeat("a", 64), redactionVersion: privacy.Version}
		if field == "app" {
			o.appID = strings.Repeat("x", 8<<20)
		} else {
			o.domain = strings.Repeat("x", 8<<20)
		}
		result := testing.Benchmark(func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if o.Validate() == nil {
					b.Fatal("oversized metadata accepted")
				}
			}
		})
		if result.NsPerOp() > 100000 {
			t.Fatalf("%s validation performed unbounded work: %d ns/op", field, result.NsPerOp())
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
	if err != nil || !obs.truncated || !strings.HasPrefix(obs.text, "🙂") || strings.Contains(obs.text, "�") || strings.Contains(obs.text, "password:x") {
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
	if err == nil || obs.text != "" {
		t.Fatalf("malformed value: %+v %v", obs, err)
	}
}
