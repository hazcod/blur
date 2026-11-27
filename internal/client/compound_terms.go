package client

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage"
)

// The exact index splits on every non-word rune, so "threat-bot", "threat bot"
// and "threat_bot" index as {bot, threat} while "threatbot" is one token. One
// bounded, model-free step bridges the two spellings: sources that write two
// adjacent question words as one word (join), or one question word as two
// adjacent words (split), count as holding the words the question used. Their
// hits are added to the ordinary ones with match reports on the question's own
// terms, so nothing downstream sees a different question.
const (
	// compoundMaxProbes caps index queries per question while looking for a
	// variant; compoundRowBudget caps the rows they may read together.
	compoundMaxProbes = 32
	compoundRowBudget = 4096
	// compoundProbeRows is the row budget of an existence probe, which the
	// index answers from the first row. compoundDiscoveryRows is the budget of
	// the probe that lists sources holding both halves of a split.
	compoundProbeRows     = 64
	compoundDiscoveryRows = search.MaxIndexRows
	// compoundMaxReads and compoundMaxBytes cap the sources read, and their
	// decrypted size, to find one whose halves sit side by side. The index
	// lists at most search.MaxResults sources per query and has no cursor, so
	// reading further candidates is not possible.
	compoundMaxReads = search.MaxResults
	compoundMaxBytes = MaxSearchBytes / 4
	// compoundMaxFilterReads is a separate allowance for checking the hits of
	// a split variant's searches; sources already read are remembered.
	compoundMaxFilterReads = search.MaxResults
	// compoundMaxJoins caps join candidates; compoundMaxRunes is the longest
	// term split or produced by a join.
	compoundMaxJoins = 6
	compoundMaxRunes = 32
	// compoundMinSplitRunes is the shortest term that is split.
	compoundMinSplitRunes = 4
)

type compoundCandidate struct {
	// terms is the complete substituted term set, sorted and unique.
	terms []string
	// joined names the concatenation of a join; left and right are the halves
	// of a split of the term from.
	joined      string
	left, right string
	from        string
	// replaced are the question terms the compound stands for.
	replaced []string
	// respelled is the question with the compound written the other way.
	respelled string
}

type compoundWord struct {
	start, end int
	token      string
}

// compoundWords lists the words of text as the index reads them: runs of
// letters and digits, with a combining mark belonging to the word it follows.
func compoundWords(text string) []compoundWord {
	var words []compoundWord
	begin := -1
	flush := func(offset int) {
		if begin < 0 {
			return
		}
		if token := search.FoldWord(text[begin:offset]); token != "" {
			words = append(words, compoundWord{begin, offset, token})
		}
		begin = -1
	}
	for offset, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || begin >= 0 && search.IsCombiningAccent(r) {
			if begin < 0 {
				begin = offset
			}
		} else {
			flush(offset)
		}
	}
	flush(len(text))
	return words
}

// compoundJoiner reports whether gap, the text between two words, only
// separates them: spaces or tabs, or one joining mark (hyphen, underscore,
// dot, slash, apostrophe), which may carry spaces only when it is a dash. A
// line break or sentence punctuation followed by a space is a real boundary.
func compoundJoiner(gap string) bool {
	trimmed := strings.Trim(gap, " \t ")
	if trimmed == "" {
		return gap != ""
	}
	r, size := utf8.DecodeRuneInString(trimmed)
	if size != len(trimmed) || !strings.ContainsRune("-‐‑–_./'’", r) {
		return false
	}
	return trimmed == gap || strings.ContainsRune("-‐‑–", r)
}

// adjacentWords reports whether text holds the two folded words side by side,
// in order, with only a separator between them.
func adjacentWords(text, left, right string) bool {
	words := compoundWords(text)
	for i := 0; i+1 < len(words); i++ {
		if words[i].token == left && words[i+1].token == right && compoundJoiner(text[words[i].end:words[i+1].start]) {
			return true
		}
	}
	return false
}

func compoundTermSet(terms []string, drop []string, add ...string) []string {
	set := make([]string, 0, len(terms)+len(add))
	for _, term := range terms {
		if !slices.Contains(drop, term) {
			set = append(set, term)
		}
	}
	set = append(set, add...)
	slices.Sort(set)
	return slices.Compact(set)
}

// compoundCandidates lists substituted term sets in a fixed order: joins in
// question order, then the splits of each term of at least four runes, again
// in question order. A split puts halves of three runes or more first (shortest
// left half first), then halves with a short side; a one-rune half is only
// allowed beside a half of three runes or more. Every set fits the index's
// term limit.
func compoundCandidates(question string, terms []string) []compoundCandidate {
	known := make(map[string]bool, len(terms))
	for _, term := range terms {
		known[term] = true
	}
	words := compoundWords(question)
	var out []compoundCandidate
	seen := map[string]bool{}
	for i := 0; i+1 < len(words) && len(out) < compoundMaxJoins; i++ {
		a, b := words[i].token, words[i+1].token
		joined := a + b
		if a == b || !known[a] || !known[b] || known[joined] || utf8.RuneCountInString(joined) > compoundMaxRunes || seen[joined] ||
			!compoundJoiner(question[words[i].end:words[i+1].start]) {
			continue
		}
		seen[joined] = true
		respelled := question[:words[i].end] + question[words[i+1].start:]
		out = append(out, compoundCandidate{terms: compoundTermSet(terms, []string{a, b}, joined), joined: joined, replaced: []string{a, b}, respelled: respelled})
	}
	ordered := make([]string, 0, len(terms))
	for _, w := range words {
		if known[w.token] && !slices.Contains(ordered, w.token) {
			ordered = append(ordered, w.token)
		}
	}
	for _, term := range terms {
		if !slices.Contains(ordered, term) {
			ordered = append(ordered, term)
		}
	}
	for _, term := range ordered {
		runes := []rune(term)
		if len(runes) < compoundMinSplitRunes || len(runes) > compoundMaxRunes {
			continue
		}
		var long, short []compoundCandidate
		for cut := 1; cut < len(runes); cut++ {
			left, right := string(runes[:cut]), string(runes[cut:])
			if cut == 1 && len(runes)-cut < 3 || len(runes)-cut == 1 && cut < 3 {
				continue
			}
			set := compoundTermSet(terms, []string{term}, left, right)
			if len(set) > search.MaxTerms {
				continue
			}
			cand := compoundCandidate{terms: set, left: left, right: right, from: term, replaced: []string{term}, respelled: question}
			if at := slices.IndexFunc(words, func(w compoundWord) bool { return w.token == term }); at >= 0 {
				cand.respelled = question[:words[at].start] + left + " " + right + question[words[at].end:]
			}
			if cut >= 3 && len(runes)-cut >= 3 {
				long = append(long, cand)
			} else {
				short = append(short, cand)
			}
		}
		out = append(out, long...)
		out = append(out, short...)
	}
	return out
}

// compoundScope is the filter, window and time scope the surface searched.
type compoundScope struct {
	filters    []memoryFilter
	windows    searchScopeWalk
	start, end time.Time
}

func (s compoundScope) bounded() bool { return !s.start.IsZero() || !s.end.IsZero() }

// anywhere is the same filters over all time. Whether a word exists at all is
// answered from one index row; walking a long scope's windows for it is not.
func (s compoundScope) anywhere() compoundScope {
	return compoundScope{filters: s.filters, windows: singleSearchWindow(searchScopeWindow{})}
}

// compoundResolver holds the hard budgets of one question.
type compoundResolver struct {
	c      *Client
	scope  compoundScope
	probes int
	rows   int
	reads  int
	bytes  int
	// limited records that sources holding both halves of a split exist but
	// could not all be checked.
	limited bool
}

// probe asks the index for sources holding every term. ok is false when a
// budget ended or the index could not prove its prefix, which leaves the
// answer unknown; on a large store that is an ordinary outcome, not a failure.
// more reports matches beyond the returned ones.
func (r *compoundResolver) probe(ctx context.Context, scope compoundScope, terms []string, topK, rows int) (hits []search.IndexHit, more, ok bool, err error) {
	seen := map[string]bool{}
	for _, f := range scope.filters {
		if r.probes < 1 || r.rows < 1 {
			return nil, false, false, nil
		}
		r.probes--
		granted := min(rows, r.rows)
		page, err := r.c.searchExactWindows(ctx, search.ExactQuery{Terms: terms, AppID: f.app, Domain: f.domain, ExcludeAppIDs: []string{appidentity.BundleID}, CapturedStart: scope.start, CapturedEnd: scope.end, TopK: topK, MaxRows: granted, MaxBytes: search.MaxIndexBytes}, scope.windows)
		if errors.Is(err, search.ErrIndexBudget) || errors.Is(err, search.ErrInvalidRequest) {
			// A half the index cannot take as a term is unknown, like a prefix
			// it cannot prove: the respelling is optional, never a failure.
			r.rows -= granted
			return nil, false, false, nil
		}
		if err != nil {
			return nil, false, false, err
		}
		r.rows -= page.Rows
		more = more || page.HasMore
		for _, hit := range page.Hits {
			if !seen[hit.SourceID] {
				seen[hit.SourceID] = true
				hits = append(hits, hit)
			}
		}
	}
	return hits, more, true, nil
}

// inScope reports whether a source in scope holds every term, and returns one.
// A bounded scope is only walked once the terms are known to exist at all.
func (r *compoundResolver) inScope(ctx context.Context, terms []string) (hit search.IndexHit, found, ok bool, err error) {
	scopes := []compoundScope{r.scope}
	if r.scope.bounded() {
		scopes = []compoundScope{r.scope.anywhere(), r.scope}
	}
	for _, scope := range scopes {
		hits, _, ok, err := r.probe(ctx, scope, terms, 1, compoundProbeRows)
		if err != nil || !ok {
			return search.IndexHit{}, false, false, err
		}
		if len(hits) == 0 {
			return search.IndexHit{}, false, true, nil
		}
		hit = hits[0]
	}
	return hit, true, true, nil
}

// compoundVariant is a verified respelling. Only sources that hold the
// compound itself count: the joined word, or both halves side by side. The
// index knows that halves are present, not where, so a split is checked
// against the source text.
type compoundVariant struct {
	// terms is the question's term set with the compound respelled.
	terms       []string
	joined      string
	left, right string
	// replaced are the question terms the compound stands for.
	replaced []string
	// respelled is the question with the compound written as sources write it.
	respelled string
	// verified is the source resolution proved to hold the compound, and the
	// terms its match report refers to.
	verified      search.IndexHit
	verifiedTerms []string
	resolver      *compoundResolver
	adjacent      map[string]bool
}

// words is the compound as the sources spell it, sorted.
func (v *compoundVariant) words() []string {
	switch {
	case v == nil:
		return nil
	case v.joined != "":
		return []string{v.joined}
	}
	words := []string{v.left, v.right}
	slices.Sort(words)
	return words
}

// compoundBits is the match-report bits of the compound's words in a query's
// terms. ok is false when one is missing, which leaves a report unreadable.
func (v *compoundVariant) compoundBits(terms []string) (need uint32, ok bool) {
	for _, term := range v.words() {
		at := slices.Index(terms, term)
		if at < 0 || at >= 32 {
			return 0, false
		}
		need |= 1 << uint(at)
	}
	return need, need != 0
}

// adjacentIn reads one source and reports whether the halves sit side by side.
// checked is false when the read budget ended first.
func (r *compoundResolver) adjacentIn(ctx context.Context, v *compoundVariant, hit search.IndexHit) (adjacent, checked bool, err error) {
	if known, ok := v.adjacent[hit.SourceID]; ok {
		return known, true, nil
	}
	if r.reads < 1 || hit.Bytes < 1 || hit.Bytes > r.bytes {
		return false, false, nil
	}
	source, found, err := storage.ReadSourceEvidence(ctx, r.c.data, hit.SourceID)
	if err != nil {
		return false, false, err
	}
	r.reads--
	if !found {
		return false, true, nil
	}
	r.bytes -= len(source.Text()) + len(source.Title())
	adjacent = adjacentWords(source.Text(), v.left, v.right) || adjacentWords(source.Title(), v.left, v.right)
	v.adjacent[hit.SourceID] = adjacent
	return adjacent, true, nil
}

// adjacentSource lists the best sources holding every term and returns the
// first whose halves sit side by side. Where that prefix cannot be proven, the
// single best source still can.
func (r *compoundResolver) adjacentSource(ctx context.Context, v *compoundVariant, terms []string) (search.IndexHit, bool, error) {
	hits, more, ok, err := r.probe(ctx, r.scope, terms, compoundMaxReads, compoundDiscoveryRows)
	if err == nil && !ok {
		hits, more, ok, err = r.probe(ctx, r.scope, terms, 1, compoundDiscoveryRows)
	}
	if err != nil {
		return search.IndexHit{}, false, err
	}
	if !ok {
		r.limited = true
		return search.IndexHit{}, false, nil
	}
	unread := more
	for _, hit := range hits {
		adjacent, checked, err := r.adjacentIn(ctx, v, hit)
		if err != nil {
			return search.IndexHit{}, false, err
		}
		if adjacent {
			return hit, true, nil
		}
		unread = unread || !checked
	}
	r.limited = r.limited || unread && len(hits) > 0
	return search.IndexHit{}, false, nil
}

// accept starts the hit filter of a verified variant with its own read
// allowance; sources read during resolution stay remembered.
func (r *compoundResolver) accept(v *compoundVariant) *compoundVariant {
	r.reads, r.bytes, r.limited = compoundMaxFilterReads, compoundMaxBytes, false
	return v
}

// resolve returns the first candidate whose compound a source in scope holds.
// Nothing is chained: it returns at the first verified respelling. A join is
// taken whenever the joined word exists, because that word is itself the
// proof. A split is only tried for a term no source in scope holds as typed
// (present lists the terms already known to occur), and only taken once a
// source is read that has the halves side by side.
func (r *compoundResolver) resolve(ctx context.Context, question string, terms []string, joinsOnly bool, present map[string]bool) (*compoundVariant, error) {
	absent := map[string]bool{}
	for _, cand := range compoundCandidates(question, terms) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.probes < 1 || r.rows < 1 {
			return nil, nil
		}
		v := &compoundVariant{terms: cand.terms, joined: cand.joined, left: cand.left, right: cand.right, replaced: cand.replaced, respelled: cand.respelled, resolver: r, adjacent: map[string]bool{}}
		if cand.joined != "" {
			hit, found, ok, err := r.inScope(ctx, v.words())
			if err != nil {
				return nil, err
			}
			if ok && found {
				v.verified, v.verifiedTerms = hit, v.words()
				return r.accept(v), nil
			}
			continue
		}
		if joinsOnly || cand.left == cand.right {
			continue
		}
		// A term that occurs in scope as typed is not the spelling problem. An
		// unproven answer counts as present: such a term is not split.
		if present[cand.from] {
			continue
		}
		if !absent[cand.from] {
			_, found, ok, err := r.inScope(ctx, []string{cand.from})
			if err != nil {
				return nil, err
			}
			if !ok || found {
				present[cand.from] = true
				continue
			}
			absent[cand.from] = true
		}
		// The question's other words narrow the sources worth reading; when no
		// source has them all, the halves alone still identify the compound.
		for _, query := range [][]string{cand.terms, v.words()} {
			hit, found, err := r.adjacentSource(ctx, v, query)
			if err != nil {
				return nil, err
			}
			if found {
				v.verified, v.verifiedTerms = hit, query
				return r.accept(v), nil
			}
			if len(cand.terms) == len(v.words()) {
				break // the question is the compound alone
			}
		}
	}
	return nil, nil
}

// admit keeps the hits of one query that hold the compound: the joined word,
// or both halves side by side. A split hit that cannot be read inside the
// budget is dropped and reported through limited.
func (v *compoundVariant) admit(ctx context.Context, terms []string, hits []search.IndexHit) (kept []search.IndexHit, limited bool, err error) {
	need, readable := v.compoundBits(terms)
	if !readable {
		return nil, len(hits) > 0, nil
	}
	for _, hit := range hits {
		if hit.MatchedTerms&need != need {
			continue
		}
		if v.joined != "" {
			kept = append(kept, hit)
			continue
		}
		adjacent, checked, err := v.resolver.adjacentIn(ctx, v, hit)
		if err != nil {
			return nil, false, err
		}
		if adjacent {
			kept = append(kept, hit)
		}
		limited = limited || !checked
	}
	return kept, limited, nil
}

// hits returns the sources in scope that hold the compound, with match
// reports rewritten onto the question's own terms: the compound stands for
// the terms it replaced. Two all-terms searches run, one with every other
// question word and one with the compound alone; a source found only by the
// second reports the compound's terms and nothing else. A search the index
// cannot prove inside its budget is skipped and reported through limited, and
// the source verified during resolution is returned regardless, so a
// respelling never turns a question into an error. more reports further
// matches beyond topK.
func (v *compoundVariant) hits(ctx context.Context, question []string, topK, maxRows int) (out []search.IndexHit, limited, more bool, err error) {
	r := v.resolver
	bitOf := func(term string) uint32 {
		if at := slices.Index(question, term); at >= 0 && at < 32 {
			return 1 << uint(at)
		}
		return 0
	}
	var replaced uint32
	for _, term := range v.replaced {
		replaced |= bitOf(term)
	}
	at := map[string]int{}
	add := func(terms []string, hits []search.IndexHit) {
		words := v.words()
		for _, hit := range hits {
			var report uint32
			for i, term := range terms {
				if i >= 32 || hit.MatchedTerms&(1<<uint(i)) == 0 {
					continue
				}
				report |= bitOf(term)
				if slices.Contains(words, term) {
					report |= replaced
				}
			}
			hit.MatchedTerms = report
			if i, seen := at[hit.SourceID]; seen {
				out[i].MatchedTerms |= report
				out[i].Score = max(out[i].Score, hit.Score)
				continue
			}
			at[hit.SourceID] = len(out)
			out = append(out, hit)
		}
	}
	queries := [][]string{v.terms}
	if len(v.terms) > len(v.words()) {
		queries = append(queries, v.words())
	}
	if v.joined == "" {
		// Halves-apart sources share the list with the ones that match; ask
		// for as many as can be checked.
		topK, maxRows = max(topK, compoundMaxFilterReads), search.MaxIndexRows
	}
	for _, terms := range queries {
		var found []search.IndexHit
		for _, f := range r.scope.filters {
			if err := ctx.Err(); err != nil {
				return nil, false, false, err
			}
			page, err := r.c.searchExactWindows(ctx, search.ExactQuery{Terms: terms, AppID: f.app, Domain: f.domain, ExcludeAppIDs: []string{appidentity.BundleID}, CapturedStart: r.scope.start, CapturedEnd: r.scope.end, TopK: topK, MaxRows: maxRows, MaxBytes: search.MaxIndexBytes}, r.scope.windows)
			if errors.Is(err, search.ErrIndexBudget) || errors.Is(err, search.ErrInvalidRequest) {
				limited = true
				continue
			}
			if err != nil {
				return nil, false, false, err
			}
			limited = limited || page.CoverageLimited
			if v.joined == "" {
				// Further sources hold both halves and were not checked.
				limited = limited || page.HasMore
			} else {
				more = more || page.HasMore
			}
			found = append(found, page.Hits...)
		}
		kept, unread, err := v.admit(ctx, terms, found)
		if err != nil {
			return nil, false, false, err
		}
		limited = limited || unread
		add(terms, kept)
	}
	verified := v.verified
	if need, ok := v.compoundBits(v.verifiedTerms); ok && verified.MatchedTerms&need != need {
		verified.MatchedTerms |= need // an existence probe reports no terms
	}
	add(v.verifiedTerms, []search.IndexHit{verified})
	return out, limited, more, nil
}

// compoundEligible reports whether a plan may be widened to a respelling, and
// whether only joins apply. A quoted search asks for the literal string. An
// unquoted identifier is literal too, but its own separators are exactly what
// a joined spelling drops ("threat_bot" for "ThreatBot"), and Ask still checks
// every hit against the whole identifier.
func compoundEligible(plan assistant.Plan, question string) (eligible, joinsOnly bool) {
	if len(plan.Terms) == 0 || question == "" {
		return false, false
	}
	if len(plan.TechnicalSubjects) > 0 {
		return !plan.Literal || !quotesText(question), true
	}
	return !plan.Literal, false
}

// quotesText reports whether the question quotes anything, by the search
// grammar's rule: a double quote or backtick anywhere, or a single quote that
// opens a word.
func quotesText(question string) bool {
	for i := 0; i < len(question); i++ {
		switch question[i] {
		case '"', '`':
			return true
		case '\'':
			if i == 0 || question[i-1] == ' ' {
				return true
			}
		}
	}
	return false
}

// compoundVariantFor resolves a respelling of the plan's terms in a scope.
// found are the surface's own hits: a term one of them reports is known to
// occur and is never split. limited reports that a split could not be fully
// checked; the variant is nil when none verified.
func (c *Client) compoundVariantFor(ctx context.Context, plan assistant.Plan, question string, scope compoundScope, found ...map[string]search.IndexHit) (v *compoundVariant, limited bool, err error) {
	eligible, joinsOnly := compoundEligible(plan, question)
	if !eligible {
		return nil, false, nil
	}
	terms := plan.Terms[:min(len(plan.Terms), search.MaxTerms)]
	present := map[string]bool{}
	for _, hits := range found {
		for _, hit := range hits {
			for i, term := range terms {
				if hit.MatchedTerms&(1<<uint(i)) != 0 {
					present[term] = true
				}
			}
		}
	}
	r := &compoundResolver{c: c, scope: scope, probes: compoundMaxProbes, rows: compoundRowBudget, reads: compoundMaxReads, bytes: compoundMaxBytes}
	v, err = r.resolve(ctx, question, terms, joinsOnly, present)
	return v, r.limited && v == nil, err
}

// locateTerms adds the spelling a respelled compound has in the sources, so
// passages and previews are cut around words a source really contains. The
// question's terms stay first: match reports index them.
func locateTerms(terms, spellings []string) []string {
	if len(spellings) == 0 || len(terms) >= search.MaxTerms {
		return terms
	}
	out := append(slices.Clone(terms), spellings...)
	return out[:min(len(out), search.MaxTerms)]
}

// mergeCompoundHits adds a variant's hits to the question's own. A source
// that holds every question term, the compound standing for the ones it
// replaced, is a full match; any other is a closest match.
func mergeCompoundHits(exact, partial map[string]search.IndexHit, extra []search.IndexHit, terms int) {
	full := uint32(1)<<uint(min(terms, 32)) - 1
	for _, hit := range extra {
		if _, known := exact[hit.SourceID]; known {
			continue
		}
		if prev, known := partial[hit.SourceID]; known {
			hit.MatchedTerms |= prev.MatchedTerms
			hit.Score = max(hit.Score, prev.Score)
			if hit.CapturedAt.Before(prev.CapturedAt) {
				hit.CapturedAt = prev.CapturedAt
			}
		}
		if hit.MatchedTerms == full {
			exact[hit.SourceID] = hit
			delete(partial, hit.SourceID)
		} else {
			partial[hit.SourceID] = hit
		}
	}
}

func mapHits(byID map[string]search.IndexHit) []search.IndexHit {
	hits := make([]search.IndexHit, 0, len(byID))
	for _, hit := range byID {
		hits = append(hits, hit)
	}
	slices.SortFunc(hits, func(a, b search.IndexHit) int { return strings.Compare(a.SourceID, b.SourceID) })
	return hits
}
