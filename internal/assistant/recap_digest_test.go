package assistant

import (
	"strings"
	"testing"
)

func TestDerivedEvidenceDigestBindsCoverageAndSupportingSources(t *testing.T) {
	base := Evidence{SourceID: strings.Repeat("a", 64), Text: "Reviewed the release.", SourceIDs: []string{strings.Repeat("b", 64)}, Layer: LayerDay}
	want := EvidenceDigests([]Evidence{base})[base.SourceID]
	for _, change := range []string{"coverage", "sources"} {
		next := base
		if change == "coverage" {
			next.CoverageLimited = true
		} else {
			next.SourceIDs = []string{strings.Repeat("c", 64)}
		}
		if EvidenceDigests([]Evidence{next})[base.SourceID] == want {
			t.Fatalf("changed %s did not invalidate previous derived context", change)
		}
	}
}
