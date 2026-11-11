package platform_test

import (
	"encoding/json"
	"testing"

	"github.com/hazcod/blur/internal/platform"
)

func TestCapabilityStatesValidateAndJSON(t *testing.T) {
	states := []platform.CapabilityState{
		"available", "permission_required", "unsupported_target", "secure_target",
		"target_elevated", "portal_missing", "temporarily_unresponsive", "user_disabled",
	}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			if !state.Valid() {
				t.Fatalf("known state %q rejected", state)
			}
			report := platform.CapabilityReport{ID: "capture", State: state, Reason: "probe", Evidence: "adapter probe"}
			if err := report.Validate(); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			want := `{"id":"capture","state":"` + string(state) + `","reason":"probe","evidence":"adapter probe"}`
			if string(got) != want {
				t.Fatalf("JSON = %s, want %s", got, want)
			}
		})
	}
	got := platform.AllCapabilityStates()
	if len(got) != len(states) {
		t.Fatalf("state count = %d", len(got))
	}
	for i, state := range states {
		if got[i] != state {
			t.Fatalf("state[%d] = %q", i, got[i])
		}
	}
	got[0] = "corrupted"
	if platform.AllCapabilityStates()[0] != "available" {
		t.Fatal("caller mutated state vocabulary")
	}
}

func TestCapabilityReportRejectsInvalidFields(t *testing.T) {
	base := platform.CapabilityReport{ID: "capture", State: "available", Reason: "ready", Evidence: "adapter probe"}
	for name, mutate := range map[string]func(*platform.CapabilityReport){
		"id":                 func(r *platform.CapabilityReport) { r.ID = "" },
		"state":              func(r *platform.CapabilityReport) { r.State = "new_state" },
		"reason":             func(r *platform.CapabilityReport) { r.Reason = "" },
		"reason with spaces": func(r *platform.CapabilityReport) { r.Reason = "permission denied" },
		"id with whitespace": func(r *platform.CapabilityReport) { r.ID = "capture target" },
		"evidence":           func(r *platform.CapabilityReport) { r.Evidence = "" },
	} {
		t.Run(name, func(t *testing.T) {
			r := base
			mutate(&r)
			if err := r.Validate(); err == nil {
				t.Fatal("invalid report accepted")
			}
		})
	}
}
