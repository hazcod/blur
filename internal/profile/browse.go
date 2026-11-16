package profile

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

type State string

const (
	Active State = "active"
	Paused State = "paused"
)

type BrowseRequest struct {
	Query  string
	Kind   Kind
	State  State
	Offset int
	Limit  int
}

type BrowsePage struct {
	Records      []Record
	Offset       int
	Total        int
	TotalRecords int
}

// Browse searches every encrypted repository page and retains at most two
// display pages. The time limit bounds a full scan; failed or canceled scans
// return an error rather than an incomplete total or a false empty result.
func Browse(parent context.Context, repository Repository, request BrowseRequest) (BrowsePage, error) {
	if parent == nil || repository == nil || !repository.EncryptedAtRest() || request.Offset < 0 || request.Limit < 1 || request.Limit > MaxPageSize ||
		request.Kind != "" && request.Kind != Fact && request.Kind != WritingRule ||
		request.State != "" && request.State != Active && request.State != Paused ||
		len(request.Query) > 512 || !utf8.ValidString(request.Query) || strings.ContainsRune(request.Query, 0) {
		return BrowsePage{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	terms := strings.Fields(normalized(request.Query))
	result := BrowsePage{Records: []Record{}, Offset: request.Offset}
	lastPage := make([]Record, 0, request.Limit)
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return BrowsePage{}, err
		}
		page, err := repository.ListProfile(ctx, PageRequest{After: after, Limit: MaxPageSize})
		if err != nil {
			return BrowsePage{}, err
		}
		if len(page.Records) > MaxPageSize || page.HasMore && (len(page.Records) == 0 || !ValidID(page.Next) || page.Next == after) {
			return BrowsePage{}, ErrInvalid
		}
		for _, record := range page.Records {
			result.TotalRecords++
			if request.Kind != "" && record.Kind != request.Kind || request.State == Active && !record.Enabled || request.State == Paused && record.Enabled {
				continue
			}
			text := normalized(strings.Join([]string{record.Text, record.Scope.AppID, record.Scope.Domain, record.Scope.Project, record.Scope.Audience}, " "))
			matches := true
			for _, term := range terms {
				if !strings.Contains(text, term) {
					matches = false
					break
				}
			}
			if !matches {
				continue
			}
			if result.Total >= request.Offset && len(result.Records) < request.Limit {
				result.Records = append(result.Records, record)
			}
			if result.Total%request.Limit == 0 {
				lastPage = lastPage[:0]
			}
			lastPage = append(lastPage, record)
			result.Total++
		}
		if !page.HasMore {
			break
		}
		after = page.Next
	}
	if result.Total == 0 {
		result.Offset = 0
	} else if request.Offset >= result.Total {
		// A deletion may remove the only item on the requested last page.
		result.Offset = (result.Total - 1) / request.Limit * request.Limit
		result.Records = lastPage
	}
	return result, nil
}
