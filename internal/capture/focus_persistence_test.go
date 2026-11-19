package capture

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestObservationWithoutBreadcrumbKeepsVersionOneEncoding(t *testing.T) {
	o, err := SanitizeObservation("example.app", "example.com", "plain text", 128)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := MarshalAuthenticatedObservation(o)
	if err != nil || encoded[0] != 1 {
		t.Fatalf("version = %v err=%v; records without a breadcrumb must stay byte-identical", encoded[0], err)
	}
	restored, err := RestoreAuthenticatedObservation(encoded)
	if err != nil || restored.FocusedRole() != "" || restored.FocusedName() != "" || restored.FocusedPath() != "" {
		t.Fatalf("old record: %v %+v", err, restored)
	}
}

func TestObservationBreadcrumbRoundTripsAdditively(t *testing.T) {
	o, err := SanitizeObservation("example.app", "example.com", "plain text", 128)
	if err != nil {
		t.Fatal(err)
	}
	withFocus := o.WithFocus("AXTextField", "Subject é", "Compose > Header")
	if err := withFocus.Validate(); err != nil {
		t.Fatal(err)
	}
	if withFocus.ContentID() != o.ContentID() {
		t.Fatal("focus fields must not enter the content ID")
	}
	encoded, err := MarshalAuthenticatedObservation(withFocus)
	if err != nil || encoded[0] != 2 {
		t.Fatalf("version=%v err=%v", encoded[0], err)
	}
	restored, err := RestoreAuthenticatedObservation(encoded)
	if err != nil || !reflect.DeepEqual(restored, withFocus) {
		t.Fatalf("round trip: %v", err)
	}
	for end := 0; end < len(encoded); end++ {
		if _, err := RestoreAuthenticatedObservation(encoded[:end]); err == nil {
			t.Fatalf("truncated record of %d bytes decoded", end)
		}
	}
	if _, err := RestoreAuthenticatedObservation(append(bytes.Clone(encoded), 0)); err == nil {
		t.Fatal("trailing byte decoded")
	}
	// A version-2 record whose breadcrumb is empty is not canonical.
	v1, err := MarshalAuthenticatedObservation(o)
	if err != nil {
		t.Fatal(err)
	}
	empty := append(bytes.Clone(v1), 0, 0, 0)
	empty[0] = 2
	if _, err := RestoreAuthenticatedObservation(empty); err == nil {
		t.Fatal("empty version-2 trailer decoded")
	}
}

func TestObservationValidateRejectsOversizedOrUnredactedBreadcrumb(t *testing.T) {
	o, err := SanitizeObservation("example.app", "", "plain text", 128)
	if err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]SanitizedObservation{
		"long name":   o.withFocusRaw("AXTextField", strings.Repeat("x", MaxFocusFieldBytes+1), ""),
		"long path":   o.withFocusRaw("AXTextField", "", strings.Repeat("x", MaxFocusFieldBytes+1)),
		"bad role":    o.withFocusRaw("not a role", "", ""),
		"secret name": o.withFocusRaw("AXTextField", "password: hunter2hunter2", ""),
		"nul":         o.withFocusRaw("AXTextField", "a\x00b", ""),
	} {
		if bad.Validate() == nil {
			t.Fatalf("%s accepted", name)
		}
		if _, err := MarshalAuthenticatedObservation(bad); err == nil {
			t.Fatalf("%s marshaled", name)
		}
	}
}
