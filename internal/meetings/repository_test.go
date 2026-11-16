package meetings

import (
	"errors"
	"testing"
	"time"
)

func TestRepositoryPageBounds(t *testing.T) {
	p, err := (PageRequest{}).Normalized()
	if err != nil || p.Limit != MaxPageRows || p.MaxBytes != DefaultPageBytes {
		t.Fatal("page defaults changed")
	}
	for _, p := range []PageRequest{{Limit: -1}, {Limit: MaxPageRows + 1}, {MaxBytes: 255}, {MaxBytes: MaxPageBytes + 1}, {After: 1 << 48}} {
		if _, err := p.Normalized(); !errors.Is(err, ErrInvalid) {
			t.Fatal("unbounded page request admitted")
		}
	}
}
func TestRepositoryMetadataBounds(t *testing.T) {
	m := Meeting{ID: "fixture", Title: "Agenda", Locale: "en-US", StartedAt: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC), State: Ready, Tracks: []string{"microphone"}, Revision: 1}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Meeting){func(m *Meeting) { m.ID = "../fixture" }, func(m *Meeting) { m.Revision = 0 }, func(m *Meeting) { m.Tracks = []string{"microphone", "microphone"} }, func(m *Meeting) { m.State = Stopped }, func(m *Meeting) { m.EndedAt = m.StartedAt.Add(-time.Second) }} {
		invalid := m
		mutate(&invalid)
		if !errors.Is(invalid.Validate(), ErrInvalid) {
			t.Fatal("invalid metadata admitted")
		}
	}
}
