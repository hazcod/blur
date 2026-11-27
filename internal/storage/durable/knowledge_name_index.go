package durable

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"sort"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// Inferred Library names are found through three keyed posting kinds. Every key
// below is derived from search.NormalizeTokens, search.OrderedTokens and
// search.FoldLower, so changing any of those rules changes what a correct index
// contains: bump the namespaces and nameTokenCursorID together so existing
// stores rebuild instead of silently missing entries.
//
// Kind bytes share the order group with other indexes and must stay distinct
// from kindInferredEntity (0x05), kindInferredSource (0x06), taskOrderKind
// (0x07) and profileOrderKind (0x08).
const (
	// v1 whole-name folded postings. Read only until the token index is
	// complete, then retired by a prefix sweep (never by recomputing keys).
	foldedInferredNameNS        = "lib-fold-name-v1"
	kindFoldedInferredName byte = 0x09

	tokenSetNameNS        = "lib-name-tokens-v2"
	squashedNameNS        = "lib-name-squash-v2"
	wordNameNS            = "lib-name-word-v2"
	kindTokenSetName byte = 0x0a
	kindSquashedName byte = 0x0b
	kindWordName     byte = 0x0c

	nameTokenCursorID = "name-tokens-v2"
	foldedRetiredID   = "folded-names-v1-retired"

	// Names longer than this are sentences: they get no word postings.
	maxWordPostingTokens = 16
	minWordPostingRunes  = 2
	// Retirement deletes at most this many v1 postings per transaction.
	foldedRetireBatch = 128

	// One lookup call shares this many physical read operations between
	// posting scans and candidate name reads. The remainder of the read limit
	// stays reserved for loading the returned entities.
	nameLookupBudget    = 3000
	nameLookupReadLimit = nameLookupBudget + bolt.MaxTransactionOps
	nameCandidateCost   = 4
)

type libraryNameCursor struct {
	After    string `json:"after,omitempty"`
	Complete bool   `json:"complete"`
}

type namePostingKind struct {
	kind byte
	ns   string
}

var (
	foldedPostings   = namePostingKind{kindFoldedInferredName, foldedInferredNameNS}
	tokenSetPostings = namePostingKind{kindTokenSetName, tokenSetNameNS}
	squashedPostings = namePostingKind{kindSquashedName, squashedNameNS}
	wordPostings     = namePostingKind{kindWordName, wordNameNS}
)

// Separate postings avoid a shared per-name capacity limit. Variants keep their
// original entity IDs and app/domain provenance. Prefix and suffix are keyed;
// the value is encrypted and authenticated against the key.
func (s *Store) postingPrefix(k namePostingKind, form string) []byte {
	return s.logicalKey(groupOrder, k.kind, k.ns, form)
}

func (s *Store) postingKey(k namePostingKind, form, id string) []byte {
	prefix := s.postingPrefix(k, form)
	suffix := s.logicalKey(groupRecord, k.kind, k.ns, id)
	defer clear(suffix)
	return append(prefix, suffix[2:]...)
}

func foldedNameForm(name string) string {
	return knowledge.NormalizeExact(search.FoldLower(name))
}

// nameForms are the posting forms of one entity name. Words is empty for
// sentence-length names and omits single-rune tokens.
type nameForms struct {
	TokenSet string
	Squashed string
	Words    []string
	Distinct int
}

func nameFormsOf(name string) (nameForms, error) {
	set, err := search.NormalizeTokens(name, knowledge.MaxTextBytes)
	if err != nil {
		return nameForms{}, err
	}
	ordered, err := search.OrderedTokens(name, knowledge.MaxTextBytes)
	if err != nil {
		return nameForms{}, err
	}
	forms := nameForms{TokenSet: joinTokens(set, " "), Squashed: joinTokens(ordered, ""), Distinct: len(set)}
	if len(set) <= maxWordPostingTokens {
		for _, token := range set {
			if utf8.RuneCountInString(token) >= minWordPostingRunes {
				forms.Words = append(forms.Words, token)
			}
		}
	}
	return forms, nil
}

func joinTokens(tokens []string, sep string) string {
	var out []byte
	for i, token := range tokens {
		if i > 0 {
			out = append(out, sep...)
		}
		out = append(out, token...)
	}
	return string(out)
}

func (tx *Tx) putPosting(k namePostingKind, form, id string) error {
	key := tx.store.postingKey(k, form, id)
	defer clear(key)
	envelope, err := tx.store.seal(key, k.ns, []byte(id))
	if err != nil {
		return err
	}
	defer clear(envelope)
	return tx.writeEntryLocked(key, envelope, true)
}

func (tx *Tx) deletePosting(k namePostingKind, form, id string) error {
	key := tx.store.postingKey(k, form, id)
	defer clear(key)
	found, err := tx.writer.Has(key)
	if err != nil {
		return mapStorePhysicalError(err)
	}
	if !found {
		return nil // Legacy entities can be deleted before backfill.
	}
	return tx.deleteEntryLocked(key)
}

// putNamePostings publishes all current postings of an entity. Names are part
// of the entity ID hash, so a name never changes under one ID and there is no
// rename path to maintain.
func (tx *Tx) putNamePostings(entity knowledge.Entity) error {
	forms, err := nameFormsOf(entity.Name)
	if err != nil {
		return enrich.ErrInvalid
	}
	if forms.TokenSet == "" {
		return nil
	}
	if err := tx.putPosting(tokenSetPostings, forms.TokenSet, entity.ID); err != nil {
		return err
	}
	if err := tx.putPosting(squashedPostings, forms.Squashed, entity.ID); err != nil {
		return err
	}
	for _, word := range forms.Words {
		if err := tx.putPosting(wordPostings, word, entity.ID); err != nil {
			return err
		}
	}
	return nil
}

func (tx *Tx) deleteNamePostings(entity knowledge.Entity) error {
	if err := tx.deletePosting(foldedPostings, foldedNameForm(entity.Name), entity.ID); err != nil {
		return err
	}
	forms, err := nameFormsOf(entity.Name)
	if err != nil {
		return enrich.ErrInvalid
	}
	if forms.TokenSet == "" {
		return nil
	}
	if err := tx.deletePosting(tokenSetPostings, forms.TokenSet, entity.ID); err != nil {
		return err
	}
	if err := tx.deletePosting(squashedPostings, forms.Squashed, entity.ID); err != nil {
		return err
	}
	for _, word := range forms.Words {
		if err := tx.deletePosting(wordPostings, word, entity.ID); err != nil {
			return err
		}
	}
	return nil
}

// putNewEntityNamePostings runs for a first-time entity only: existing
// entities already own their postings or are covered by the backfill cursor,
// which keeps a 64-proposal chunk inside the transaction budget.
func (tx *Tx) putNewEntityNamePostings(entity knowledge.Entity) error {
	retired, err := tx.store.libraryNameCursorReader(tx.writer, foldedRetiredID)
	if err != nil {
		return err
	}
	if !retired.Complete {
		if err := tx.putPosting(foldedPostings, foldedNameForm(entity.Name), entity.ID); err != nil {
			return err
		}
	}
	return tx.putNamePostings(entity)
}

// A first current-version writer can prove there are no legacy entities to
// migrate. Do this before publishing its entity order record, in the same
// transaction; existing nonempty Libraries keep their backfill requirement.
func (tx *Tx) initializeEmptyLibraryNameIndex() error {
	cursor, err := tx.store.libraryNameCursorReader(tx.writer, nameTokenCursorID)
	if err != nil || cursor.Complete || cursor.After != "" {
		return err
	}
	entries, _, err := tx.writer.Scan([]byte{groupOrder, kindInferredEntity}, nil, 1, 1024)
	defer clearBoltEntries(entries)
	if err != nil {
		return mapStorePhysicalError(err)
	}
	if len(entries) != 0 {
		return nil
	}
	if err := tx.putIndexValueLocked(inferredMetaNS, foldedRetiredID, mustJSON(libraryNameCursor{Complete: true})); err != nil {
		return err
	}
	return tx.putIndexValueLocked(inferredMetaNS, nameTokenCursorID, mustJSON(libraryNameCursor{Complete: true}))
}

// nameLookup resolves names under one read snapshot and one shared operation
// budget, so no number of queries can exceed the transaction read limit.
type nameLookup struct {
	s      *Store
	reader bolt.Reader
	budget int
	// foldedToo is set while the token index is incomplete: v1 postings still
	// answer for entities the backfill has not reached.
	foldedToo bool
}

func (s *Store) newNameLookup(reader bolt.Reader) (*nameLookup, error) {
	cursor, err := s.libraryNameCursorReader(reader, nameTokenCursorID)
	if err != nil {
		return nil, err
	}
	return &nameLookup{s: s, reader: reader, budget: nameLookupBudget, foldedToo: !cursor.Complete}, nil
}

// scan lists the validated entity IDs of one posting prefix. more reports a
// scan cut short by MaxNameEntries or by the shared budget.
func (l *nameLookup) scan(k namePostingKind, form string) ([]string, bool, error) {
	limit := min(enrich.MaxNameEntries, l.budget-1)
	if limit < 1 {
		return nil, true, nil
	}
	prefix := l.s.postingPrefix(k, form)
	defer clear(prefix)
	entries, more, err := l.reader.Scan(prefix, nil, limit, 64<<10)
	if err != nil {
		return nil, false, mapStorePhysicalError(err)
	}
	defer clearBoltEntries(entries)
	l.budget -= len(entries) + 1
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if len(entry.Value) > 256 {
			return nil, false, ErrCorrupt
		}
		plain, err := l.s.open(entry.Key, k.ns, entry.Value)
		if err != nil {
			return nil, false, ErrCorrupt
		}
		id := string(plain)
		clear(plain)
		expected := l.s.postingKey(k, form, id)
		valid := enrich.IsEntityID(id) && bytes.Equal(expected, entry.Key)
		clear(expected)
		if !valid {
			return nil, false, ErrCorrupt
		}
		ids = append(ids, id)
	}
	return ids, more, nil
}

// exact returns entities whose name equals the query after case, accent,
// possessive, punctuation, word-order and spacing differences: legacy exact
// IDs, token-set postings, squashed postings, and v1 while backfill runs.
func (l *nameLookup) exact(name string) ([]string, bool, error) {
	ids, err := l.s.inferredNameIDs(l.reader, name) // Legacy entries work during backfill.
	if err != nil {
		return nil, false, err
	}
	l.budget--
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}
	more := false
	add := func(k namePostingKind, form string) error {
		if form == "" {
			return nil
		}
		found, cut, err := l.scan(k, form)
		if err != nil {
			return err
		}
		more = more || cut
		for _, id := range found {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		return nil
	}
	if forms, err := nameFormsOf(name); err == nil {
		if err := add(tokenSetPostings, forms.TokenSet); err != nil {
			return nil, false, err
		}
		if err := add(squashedPostings, forms.Squashed); err != nil {
			return nil, false, err
		}
	}
	if l.foldedToo {
		if err := add(foldedPostings, foldedNameForm(name)); err != nil {
			return nil, false, err
		}
	}
	sort.Strings(ids)
	return ids, more, nil
}

// partialCandidates returns the entity IDs holding a word posting for every
// query token. The shortest posting list is the candidate set; membership in
// the others is a point read per candidate, so a common word whose list is cut
// short cannot hide an entry a rarer word identifies. more reports that the
// candidate list itself was cut short, or the shared budget ended.
func (l *nameLookup) partialCandidates(tokens []string) ([]string, bool, error) {
	base, cut := -1, false
	var candidates []string
	for i, token := range tokens {
		ids, truncated, err := l.scan(wordPostings, token)
		if err != nil {
			return nil, false, err
		}
		if len(ids) == 0 && !truncated {
			return nil, false, nil
		}
		// Prefer a complete list, then the shorter one.
		if base < 0 || cut && !truncated || cut == truncated && len(ids) < len(candidates) {
			base, cut, candidates = i, truncated, ids
		}
	}
	var held []string
	for _, id := range candidates {
		every := true
		for i, token := range tokens {
			if i == base {
				continue
			}
			if l.budget < 1 {
				return held, true, nil
			}
			l.budget--
			key := l.s.postingKey(wordPostings, token, id)
			value, found, err := l.reader.Get(key)
			clear(key)
			clear(value)
			if err != nil {
				return nil, false, mapStorePhysicalError(err)
			}
			if !found {
				every = false
				break
			}
		}
		if every {
			held = append(held, id)
		}
	}
	return held, cut, nil
}

// Name-part matching is recall-only and has its own bounds. One call reads at
// most MaxReadTransactionOps physical operations: scanBudget covers posting
// scans and one entity record per candidate; the rest is reserved for loading
// admitted entities, each charged by its worst-case page and source reads
// before it is read. A call that cannot finish within a bound reports more.
const (
	partReadLimit  = bolt.MaxReadTransactionOps
	partScanBudget = 2048
	partLoadBudget = partReadLimit - partScanBudget - 128
	// More ambiguous candidates than this are not returned at all: guessing
	// among many people sharing a first name would be wrong more often than not.
	maxPartCandidates = 8
	maxFactPageReads  = knowledge.MaxFacts/inferredFactPageSize + 1
)

type partCandidate struct {
	id    string
	count int
}

// loadCost bounds the reads loadInferredFactWindow and currentInferredRecord
// perform for one record: fact pages, source pages, and two note lookups per
// source.
func loadCost(record inferredRecord) int {
	return 1 + min(record.FactPageCount, maxFactPageReads) + record.SourcePageCount + 2*(len(record.Sources)+record.SourcePageCount*inferredSourcePageSize)
}

// FindInferredKnowledgeByNamePartsInScope finds entities whose names contain
// every token of a query and cover at least half of the entity's distinct name
// tokens. It never returns whole-name matches the caller already has (exclude)
// and carries no correction authority: callers use it for recall only.
func (s *Store) FindInferredKnowledgeByNamePartsInScope(ctx context.Context, names []string, limit int, appIDs, domains []string, kinds []knowledge.Kind, exclude []string) ([]knowledge.Entity, bool, error) {
	if ctx == nil || len(names) == 0 || len(names) > maxInferredKnowledgeNameBatch || limit < 1 || limit > maxInferredKnowledgeNameBatch {
		return nil, false, enrich.ErrInvalid
	}
	var queries [][]string
	querySeen := map[string]bool{}
	for _, name := range names {
		tokens, err := search.NormalizeTokens(name, knowledge.MaxTextBytes)
		if err != nil || len(name) > knowledge.MaxTextBytes {
			return nil, false, enrich.ErrInvalid
		}
		key := joinTokens(tokens, " ")
		if key == "" || querySeen[key] {
			continue
		}
		querySeen[key] = true
		queries = append(queries, tokens)
	}
	var entities []knowledge.Entity
	hasMore := false
	err := s.viewWithReadLimit(ctx, partReadLimit, func(reader bolt.Reader) error {
		lookup := &nameLookup{s: s, reader: reader, budget: partScanBudget}
		type meta struct {
			found       bool
			count       int
			kind        knowledge.Kind
			app, domain string
		}
		metas := map[string]meta{}
		qualified := map[string]bool{}
		var ordered []string
		records := map[string]inferredRecord{}
		for _, tokens := range queries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(tokens) > maxWordPostingTokens || slices.ContainsFunc(tokens, func(token string) bool { return utf8.RuneCountInString(token) < minWordPostingRunes }) {
				continue // Such words are never indexed.
			}
			ids, more, err := lookup.partialCandidates(tokens)
			if err != nil {
				return err
			}
			hasMore = hasMore || more
			var kept []partCandidate
			for _, id := range ids {
				if slices.Contains(exclude, id) || qualified[id] {
					continue
				}
				m, ok := metas[id]
				if !ok {
					if lookup.budget < 1 {
						hasMore = true
						break
					}
					lookup.budget--
					record, found, err := s.readInferredRecord(reader, id)
					if err != nil {
						return err
					}
					if !found {
						return ErrCorrupt
					}
					forms, err := nameFormsOf(record.Entity.Name)
					if err != nil {
						return ErrCorrupt
					}
					m = meta{found: true, count: forms.Distinct, kind: record.Entity.Kind}
					if record.Entity.Inference != nil {
						m.app, m.domain = record.Entity.Inference.AppID, record.Entity.Inference.Domain
					}
					metas[id] = m
					records[id] = record
				}
				if 2*len(tokens) < m.count || len(kinds) > 0 && !slices.Contains(kinds, m.kind) ||
					len(appIDs) > 0 && !slices.Contains(appIDs, m.app) || len(domains) > 0 && !slices.Contains(domains, m.domain) {
					continue
				}
				kept = append(kept, partCandidate{id, m.count})
			}
			sort.Slice(kept, func(i, j int) bool {
				if kept[i].count != kept[j].count {
					return kept[i].count < kept[j].count
				}
				return kept[i].id < kept[j].id
			})
			if len(kept) > maxPartCandidates {
				// Too many entries share this name part to tell which is meant.
				// Only this query is dropped: another part of the same question
				// may still name one entry unambiguously.
				hasMore = true
				continue
			}
			for _, c := range kept {
				qualified[c.id] = true
				ordered = append(ordered, c.id)
			}
		}
		loadBudget := partLoadBudget
		for _, id := range ordered {
			if err := ctx.Err(); err != nil {
				return err
			}
			record := records[id]
			cost := loadCost(record)
			if cost > loadBudget {
				hasMore = true
				return nil
			}
			loadBudget -= cost
			if err := s.loadInferredFactWindow(reader, &record); err != nil {
				return err
			}
			if err := s.currentInferredRecord(reader, &record); err != nil {
				return err
			}
			if len(record.Entity.Facts) == 0 {
				continue
			}
			if len(entities) == limit {
				hasMore = true
				return nil
			}
			entities = append(entities, decorateInferredEntity(record))
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return entities, hasMore, nil
}

func (s *Store) libraryNameCursorReader(reader bolt.Reader, id string) (libraryNameCursor, error) {
	b, found, err := s.readIndexValueReader(reader, inferredMetaNS, id, 256)
	if err != nil || !found {
		return libraryNameCursor{}, err
	}
	defer clear(b)
	var cursor libraryNameCursor
	if json.Unmarshal(b, &cursor) != nil || cursor.After != "" && !enrich.IsEntityID(cursor.After) {
		return libraryNameCursor{}, ErrCorrupt
	}
	return cursor, nil
}

func libraryEntitiesExist(reader bolt.Reader) (bool, error) {
	entries, _, err := reader.Scan([]byte{groupOrder, kindInferredEntity}, nil, 1, 1024)
	defer clearBoltEntries(entries)
	if err != nil {
		return false, mapStorePhysicalError(err)
	}
	return len(entries) > 0, nil
}

// Missing metadata schedules backfill without adding work to store open.
// Pending covers both the token backfill and the v1 retirement sweep.
func (s *Store) LibraryNameIndexPending(ctx context.Context) (bool, error) {
	var pending bool
	err := s.view(ctx, func(reader bolt.Reader) error {
		cursor, err := s.libraryNameCursorReader(reader, nameTokenCursorID)
		if err != nil {
			return err
		}
		if !cursor.Complete {
			pending = true
			if cursor.After == "" {
				// Empty Libraries have no incomplete lookup coverage. In particular,
				// do not suppress a general answer while an unused index is absent.
				pending, err = libraryEntitiesExist(reader)
			}
			return err
		}
		retired, err := s.libraryNameCursorReader(reader, foldedRetiredID)
		if err != nil || retired.Complete {
			return err
		}
		entries, _, err := reader.Scan([]byte{groupOrder, kindFoldedInferredName}, nil, 1, 1024)
		defer clearBoltEntries(entries)
		pending = len(entries) > 0
		if err != nil {
			return mapStorePhysicalError(err)
		}
		return nil
	})
	return pending, err
}

// IndexPendingLibraryNames commits one entity (or one bounded v1 retirement
// batch) and its cursor per transaction. It is model-free and resumes from the
// authenticated cursors after a restart.
func (s *Store) IndexPendingLibraryNames(ctx context.Context, maxBatches int) (bool, error) {
	if ctx == nil || maxBatches < 1 || maxBatches > 32 {
		return false, enrich.ErrInvalid
	}
	for round := 0; round < maxBatches; round++ {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		done := false
		err := s.inferredUpdate(ctx, func(tx *Tx) error {
			cursor, err := s.libraryNameCursorReader(tx.writer, nameTokenCursorID)
			if err != nil {
				return err
			}
			if !cursor.Complete {
				return tx.backfillOneEntityName(cursor)
			}
			retired, err := s.libraryNameCursorReader(tx.writer, foldedRetiredID)
			if err != nil {
				return err
			}
			if retired.Complete {
				done = true
				return nil
			}
			done, err = tx.retireFoldedPostings()
			return err
		})
		if err != nil {
			return true, err
		}
		if done {
			return false, nil
		}
	}
	return s.LibraryNameIndexPending(ctx)
}

func (tx *Tx) backfillOneEntityName(cursor libraryNameCursor) error {
	s := tx.store
	var after []byte
	if cursor.After != "" {
		after = s.logicalKey(groupOrder, kindInferredEntity, inferredEntityNS, cursor.After)
	}
	defer clear(after)
	entries, more, err := tx.writer.Scan([]byte{groupOrder, kindInferredEntity}, after, 1, 1024)
	if err != nil {
		return mapStorePhysicalError(err)
	}
	defer clearBoltEntries(entries)
	for _, entry := range entries {
		order, err := s.decodeInferredOrder(kindInferredEntity, entry.Key, entry.Value)
		if err != nil {
			return err
		}
		record, found, err := s.readInferredRecord(tx.writer, order.ID)
		if err != nil {
			return err
		}
		if !found {
			return ErrCorrupt
		}
		if err := tx.putNamePostings(record.Entity); err != nil {
			return err
		}
		cursor.After = order.ID
	}
	cursor.Complete = !more
	return tx.putIndexValueLocked(inferredMetaNS, nameTokenCursorID, mustJSON(cursor))
}

// retireFoldedPostings removes v1 postings by scanning their kind prefix: v1
// keys depend on fold rules that may have changed since they were written, so
// they are never recomputed from names.
func (tx *Tx) retireFoldedPostings() (bool, error) {
	entries, more, err := tx.writer.Scan([]byte{groupOrder, kindFoldedInferredName}, nil, foldedRetireBatch, 64<<10)
	if err != nil {
		return false, mapStorePhysicalError(err)
	}
	defer clearBoltEntries(entries)
	for _, entry := range entries {
		if err := tx.deleteKnownEntryLocked(entry.Key, entry.Value); err != nil {
			return false, err
		}
	}
	if more {
		return false, nil
	}
	return true, tx.putIndexValueLocked(inferredMetaNS, foldedRetiredID, mustJSON(libraryNameCursor{Complete: true}))
}
