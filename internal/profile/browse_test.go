package profile

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type browseRepository struct {
	Repository
	records []Record
	errAt   int
}

func (*browseRepository) EncryptedAtRest() bool { return true }
func (r *browseRepository) ListProfile(ctx context.Context, request PageRequest) (Page, error) {
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	start := 0
	if request.After != "" {
		for n, record := range r.records {
			if record.ID == request.After {
				start = n + 1
				break
			}
		}
	}
	if r.errAt > 0 && start >= r.errAt {
		return Page{}, errors.New("encrypted storage unavailable")
	}
	end := min(start+request.Limit, len(r.records))
	page := Page{Records: r.records[start:end], HasMore: end < len(r.records)}
	if page.HasMore {
		page.Next = r.records[end-1].ID
	}
	return page, nil
}

func browseFixture(n int) Record {
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	return Record{ID: fmt.Sprintf("%032x", n+1), Revision: 1, Kind: Fact, Text: "A saved preference.", Enabled: true, UserAuthored: true, CreatedAt: at, UpdatedAt: at}
}

func TestBrowseFindsMatchesBeyondTheFirstStoredPage(t *testing.T) {
	repository := &browseRepository{}
	for n := 0; n < 70; n++ {
		record := browseFixture(n)
		if n >= 40 {
			record.Text = "My preferred working language is Dutch."
		}
		repository.records = append(repository.records, record)
	}
	page, err := Browse(context.Background(), repository, BrowseRequest{Query: "DUTCH", Limit: 20})
	if err != nil || page.Total != 30 || page.TotalRecords != 70 || len(page.Records) != 20 || page.Records[0].ID != fmt.Sprintf("%032x", 41) {
		t.Fatalf("first filtered page = %+v, %v", page, err)
	}
	next, err := Browse(context.Background(), repository, BrowseRequest{Query: "Dutch", Offset: 20, Limit: 20})
	if err != nil || next.Total != 30 || next.Offset != 20 || len(next.Records) != 10 || next.Records[0].ID != fmt.Sprintf("%032x", 61) {
		t.Fatalf("second filtered page = %+v, %v", next, err)
	}
}

func TestBrowseSearchesEveryScopeAndCombinesKindAndStateFilters(t *testing.T) {
	repository := &browseRepository{}
	for n := 0; n < 5; n++ {
		record := browseFixture(n)
		record.Kind, record.Enabled = WritingRule, false
		switch n {
		case 0:
			record.Text = "Use the team's preferred tone."
		case 1:
			record.Scope.AppID = "com.example.editor"
		case 2:
			record.Scope.Domain = "example.work"
		case 3:
			record.Scope.Project = "Atlas"
		case 4:
			record.Scope.Audience = "Executive team"
		}
		repository.records = append(repository.records, record)
	}
	active := browseFixture(10)
	active.Text = "Atlas is my preferred project."
	repository.records = append(repository.records, active)
	for _, test := range []struct {
		query string
		id    int
	}{{"PREFERRED TONE", 1}, {"com.example.editor", 2}, {"example.work", 3}, {"ATLAS", 4}, {"executive team", 5}} {
		page, err := Browse(context.Background(), repository, BrowseRequest{Query: test.query, Kind: WritingRule, State: Paused, Limit: 20})
		if err != nil || page.Total != 1 || len(page.Records) != 1 || page.Records[0].ID != fmt.Sprintf("%032x", test.id) {
			t.Fatalf("query %q = %+v, %v", test.query, page, err)
		}
	}
	page, err := Browse(context.Background(), repository, BrowseRequest{Kind: Fact, State: Active, Limit: 20})
	if err != nil || page.Total != 1 || len(page.Records) != 1 || !page.Records[0].Enabled {
		t.Fatalf("active preferences = %+v, %v", page, err)
	}
}

func TestBrowseDoesNotTruncateAtAnswerContextScanLimit(t *testing.T) {
	repository := &browseRepository{}
	for n := 0; n < MaxScanRecords+2; n++ {
		record := browseFixture(n)
		if n == MaxScanRecords+1 {
			record.Text = "The final saved item contains the needle."
		}
		repository.records = append(repository.records, record)
	}
	page, err := Browse(context.Background(), repository, BrowseRequest{Query: "needle", Limit: 20})
	if err != nil || page.Total != 1 || len(page.Records) != 1 || page.TotalRecords != MaxScanRecords+2 {
		t.Fatalf("global search truncated at context limit = %+v, %v", page, err)
	}
}

func TestBrowseDoesNotReportPartialScanAsGlobalEmpty(t *testing.T) {
	repository := &browseRepository{errAt: MaxPageSize}
	for n := 0; n < 40; n++ {
		repository.records = append(repository.records, browseFixture(n))
	}
	page, err := Browse(context.Background(), repository, BrowseRequest{Query: "unseen", Limit: 20})
	if err == nil || page.Records != nil || page.TotalRecords != 0 {
		t.Fatalf("failed scan looked like an empty global result: %+v, %v", page, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Browse(ctx, repository, BrowseRequest{Limit: 20}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled search = %v", err)
	}
}

func TestBrowseClampsStaleLastPageAfterDeletion(t *testing.T) {
	repository := &browseRepository{}
	for n := 0; n < 20; n++ {
		repository.records = append(repository.records, browseFixture(n))
	}
	page, err := Browse(context.Background(), repository, BrowseRequest{Offset: 20, Limit: 20})
	if err != nil || page.Offset != 0 || page.Total != 20 || len(page.Records) != 20 {
		t.Fatalf("stale page did not recover after deletion: %+v, %v", page, err)
	}
}
