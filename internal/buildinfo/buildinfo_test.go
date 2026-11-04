package buildinfo

import "testing"

func TestUserAgentUsesSafeDevelopmentDefault(t *testing.T) {
	if got, want := UserAgent(), "Blur/dev"; got != want {
		t.Fatalf("UserAgent() = %q, want %q", got, want)
	}
}

func TestUserAgentIsDeterministicFromBuildVersion(t *testing.T) {
	old := Version
	Version = "1.2.3"
	t.Cleanup(func() { Version = old })

	for range 2 {
		if got, want := UserAgent(), "Blur/1.2.3"; got != want {
			t.Fatalf("UserAgent() = %q, want %q", got, want)
		}
	}
}
