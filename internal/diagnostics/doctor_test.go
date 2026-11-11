package diagnostics

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/memory"
)

func TestDoctorContainsOnlyCountersAndClosedStates(t *testing.T) {
	const sentinel = "PERSONAL-SENTINEL-9f41"
	r := Build(Inputs{StoreVersion: 2, Integrity: Healthy, Queue: memory.Stats{Queued: 2, Rejected: 1}, StaleDerivations: 3, QuarantinedDerivations: 1, Today: Fresh, Capabilities: map[string]CapabilityState{"capture": Available, "ai": CapabilityState(sentinel)}})
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), sentinel) {
		t.Fatal("diagnostic disclosed private input")
	}
	if r.Queue.Queued != 2 || r.StaleDerivations != 3 || r.Integrity != Healthy {
		t.Fatalf("report=%+v", r)
	}
}

func TestDoctorNormalizesUnknownCapability(t *testing.T) {
	r := Build(Inputs{Capabilities: map[string]CapabilityState{"unsafe": "secret details"}})
	if r.Capabilities["unsafe"] != Unavailable {
		t.Fatalf("capability state=%q", r.Capabilities["unsafe"])
	}
}
