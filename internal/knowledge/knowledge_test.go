package knowledge

import "testing"

func TestEntityRequiresEvidenceForEveryFactAndLoop(t *testing.T) {
	entity := Entity{ID: "p1", Kind: Project, Name: "Compiler", State: Confirmed,
		Facts: []Fact{{ID: "f1", Text: "private", State: Confirmed}}}
	if err := entity.Validate(); err == nil {
		t.Fatal("fact without evidence was accepted")
	}
	entity.Facts[0].Sources = []Source{{UserAuthored: true}}
	if err := entity.Validate(); err != nil {
		t.Fatalf("explicit user-authored fact rejected: %v", err)
	}
}

func TestEntityRejectsInvalidKindsStatesAndUnboundedFields(t *testing.T) {
	entity := Entity{ID: "p1", Kind: Kind("personality"), Name: "A", State: Confirmed}
	if err := entity.Validate(); err == nil {
		t.Fatal("unknown kind accepted")
	}
	entity.Kind = Person
	entity.State = State("guessed")
	if err := entity.Validate(); err == nil {
		t.Fatal("unknown state accepted")
	}
	entity.State = Confirmed
	entity.Name = string(make([]byte, MaxTextBytes+1))
	if err := entity.Validate(); err == nil {
		t.Fatal("oversized entity accepted")
	}
}

func TestNormalizeExactAliasIsStable(t *testing.T) {
	if got := NormalizeExact("  Project   CAFÉ "); got != "project café" {
		t.Fatalf("NormalizeExact()=%q", got)
	}
}
