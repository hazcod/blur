package client

import (
	"context"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/knowledge"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

const libraryPageSize = 20

type libraryBrowserState struct {
	active              bool
	query, appliedQuery string
	processingStatus    string
	kind                knowledge.Kind
	page, total         int
	request             uint64
	refreshing          bool
	updatesAvailable    bool
	cancel              context.CancelFunc
}

type libraryDirectoryPage struct {
	knowledge.Page
	number, total int
	groups        [][]knowledge.Entity
}

func librarySubjectKey(entity knowledge.Entity) string {
	return libraryNameKey(entity.Name)
}

// libraryNameKey folds only presentation separators, leaving symbols such as
// +, #, and . intact because they can change technical names.
func libraryNameKey(value string) string {
	var key strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsSpace(r) || unicode.Is(unicode.Pd, r) || r == '_' {
			continue
		}
		key.WriteRune(r)
	}
	return key.String()
}

func sameLibraryGroups(a, b [][]knowledge.Entity) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j].ID != b[i][j].ID {
				return false
			}
		}
	}
	return true
}

func validLibraryKind(kind knowledge.Kind) bool {
	switch kind {
	case "", knowledge.Person, knowledge.Project, knowledge.Topic, knowledge.Place, knowledge.Workflow:
		return true
	default:
		return false
	}
}

func matchesLibraryEntity(entity knowledge.Entity, query string, kind knowledge.Kind) bool {
	if kind != "" && entity.Kind != kind {
		return false
	}
	parts := []string{entity.Name}
	parts = append(parts, entity.Aliases...)
	var nameKeys []string
	for _, name := range parts {
		if key := libraryNameKey(name); key != "" {
			nameKeys = append(nameKeys, key)
		}
	}
	for _, fact := range entity.Facts {
		if fact.State != knowledge.Dismissed {
			parts = append(parts, fact.Text)
		}
	}
	for _, loop := range entity.OpenLoops {
		if loop.State != knowledge.Dismissed {
			parts = append(parts, loop.Text)
		}
	}
	text := strings.ToLower(strings.Join(parts, "\n"))
	for _, word := range librarySearchTerms(query) {
		matched := libraryTextHasTerm(text, word)
		if !matched {
			key := libraryNameKey(word)
			for _, name := range nameKeys {
				if key != "" && libraryTextHasTerm(name, key) {
					matched = true
					break
				}
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func librarySearchTerms(query string) []string {
	terms := strings.Fields(strings.ToLower(query))
	for i, term := range terms {
		terms[i] = strings.TrimFunc(term, func(r rune) bool {
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				return false
			}
			return !strings.ContainsRune("#+._-", r)
		})
	}
	return slices.DeleteFunc(terms, func(term string) bool { return term == "" })
}

func libraryTextHasTerm(text, term string) bool {
	text, term = strings.ToLower(text), strings.ToLower(term)
	if term == "" {
		return false
	}
	for offset := 0; offset <= len(text)-len(term); {
		relative := strings.Index(text[offset:], term)
		if relative < 0 {
			return false
		}
		start := offset + relative
		end := start + len(term)
		beforeWord, afterWord := false, false
		if start > 0 {
			before, _ := utf8.DecodeLastRuneInString(text[:start])
			beforeWord = unicode.IsLetter(before) || unicode.IsNumber(before) || before == '_'
		}
		if end < len(text) {
			after, _ := utf8.DecodeRuneInString(text[end:])
			afterWord = unicode.IsLetter(after) || unicode.IsNumber(after) || after == '_'
		}
		if !beforeWord && !afterWord {
			return true
		}
		offset = start + 1
	}
	return false
}

func libraryEntityRelevance(entity knowledge.Entity, query string) int {
	query = strings.TrimSpace(query)
	if query == "" {
		return 0
	}
	queryKey := libraryNameKey(query)
	queryWords := librarySearchTerms(query)
	nameKeys := []string{libraryNameKey(entity.Name)}
	for _, alias := range entity.Aliases {
		nameKeys = append(nameKeys, libraryNameKey(alias))
	}
	best := 0
	for index, name := range nameKeys {
		if name == "" {
			continue
		}
		switch {
		case name == queryKey:
			if index == 0 {
				best = max(best, 1000)
			} else {
				best = max(best, 950)
			}
		case strings.HasPrefix(name, queryKey):
			if index == 0 {
				best = max(best, 850)
			} else {
				best = max(best, 800)
			}
		case strings.Contains(name, queryKey):
			if index == 0 {
				best = max(best, 700)
			} else {
				best = max(best, 650)
			}
		}
	}
	nameText := strings.Join(nameKeys, " ")
	nameMatches, anyNameMatch := true, false
	for _, word := range queryWords {
		key := libraryNameKey(word)
		if key == "" {
			continue
		}
		matched := libraryTextHasTerm(nameText, key)
		nameMatches = nameMatches && matched
		anyNameMatch = anyNameMatch || matched
	}
	if nameMatches {
		best = max(best, 600)
	} else if anyNameMatch {
		best = max(best, 400)
	}
	// matchesLibraryEntity already guarantees that the query occurs in the
	// searchable name, alias, fact, or open question. Keep those content-only
	// hits discoverable while ranking direct subject matches first.
	return max(best, 100)
}

func libraryEntityNameOrder(a, b knowledge.Entity) bool {
	collator := collate.New(language.Und, collate.Numeric, collate.IgnoreCase)
	if order := collator.CompareString(a.Name, b.Name); order != 0 {
		return order < 0
	}
	if keyA, keyB := librarySubjectKey(a), librarySubjectKey(b); keyA != keyB {
		return keyA < keyB
	}
	return a.ID < b.ID
}

// Search the complete encrypted directory, retaining only the requested and
// last result pages in memory. Filtering a loaded UI page would miss later
// manual entries and the automatic directory after them.
func (c *Client) libraryDirectoryPage(ctx context.Context, query string, kind knowledge.Kind, number int) (libraryDirectoryPage, error) {
	if c == nil || c.data == nil || ctx == nil || len(query) > MaxQueryBytes || !utf8.ValidString(query) || strings.ContainsRune(query, 0) || !validLibraryKind(kind) || number < 0 {
		return libraryDirectoryPage{}, ErrInvalidRequest
	}
	result := libraryDirectoryPage{number: number}
	var allGroups [][]knowledge.Entity
	groupIndex := map[string]int{}
	after := ""
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return libraryDirectoryPage{}, err
		}
		page, err := c.readKnowledgePageContext(ctx, after)
		if err != nil {
			return libraryDirectoryPage{}, err
		}
		if _, err := appendKnowledgePage(knowledge.Page{}, page, ""); err != nil || page.HasMore && (page.Next == after || seen[page.Next]) {
			return libraryDirectoryPage{}, errKnowledgePage
		}
		for _, entity := range page.Entities {
			if !matchesLibraryEntity(entity, query, kind) {
				continue
			}
			key := librarySubjectKey(entity)
			index, ok := groupIndex[key]
			if !ok {
				index = len(allGroups)
				groupIndex[key] = index
				allGroups = append(allGroups, nil)
			}
			allGroups[index] = append(allGroups[index], entity)
		}
		if !page.HasMore {
			break
		}
		seen[page.Next] = true
		after = page.Next
	}
	for i := range allGroups {
		sort.SliceStable(allGroups[i], func(a, b int) bool {
			left, right := libraryEntityRelevance(allGroups[i][a], query), libraryEntityRelevance(allGroups[i][b], query)
			if left != right {
				return left > right
			}
			return libraryEntityNameOrder(allGroups[i][a], allGroups[i][b])
		})
	}
	sort.SliceStable(allGroups, func(i, j int) bool {
		left, right := allGroups[i][0], allGroups[j][0]
		leftRank, rightRank := libraryEntityRelevance(left, query), libraryEntityRelevance(right, query)
		if leftRank != rightRank {
			return leftRank > rightRank
		}
		return libraryEntityNameOrder(left, right)
	})
	result.total = len(allGroups)
	lastNumber := max(0, (result.total-1)/libraryPageSize)
	if number > lastNumber {
		number = lastNumber
		result.number = lastNumber
	}
	start, end := number*libraryPageSize, min((number+1)*libraryPageSize, len(allGroups))
	result.groups = allGroups[start:end]
	for _, group := range result.groups {
		if len(group) > 0 {
			result.Entities = append(result.Entities, group[0])
		}
	}
	result.HasMore = result.number < lastNumber
	if result.HasMore && len(result.Entities) > 0 {
		result.Next = result.Entities[len(result.Entities)-1].ID
	}
	c.libraryAttributions(ctx, result.Entities)
	if err := ctx.Err(); err != nil {
		return libraryDirectoryPage{}, err
	}
	return result, nil
}

// Delete All also erases the text used to browse prior memories. Keep the
// request fence monotonic so a cancelled, already-queued read stays obsolete.
func (d *desktop) clearLibraryBrowserSearch() {
	if d == nil {
		return
	}
	request := d.libraryBrowser.request + 1
	if d.libraryBrowser.cancel != nil {
		d.libraryBrowser.cancel()
	}
	d.libraryBrowser = libraryBrowserState{request: request}
	d.libraryRefreshPending, d.libraryLoading = false, false
}

func (d *desktop) loadLibraryBrowserPage(number int, refreshing bool) {
	if d == nil || d.client == nil || d.client.data == nil {
		return
	}
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	x := &d.libraryBrowser
	if x.cancel != nil {
		x.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), shortOperationTimeout)
	x.cancel = cancel
	x.active, x.refreshing = true, refreshing
	x.request++
	request, generation := x.request, d.memoryGeneration
	query, kind := x.appliedQuery, x.kind
	d.libraryLoading = true
	if !refreshing {
		d.app.RequestUpdate()
	}
	go func() {
		defer cancel()
		result, err := d.client.libraryDirectoryPage(ctx, query, kind, number)
		d.app.Post(func() {
			if request != d.libraryBrowser.request || !currentMemoryRead(generation, d.memoryGeneration) {
				return
			}
			processingStatus := libraryStatusCopy(d.client.LibraryStatus(), effectiveProvider(d.client.LocalAISettings()))
			updated := !refreshing
			if !d.client.PublishMemoryRead(epoch, func() {
				d.libraryBrowser.cancel = nil
				d.libraryLoading, d.libraryBrowser.refreshing = false, false
				if refreshing && (d.libraryInspect != nil || d.libraryInspectLoading || d.libraryEditFact != "") {
					d.libraryRefreshPending = true
					return
				}
				if err != nil {
					status := friendlyError(err)
					updated = updated || d.status != status
					d.status = status
					return
				}
				if !sameKnowledgePage(d.library, result.Page) || !sameLibraryGroups(d.libraryGroups, result.groups) || d.libraryBrowser.total != result.total || d.libraryBrowser.page != result.number {
					d.library = result.Page
					d.libraryGroups = result.groups
					d.libraryBrowser.total, d.libraryBrowser.page = result.total, result.number
					updated = true
				}
				d.libraryBrowser.updatesAvailable = false
				if d.libraryBrowser.processingStatus != processingStatus {
					d.libraryBrowser.processingStatus = processingStatus
					updated = true
				}
				d.libraryLoaded = true
				if !refreshing {
					d.status = ""
				}
			}) {
				return
			}
			if d.libraryRefreshPending && d.libraryInspect == nil && !d.libraryInspectLoading && d.libraryEditFact == "" {
				d.refreshLibrary()
			}
			if updated {
				d.app.RequestUpdate()
			}
		})
	}()
}
