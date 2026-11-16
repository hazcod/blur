package profile

import (
	"context"
	"sort"
	"strings"
	"unicode"
)

type Selection struct {
	Rules   []Record
	Facts   []Record
	Omitted int
}

// ReadAll scans encrypted, bounded pages. It reports capacity rather than
// quietly treating the first physical pages as the complete set of preferences.
func ReadAll(ctx context.Context, repository Repository) ([]Record, error) {
	if ctx == nil || repository == nil || !repository.EncryptedAtRest() {
		return nil, ErrInvalid
	}
	records := []Record{}
	after := ""
	for {
		page, err := repository.ListProfile(ctx, PageRequest{After: after, Limit: MaxPageSize})
		if err != nil {
			return nil, err
		}
		if len(page.Records) > MaxPageSize || len(records)+len(page.Records) > MaxScanRecords {
			return nil, ErrCapacity
		}
		records = append(records, page.Records...)
		if !page.HasMore {
			return records, nil
		}
		if page.Next == "" || page.Next == after {
			return nil, ErrInvalid
		}
		after = page.Next
	}
}

func relevantFact(question, text string) bool {
	q := normalized(question)
	for _, phrase := range []string{"what do you know about me", "what have i told you about myself", "my preferences", "my personal preferences", "about myself"} {
		if strings.Contains(q, phrase) {
			return true
		}
	}
	stop := " a about all am an and are as at be been but by can could did do does for from had has have how i in is it me my of on or our please prefer preference preferences that the their them there these they this those to was we were what when where which who why with would you your "
	terms := strings.FieldsFunc(strings.ToLower(question), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	for _, term := range terms {
		if len(term) < 3 || strings.Contains(stop, " "+term+" ") {
			continue
		}
		if MentionsScope(text, term) {
			return true
		}
	}
	return false
}

// Select applies all scope conditions, then prioritizes more specific rules.
// Only explicit mentions in the request supply project/audience context;
// incidental active-window content can never activate a rule.
func Select(records []Record, question string, context Context, includeFacts bool) Selection {
	result := Selection{}
	candidates := []Record{}
	for _, record := range records {
		if !record.Enabled {
			continue
		}
		current := context
		if record.Scope.Project != "" && MentionsScope(question, record.Scope.Project) {
			current.Projects = append(append([]string(nil), current.Projects...), record.Scope.Project)
		}
		if record.Scope.Audience != "" && MentionsScope(question, record.Scope.Audience) {
			current.Audiences = append(append([]string(nil), current.Audiences...), record.Scope.Audience)
		}
		if !record.Scope.Applies(current) {
			continue
		}
		if record.Kind == WritingRule || record.Kind == Fact && includeFacts && relevantFact(question, record.Text) {
			candidates = append(candidates, record)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Kind != b.Kind {
			return a.Kind == Fact
		}
		if a.Scope.Specificity() != b.Scope.Specificity() {
			return a.Scope.Specificity() > b.Scope.Specificity()
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		return a.ID < b.ID
	})
	used, count := 0, 0
	for _, record := range candidates {
		if count >= MaxContextRecords || used+len(record.Text) > MaxContextBytes {
			result.Omitted++
			continue
		}
		used += len(record.Text)
		count++
		if record.Kind == WritingRule {
			result.Rules = append(result.Rules, record)
		} else {
			result.Facts = append(result.Facts, record)
		}
	}
	return result
}
