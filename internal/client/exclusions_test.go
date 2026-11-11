package client

import (
	"reflect"
	"testing"
)

func TestParseCaptureExclusionDrafts(t *testing.T) {
	got, ok := parseCaptureExclusionDrafts("  COM.Example.Secret  \n\ncom.example.other\n", " Private.Test.\nsub.private.test ")
	if !ok {
		t.Fatal("valid exclusion list rejected")
	}
	wantApps := []string{"com.example.secret", "com.example.other"}
	wantDomains := []string{"private.test", "sub.private.test"}
	if !reflect.DeepEqual(got.Apps, wantApps) || !reflect.DeepEqual(got.Domains, wantDomains) {
		t.Fatalf("parsed exclusions = %+v, want apps=%v domains=%v", got, wantApps, wantDomains)
	}
}

func TestParseCaptureExclusionDraftsRejectsUnsafeOrOversizedEntries(t *testing.T) {
	for _, tc := range []struct {
		name, apps, domains string
	}{
		{name: "app path", apps: "com.example/private"},
		{name: "domain URL", domains: "https://private.test"},
		{name: "domain path", domains: "private.test/path"},
		{name: "too many apps", apps: stringsRepeatLines("com.example.app", maxCaptureExclusionEntries+1)},
		{name: "too many domains", domains: stringsRepeatLines("private.test", maxCaptureExclusionEntries+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := parseCaptureExclusionDrafts(tc.apps, tc.domains); ok {
				t.Fatal("invalid exclusion list accepted")
			}
		})
	}
}

func stringsRepeatLines(value string, count int) string {
	result := value
	for i := 1; i < count; i++ {
		result += "\n" + value
	}
	return result
}
