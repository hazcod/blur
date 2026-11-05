package today

import (
	"testing"
	"time"
)

func TestBuildGroupsDeterministicallyAndOnlyUsesExplicitTodoMarkers(t *testing.T) {
	zone := time.FixedZone("local", 2*60*60)
	generated := time.Date(2026, 11, 5, 17, 0, 0, 0, zone)
	input := Input{
		Date: "2026-11-05", Zone: "Europe/Brussels", GeneratedAt: generated, GenerationID: "gen-1",
		Evidence: []Evidence{
			{SourceID: "src-b", At: generated.Add(-time.Hour), SubjectID: "project-x", Subject: "Project X", Change: "Updated design", Todo: "Ship the draft", TodoMarker: MarkerTODO, OpenLoop: "Review with Sam"},
			{SourceID: "src-a", At: generated.Add(-2 * time.Hour), SubjectID: "project-x", Subject: "Project X", Change: "Added tests", Todo: "Maybe refactor", TodoMarker: MarkerNone},
			{SourceID: "src-c", At: generated.Add(-30 * time.Minute), SubjectID: "project-y", Subject: "Project Y", Todo: "Call legal", TodoMarker: MarkerUserCreated, TodoConfirmed: true},
		},
		TimeSplit: []TimeSplit{{Key: "slack", Label: "Slack", Duration: time.Hour, SourceIDs: []string{"src-a", "src-b"}}},
	}
	got, err := Build(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Date != input.Date || got.Zone != input.Zone || !got.GeneratedAt.Equal(generated) || len(got.ActiveSubjects) != 2 {
		t.Fatalf("snapshot header/subjects = %#v", got)
	}
	if got.ActiveSubjects[0].ID != "project-y" || got.ActiveSubjects[1].ID != "project-x" || len(got.ActiveSubjects[1].SourceIDs) != 2 {
		t.Fatalf("grouped subjects = %#v", got.ActiveSubjects)
	}
	if len(got.TodoCandidates) != 1 || got.TodoCandidates[0].Text != "Ship the draft" || len(got.ConfirmedTodos) != 1 {
		t.Fatalf("todo distinction = candidates %#v confirmed %#v", got.TodoCandidates, got.ConfirmedTodos)
	}
	if len(got.OpenLoops) != 1 || len(got.RecentChanges) != 2 || len(got.SourceIDs) != 3 {
		t.Fatalf("snapshot contents = %#v", got)
	}
	input.Evidence[0], input.Evidence[1] = input.Evidence[1], input.Evidence[0]
	again, err := Build(input)
	if err != nil {
		t.Fatal(err)
	}
	if !Equal(got, again) {
		t.Fatalf("input order changed snapshot:\n%#v\n%#v", got, again)
	}
}

func TestBuildRejectsInvalidTimezoneAndBoundsOutput(t *testing.T) {
	base := Input{Date: "2026-11-05", Zone: "Europe/Brussels", GeneratedAt: time.Now(), GenerationID: "g"}
	bad := base
	bad.Zone = "../../etc/passwd"
	if _, err := Build(bad); err == nil {
		t.Fatal("invalid zone accepted")
	}
	tooMany := base
	for i := 0; i < MaxEvidence+1; i++ {
		tooMany.Evidence = append(tooMany.Evidence, Evidence{SourceID: "s", At: base.GeneratedAt})
	}
	if _, err := Build(tooMany); err == nil {
		t.Fatal("unbounded evidence accepted")
	}
}

func TestBuildUsesLocalDateAcrossDSTBoundary(t *testing.T) {
	zone, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	generated := time.Date(2026, 10, 25, 23, 30, 0, 0, zone)
	previousDay := time.Date(2026, 10, 24, 21, 59, 0, 0, time.UTC)
	lateSameDay := time.Date(2026, 10, 25, 22, 15, 0, 0, time.UTC)
	s, err := Build(Input{Date: "2026-10-25", Zone: "Europe/Brussels", GeneratedAt: generated, GenerationID: "g", Evidence: []Evidence{{SourceID: "before", At: previousDay, Change: "Previous local day"}, {SourceID: "during", At: lateSameDay, Change: "After clock change"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.RecentChanges) != 1 || s.RecentChanges[0].SourceIDs[0] != "during" || s.Completeness.EvidenceOmitted != 1 {
		t.Fatalf("DST local-date filter %#v", s)
	}
}

func TestBuildDuplicateSourceAndConflictingLabelsAreOrderIndependent(t *testing.T) {
	at := time.Date(2026, 11, 5, 16, 0, 0, 0, time.UTC)
	a := Evidence{SourceID: "src", At: at, SubjectID: "project", Subject: "Z project", Change: "Earlier value"}
	b := Evidence{SourceID: "src", At: at.Add(time.Minute), SubjectID: "project", Subject: "A project", Change: "Latest value"}
	in := Input{Date: "2026-11-05", Zone: "UTC", GeneratedAt: at.Add(time.Hour), GenerationID: "g", Evidence: []Evidence{a, b}}
	first, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	in.Evidence = []Evidence{b, a}
	second, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if !Equal(first, second) || first.ActiveSubjects[0].Label != "A project" || first.RecentChanges[0].Text != "Latest value" || first.Completeness.DuplicateEvidence != 1 {
		t.Fatalf("noncanonical duplicate result: %#v / %#v", first, second)
	}
}

func TestSnapshotCloneDoesNotShareMutableSlices(t *testing.T) {
	in := Input{Date: "2026-11-05", Zone: "UTC", GeneratedAt: time.Date(2026, 11, 5, 17, 0, 0, 0, time.UTC), GenerationID: "g-1", Evidence: []Evidence{{SourceID: "src-1", At: time.Date(2026, 11, 5, 16, 0, 0, 0, time.UTC), SubjectID: "p", Subject: "Project"}}}
	s, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	copy := s.Clone()
	copy.ActiveSubjects[0].SourceIDs[0] = "changed"
	if s.ActiveSubjects[0].SourceIDs[0] != "src-1" {
		t.Fatal("Clone shared subject source IDs")
	}
}

func TestMaterializerIncrementalLateDuplicateDeleteAndTodoDisposition(t *testing.T) {
	day := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	m, err := NewMaterializer("2026-11-05", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	early := Evidence{SourceID: "src-1", At: day.Add(-time.Hour), SubjectID: "p", Subject: "Project", Todo: "Draft release", TodoMarker: MarkerTODO}
	if err := m.Upsert(early); err != nil {
		t.Fatal(err)
	}
	if err := m.Upsert(early); err != nil {
		t.Fatalf("duplicate sighting: %v", err)
	}
	late := Evidence{SourceID: "src-2", At: day.Add(time.Hour), SubjectID: "p", Subject: "Project", Todo: "Draft release v2", TodoMarker: MarkerUserCreated, TodoConfirmed: true}
	if err := m.Upsert(late); err != nil {
		t.Fatal(err)
	}
	s, err := m.Snapshot(day.Add(2*time.Hour), "g1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveSubjects[0].FirstSeen != early.At || s.ActiveSubjects[0].LastSeen != late.At || s.Completeness.DuplicateEvidence != 1 || len(s.ConfirmedTodos) != 1 || len(s.TodoCandidates) != 1 {
		t.Fatalf("incremental snapshot %#v", s)
	}
	late.Todo = "Corrected task"
	late.TodoConfirmed = false
	late.TodoDismissed = true
	if err := m.Upsert(late); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteSource("src-1"); err != nil {
		t.Fatal(err)
	}
	s, err = m.Snapshot(day.Add(3*time.Hour), "g2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ActiveSubjects) != 1 || s.ActiveSubjects[0].FirstSeen != late.At || len(s.ConfirmedTodos) != 0 || len(s.TodoCandidates) != 0 || len(s.SourceIDs) != 1 || s.SourceIDs[0] != "src-2" {
		t.Fatalf("corrected/deleted snapshot %#v", s)
	}
}
