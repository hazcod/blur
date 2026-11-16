package tasks

import "testing"

func TestAuthoredTaskDoesNotReferenceAnEmptySource(t *testing.T) {
	if (Item{UserAuthored: true}).References("") {
		t.Fatal("empty provenance matched an empty source")
	}
}
