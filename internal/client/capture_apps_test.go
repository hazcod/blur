package client

import (
	"reflect"
	"testing"

	"github.com/hazcod/blur/internal/platform/darwin"
)

func TestDraftHelpers(t *testing.T) {
	d := draftWith("a.b\nc.d", "e.f")
	if d != "a.b\nc.d\ne.f" || draftWith(d, "C.D") != d {
		t.Fatalf("draftWith = %q", d)
	}
	if got := draftWithout(d, "c.d"); got != "a.b\ne.f" {
		t.Fatalf("draftWithout = %q", got)
	}
}

func TestAddableApps(t *testing.T) {
	running := []darwin.Application{
		{Name: "Zed", BundleID: "z"}, {Name: "Blur", BundleID: blurBundleID},
		{Name: "Mail", BundleID: "m"}, {Name: "Notes", BundleID: "n"}, {Name: "Arc", BundleID: "a"},
	}
	got := addableApps(running, func(id string) bool { return id == "n" }, 2)
	want := []darwin.Application{{Name: "Arc", BundleID: "a"}, {Name: "Mail", BundleID: "m"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}
