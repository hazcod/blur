package client

import (
	"reflect"
	"testing"

	"github.com/hazcod/blur/internal/preferences"
)

func TestCaptureExclusionsFromPreferencesCopiesBothLists(t *testing.T) {
	config := preferences.Config{
		ExcludedApps:    []string{"com.example.secret"},
		ExcludedDomains: []string{"private.test"},
	}
	got := captureExclusionsFromPreferences(config)
	if !reflect.DeepEqual(got.Apps, config.ExcludedApps) || !reflect.DeepEqual(got.Domains, config.ExcludedDomains) {
		t.Fatalf("capture exclusions = %+v, want apps=%v domains=%v", got, config.ExcludedApps, config.ExcludedDomains)
	}
	got.Apps[0] = "changed"
	got.Domains[0] = "changed"
	if config.ExcludedApps[0] != "com.example.secret" || config.ExcludedDomains[0] != "private.test" {
		t.Fatal("capture exclusion conversion aliases persisted preference slices")
	}
}
