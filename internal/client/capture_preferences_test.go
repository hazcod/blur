package client

import (
	"reflect"
	"testing"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/preferences"
)

func TestCaptureExclusionsFromPreferencesCopiesBothLists(t *testing.T) {
	config := preferences.Config{
		ExcludedApps:    []string{"com.example.secret"},
		ExcludedDomains: []string{"private.test"},
	}
	got := captureExclusionsFromPreferences(config)
	wantApps := append(append([]string(nil), config.ExcludedApps...), appidentity.BundleID)
	if !reflect.DeepEqual(got.Apps, wantApps) || !reflect.DeepEqual(got.Domains, config.ExcludedDomains) {
		t.Fatalf("capture exclusions = %+v, want apps=%v domains=%v", got, wantApps, config.ExcludedDomains)
	}
	got.Apps[0] = "changed"
	got.Domains[0] = "changed"
	if config.ExcludedApps[0] != "com.example.secret" || config.ExcludedDomains[0] != "private.test" {
		t.Fatal("capture exclusion conversion aliases persisted preference slices")
	}
}

func TestCaptureExclusionsAlwaysBlockBlurIncludingExplicitAllowRules(t *testing.T) {
	got := captureExclusionsFromPreferences(preferences.Config{
		CaptureDefaultDisabled: true,
		IncludedApps:           []string{appidentity.BundleID},
	})
	if !reflect.DeepEqual(got.Apps, []string{appidentity.BundleID}) || !reflect.DeepEqual(got.IncludedApps, []string{appidentity.BundleID}) {
		t.Fatalf("Blur self-capture exclusion was lost: %+v", got)
	}
}
