package capture

import (
	"context"
	"testing"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/fake"
)

func TestRequiredFixtureScopeAdmitsOnlyIntersectionWithNormalPolicy(t *testing.T) {
	for _, test := range []struct {
		name, app, domain                                        string
		defaultDisabled                                          bool
		includedApps, includedDomains, deniedApps, deniedDomains []string
		want                                                     bool
	}{
		{name: "required app", app: "fixture.app", want: true},
		{name: "required domain", app: "com.google.chrome", domain: "blur-fixture.localhost", want: true},
		{name: "unrelated app", app: "personal.app", want: false},
		{name: "unrelated domain", app: "com.google.chrome", domain: "personal.example", want: false},
		{name: "ordinary deny app", app: "fixture.app", deniedApps: []string{"fixture.app"}, want: false},
		{name: "ordinary deny domain", app: "com.google.chrome", domain: "blur-fixture.localhost", deniedDomains: []string{"blur-fixture.localhost"}, want: false},
		{name: "ordinary scope required too", app: "fixture.app", defaultDisabled: true, want: false},
		{name: "ordinary scope and guard", app: "fixture.app", defaultDisabled: true, includedApps: []string{"fixture.app"}, want: true},
		{name: "ordinary domain scope and guard", app: "com.google.chrome", domain: "blur-fixture.localhost", defaultDisabled: true, includedDomains: []string{"blur-fixture.localhost"}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "fixture-window", AppID: test.app, Domain: test.domain}}, Limits: limits(),
				Exclusions: Exclusions{RequiredApps: []string{"fixture.app"}, RequiredDomains: []string{"blur-fixture.localhost"}, DefaultDisabled: test.defaultDisabled, IncludedApps: test.includedApps, IncludedDomains: test.includedDomains, Apps: test.deniedApps, Domains: test.deniedDomains},
				Read: func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
					return rawFrame{roots: []*rawNode{{declaredBytes: 7, value: func(context.Context, int) (string, error) { reads++; return "fixture", nil }}}}, nil
				},
			}
			observation, err := p.Capture(context.Background())
			if test.want {
				if err != nil || observation.Text() != "fixture" || reads != 1 {
					t.Fatal("eligible fixture refused", observation, reads, err)
				}
			} else if err == nil || reads != 0 {
				t.Fatal("ineligible source read", reads, err)
			}
		})
	}
}

func TestRequiredFixtureScopeValidationAndCopies(t *testing.T) {
	for _, invalid := range []Exclusions{{RequiredApps: []string{"invalid/app"}}, {RequiredDomains: []string{"https://private.example"}}} {
		if _, ok := NormalizeExclusions(invalid); ok {
			t.Fatal("invalid fixture guard admitted", invalid)
		}
	}
	original := Exclusions{RequiredApps: []string{"Fixture.App"}, RequiredDomains: []string{"BLUR-FIXTURE.LOCALHOST"}}
	cloned := cloneExclusions(original)
	normalized, ok := NormalizeExclusions(original)
	if !ok || normalized.RequiredApps[0] != "fixture.app" || normalized.RequiredDomains[0] != "blur-fixture.localhost" {
		t.Fatal("fixture guard not canonicalized", normalized)
	}
	cloned.RequiredApps[0], cloned.RequiredDomains[0] = "changed.app", "changed.localhost"
	normalized.RequiredApps[0], normalized.RequiredDomains[0] = "changed.app", "changed.localhost"
	if original.RequiredApps[0] != "Fixture.App" || original.RequiredDomains[0] != "BLUR-FIXTURE.LOCALHOST" {
		t.Fatal("fixture guard slices aliased")
	}
}
