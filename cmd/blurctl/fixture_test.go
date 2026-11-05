package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFixtureCommandRequiresExplicitStoreAndRejectsKeyArguments(t *testing.T) {
	for _, args := range [][]string{{"fixture", "doctor"}, {"fixture", "doctor", "--fixture", "--store", "x", "--key", "secret"}, {"fixture", "open", "--fixture", "--store", "x"}, {"fixture", "today", "--fixture", "--store", ""}, {"fixture", "today", "--store", filepath.Join(os.TempDir(), "blur-fixture-demo.db")}} {
		if _, _, ok := parseFixture(args); ok {
			t.Fatalf("accepted unsafe fixture args %q", args)
		}
	}
	pathWant := filepath.Join(os.TempDir(), "blur-fixture-demo.db")
	cmd, path, ok := parseFixture([]string{"fixture", "today", "--fixture", "--store", pathWant})
	if !ok || cmd != "today" || path != pathWant {
		t.Fatalf("got %q %q %v", cmd, path, ok)
	}
}
