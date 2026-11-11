package client

import (
	"errors"
	"testing"

	"github.com/hazcod/blur/internal/knowledge"
)

func TestAppendKnowledgePageFollowsCursorAndPreservesBrowseState(t *testing.T) {
	first := knowledge.Page{Entities: []knowledge.Entity{{ID: "person-a"}}, Next: "person-a", HasMore: true}
	second := knowledge.Page{Entities: []knowledge.Entity{{ID: "project-b"}}, HasMore: false}
	got, err := appendKnowledgePage(first, second, "person-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entities) != 2 || got.Entities[0].ID != "person-a" || got.Entities[1].ID != "project-b" || got.HasMore || got.Next != "" {
		t.Fatalf("merged knowledge page = %+v", got)
	}
}

func TestAppendKnowledgePageRejectsStaleCursorDuplicatesAndNoProgress(t *testing.T) {
	current := knowledge.Page{Entities: []knowledge.Entity{{ID: "person-a"}}, Next: "person-a", HasMore: true}
	tests := []struct {
		name  string
		page  knowledge.Page
		after string
	}{
		{name: "stale cursor", page: knowledge.Page{Entities: []knowledge.Entity{{ID: "b"}}}, after: "wrong"},
		{name: "duplicate", page: knowledge.Page{Entities: []knowledge.Entity{{ID: "person-a"}}}, after: "person-a"},
		{name: "no cursor progress", page: knowledge.Page{Entities: []knowledge.Entity{{ID: "person-b"}}, Next: "person-a", HasMore: true}, after: "person-a"},
		{name: "empty continued page", page: knowledge.Page{Next: "person-b", HasMore: true}, after: "person-a"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := appendKnowledgePage(current, test.page, test.after); !errors.Is(err, errKnowledgePage) {
				t.Fatalf("append error=%v, want errKnowledgePage", err)
			}
		})
	}
}
