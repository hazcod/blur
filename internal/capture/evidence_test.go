package capture

import (
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/privacy"
)

func TestEvidencePreservesLegacyIdentityWithoutAliasing(t *testing.T) {
	o, err := SanitizeObservation("terminal.app", "", "Synthetic rollback owner is Mira.", 1024)
	if err != nil {
		t.Fatal(err)
	}
	e, err := ObservationEvidence(o)
	if err != nil || e.Validate() != nil || e.SourceID() != o.ContentID() || e.Text() != o.Text() || e.RedactionVersion() != privacy.Version {
		t.Fatalf("legacy evidence=%v err=%v", e, err)
	}
	if _, ok := e.Provenance(); ok {
		t.Fatal("legacy observation acquired invented multipart provenance")
	}
}

func TestCaptureEvidenceUsesCanonicalPartIdentityAndOriginalSpans(t *testing.T) {
	parts, summary := buildCapture(t, strings.Repeat("Ordered 日本語🙂 technical source.\n", 3000)+"Mira owns the rollback.", privacy.MaxStreamInputBytes)
	publication := strings.Repeat("a", 64)
	for _, part := range parts {
		e, err := CapturePartEvidence(summary, part, publication)
		if err != nil || e.Validate() != nil || e.SourceID() != CapturePartSourceID(summary.ContentID(), publication, part.Index()) || e.Text() != part.Text() || e.AppID() != summary.Header().AppID() || e.RedactionVersion() != privacy.StreamVersion {
			t.Fatalf("capture evidence rejected: %v", err)
		}
		p, ok := e.Provenance()
		if !ok || p.EditionID != summary.ContentID() || p.PartIndex != part.Index() || p.Start != part.Offset() || p.End != part.Offset()+int64(len(part.Text())) {
			t.Fatalf("missing canonical provenance: %+v", p)
		}
		if e.SourceID() == summary.ContentID() {
			t.Fatal("part evidence forged edition identity")
		}
		origins := e.Origins()
		origins[0].InputStart = 99
		if e.Origins()[0].InputStart == 99 {
			t.Fatal("provenance slices alias caller")
		}
	}
}

func TestCaptureEvidenceRejectsUnrelatedPartsAndInvalidPublication(t *testing.T) {
	parts, a := buildCapture(t, "First independent synthetic source.", 4096)
	_, b := buildCapture(t, "Other independent synthetic source.", 4096)
	for _, publication := range []string{"", strings.Repeat("g", 64), strings.Repeat("a", 63)} {
		if _, err := CapturePartEvidence(a, parts[0], publication); err == nil {
			t.Fatal("invalid publication accepted")
		}
	}
	if _, err := CapturePartEvidence(b, parts[0], strings.Repeat("a", 64)); err == nil {
		t.Fatal("unrelated source part accepted")
	}
	if (Evidence{}).Validate() == nil {
		t.Fatal("empty generic evidence accepted")
	}
}
