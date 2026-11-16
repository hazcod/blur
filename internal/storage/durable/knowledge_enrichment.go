package durable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const (
	inferredEntityNS     = "lib-entity-v1"
	inferredSourceNS     = "lib-source-v1"
	inferredNameNS       = "lib-name-v1"
	inferredMetaNS       = "lib-meta-v1"
	inferredCheckpointID = "backfill"
	// Physical order prefixes must be distinct: namespace hashes do not provide
	// a scan prefix. kindIndex's order prefix belongs to semantic vectors.
	kindInferredEntity byte = 0x05
	kindInferredSource byte = 0x06
)

type inferredRecord struct {
	Entity  knowledge.Entity `json:"entity"`
	Sources []string         `json:"sources,omitempty"`
	Muted   []string         `json:"muted,omitempty"`
}

type inferredOrder struct {
	ID    string `json:"id"`
	Bytes int    `json:"bytes"`
}

var _ enrich.Repository = (*Store)(nil)

// inferredUpdate keeps domain outcomes out of storage's deliberately opaque
// callback error envelope while retaining atomic rollback on every failure.
func (s *Store) inferredUpdate(ctx context.Context, fn func(*Tx) error) error {
	if ctx == nil {
		return enrich.ErrInvalid
	}
	if !s.state.knowledgeMu.TryRLock() {
		return enrich.ErrUnavailable
	}
	defer s.state.knowledgeMu.RUnlock()
	var outcome error
	err := s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		err := fn(tx)
		for _, expected := range []error{enrich.ErrInvalid, enrich.ErrSourceGone, enrich.ErrStale, enrich.ErrSuppressed, enrich.ErrCapacity} {
			if errors.Is(err, expected) {
				outcome = expected
				return &storage.StoreError{Code: storage.Invalid}
			}
		}
		return err
	})
	if err != nil && outcome != nil {
		return outcome
	}
	return err
}

func inferredOrderNamespace(kind byte) string {
	if kind == kindInferredEntity {
		return inferredEntityNS
	}
	return inferredSourceNS
}

func (tx *Tx) putInferredOrder(kind byte, id string, size int) error {
	ns := inferredOrderNamespace(kind)
	key := tx.store.logicalKey(groupOrder, kind, ns, id)
	defer clear(key)
	plain := mustJSON(inferredOrder{ID: id, Bytes: size})
	defer clear(plain)
	envelope, err := tx.store.seal(key, ns, plain)
	if err != nil {
		return err
	}
	defer clear(envelope)
	return tx.writeEntryLocked(key, envelope, true)
}

func (s *Store) decodeInferredOrder(kind byte, key, value []byte) (inferredOrder, error) {
	if len(value) > 512 {
		return inferredOrder{}, ErrCorrupt
	}
	ns := inferredOrderNamespace(kind)
	plain, err := s.open(key, ns, value)
	if err != nil {
		return inferredOrder{}, ErrCorrupt
	}
	defer clear(plain)
	var order inferredOrder
	if json.Unmarshal(plain, &order) != nil || order.Bytes < 1 || order.Bytes > enrich.MaxEntityBytes || kind == kindInferredEntity && !enrich.IsEntityID(order.ID) || kind == kindInferredSource && !validHash(order.ID) {
		return inferredOrder{}, ErrCorrupt
	}
	expected := s.logicalKey(groupOrder, kind, ns, order.ID)
	defer clear(expected)
	if !bytes.Equal(key, expected) {
		return inferredOrder{}, ErrCorrupt
	}
	return order, nil
}

func (s *Store) readInferredRecord(reader bolt.Reader, id string) (inferredRecord, bool, error) {
	encoded, found, err := s.readIndexValueReader(reader, inferredEntityNS, id, enrich.MaxEntityBytes)
	if err != nil || !found {
		return inferredRecord{}, found, err
	}
	defer clear(encoded)
	var record inferredRecord
	if json.Unmarshal(encoded, &record) != nil || record.Entity.ID != id || !enrich.IsEntityID(id) || record.Entity.Validate() != nil || len(record.Sources) > knowledge.MaxFacts || !validSourceHashIDs(record.Sources) || len(record.Muted) > knowledge.MaxFacts || !validSourceHashIDs(record.Muted) {
		return inferredRecord{}, false, ErrCorrupt
	}
	return record, true, nil
}

// Filter only the read view. Immutable superseded note revisions remain saved
// for source inspection and task proof, but no longer support current memory.
func (s *Store) currentInferredRecord(reader bolt.Reader, record *inferredRecord) error {
	current := make(map[string]bool, len(record.Sources))
	for _, id := range record.Sources {
		live, err := s.isCurrentNoteSourceReader(reader, id)
		if err != nil {
			return err
		}
		current[id] = live
	}
	facts := make([]knowledge.Fact, 0, len(record.Entity.Facts))
	for _, fact := range record.Entity.Facts {
		sources := make([]knowledge.Source, 0, len(fact.Sources))
		for _, source := range fact.Sources {
			if source.UserAuthored || current[source.ID] {
				sources = append(sources, source)
			}
		}
		if len(sources) > 0 {
			fact.Sources = sources
			facts = append(facts, fact)
		}
	}
	record.Entity.Facts = facts
	sources := make([]string, 0, len(record.Sources))
	for _, id := range record.Sources {
		if current[id] {
			sources = append(sources, id)
		}
	}
	record.Sources = sources
	record.Entity.ActivitySourceIDs = append([]string(nil), sources...)
	return nil
}

func (s *Store) readEnrichmentProgress(reader bolt.Reader, sourceID string) (enrich.Progress, bool, error) {
	encoded, found, err := s.readIndexValueReader(reader, inferredSourceNS, sourceID, enrich.MaxProgressBytes)
	if err != nil || !found {
		return enrich.Progress{}, found, err
	}
	defer clear(encoded)
	var progress enrich.Progress
	if json.Unmarshal(encoded, &progress) != nil || progress.SourceID != sourceID || !enrich.ValidProgress(progress) {
		return enrich.Progress{}, false, ErrCorrupt
	}
	return progress, true, nil
}

func (s *Store) GetEnrichmentProgress(ctx context.Context, sourceID string) (enrich.Progress, bool, error) {
	if ctx == nil || !validHash(sourceID) {
		return enrich.Progress{}, false, enrich.ErrInvalid
	}
	var progress enrich.Progress
	var found bool
	err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		progress, found, err = s.readEnrichmentProgress(reader, sourceID)
		return err
	})
	return progress, found, err
}

func (tx *Tx) putEnrichmentProgress(progress enrich.Progress) error {
	if !enrich.ValidProgress(progress) {
		return enrich.ErrInvalid
	}
	encoded := mustJSON(progress)
	defer clear(encoded)
	if len(encoded) > enrich.MaxProgressBytes {
		return enrich.ErrCapacity
	}
	if err := tx.putIndexValueLocked(inferredSourceNS, progress.SourceID, encoded); err != nil {
		return err
	}
	return tx.putInferredOrder(kindInferredSource, progress.SourceID, len(encoded))
}

func (s *Store) inferredNameIDs(reader bolt.Reader, name string) ([]string, error) {
	encoded, found, err := s.readIndexValueReader(reader, inferredNameNS, s.knowledgeAliasKey(name), 12<<10)
	if err != nil || !found {
		return nil, err
	}
	defer clear(encoded)
	var ids []string
	if json.Unmarshal(encoded, &ids) != nil || len(ids) > enrich.MaxNameEntries {
		return nil, ErrCorrupt
	}
	for i, id := range ids {
		if !enrich.IsEntityID(id) || i > 0 && ids[i-1] >= id {
			return nil, ErrCorrupt
		}
	}
	return ids, nil
}

func (tx *Tx) putInferredRecord(record inferredRecord, existed bool) error {
	entity := record.Entity
	entity.RelatedIDs = nil // Connections are a view of live shared provenance.
	entity.ActivitySourceIDs = append([]string(nil), record.Sources...)
	record.Entity = entity
	if entity.Validate() != nil || len(record.Sources) > knowledge.MaxFacts || len(record.Muted) > knowledge.MaxFacts {
		return enrich.ErrCapacity
	}
	encoded := mustJSON(record)
	defer clear(encoded)
	if len(encoded) > enrich.MaxEntityBytes {
		return enrich.ErrCapacity
	}
	if !existed {
		ids, err := tx.store.inferredNameIDs(tx.writer, entity.Name)
		if err != nil {
			return err
		}
		if len(ids) >= enrich.MaxNameEntries {
			return enrich.ErrCapacity
		}
		ids = append(ids, entity.ID)
		sort.Strings(ids)
		if err := tx.putIndexValueLocked(inferredNameNS, tx.store.knowledgeAliasKey(entity.Name), mustJSON(ids)); err != nil {
			return err
		}
	}
	if err := tx.putIndexValueLocked(inferredEntityNS, entity.ID, encoded); err != nil {
		return err
	}
	return tx.putInferredOrder(kindInferredEntity, entity.ID, len(encoded))
}

func (tx *Tx) deleteInferredIndex(ns, id string) error {
	key := tx.store.logicalKey(groupRecord, kindIndex, "index:"+ns, id)
	defer clear(key)
	return tx.deleteEntryLocked(key)
}

func (tx *Tx) deleteInferredRecord(record inferredRecord) error {
	ids, err := tx.store.inferredNameIDs(tx.writer, record.Entity.Name)
	if err != nil {
		return err
	}
	updated := removeString(ids, record.Entity.ID)
	if len(updated) == len(ids) {
		return ErrCorrupt
	}
	if len(updated) == 0 {
		err = tx.deleteInferredIndex(inferredNameNS, tx.store.knowledgeAliasKey(record.Entity.Name))
	} else {
		err = tx.putIndexValueLocked(inferredNameNS, tx.store.knowledgeAliasKey(record.Entity.Name), mustJSON(updated))
	}
	if err != nil {
		return err
	}
	if err := tx.deleteInferredIndex(inferredEntityNS, record.Entity.ID); err != nil {
		return err
	}
	key := tx.store.logicalKey(groupOrder, kindInferredEntity, inferredEntityNS, record.Entity.ID)
	defer clear(key)
	return tx.deleteEntryLocked(key)
}

// ApplyEnrichment publishes only validated proposals from one exact immutable
// source span, with a compare-and-swap cursor. Progress and claims commit together.
func (s *Store) ApplyEnrichment(ctx context.Context, chunk enrich.Chunk) error {
	err := s.applyEnrichment(ctx, chunk)
	if !errors.Is(err, enrich.ErrCapacity) {
		return err
	}
	// The first attempt validates the exact source, span, and model cursor
	// before a capacity failure. Retire only obsolete inferred note claims,
	// in separate bounded transactions, then retry the original atomic commit.
	// Interrupted maintenance leaves valid reverse receipts and no new claims.
	retired, repairErr := s.retireSupersededNoteClaims(ctx, chunk)
	if repairErr != nil {
		return repairErr
	}
	if !retired {
		return err
	}
	return s.applyEnrichment(ctx, chunk)
}

func (s *Store) applyEnrichment(ctx context.Context, chunk enrich.Chunk) error {
	if ctx == nil || !validHash(chunk.SourceID) || !validHash(chunk.ModelKey) || chunk.FailureCode != "" && (!chunk.Declined || chunk.FailureCode != enrich.FailureInvalidOutput) {
		return enrich.ErrInvalid
	}
	return s.inferredUpdate(ctx, func(tx *Tx) error {
		source, _, found, err := s.sourceEvidenceReader(tx.writer, chunk.SourceID)
		if err != nil {
			return err
		}
		if !found {
			return enrich.ErrSourceGone
		}
		current, err := s.isCurrentNoteSourceReader(tx.writer, chunk.SourceID)
		if err != nil || !current {
			if err != nil {
				return err
			}
			return enrich.ErrSourceGone
		}
		text := source.Text()
		progress, exists, err := s.readEnrichmentProgress(tx.writer, chunk.SourceID)
		if err != nil {
			return err
		}
		if progress.Suppressed {
			return enrich.ErrSuppressed
		}
		retrying := exists && progress.ModelKey == chunk.ModelKey && progress.Declined && progress.FailureCode == enrich.FailureInvalidOutput && chunk.RetryInvalid
		if chunk.RetryInvalid && !retrying {
			return enrich.ErrStale
		}
		end, next, complete, err := enrich.NextChunk(text, chunk.Start)
		if err != nil || chunk.End != end || chunk.Next != next || chunk.Complete != complete {
			return enrich.ErrStale
		}
		if exists && progress.ModelKey == chunk.ModelKey {
			if progress.Declined && !retrying {
				if chunk.Declined && chunk.FailureCode == progress.FailureCode && chunk.Start == progress.Next && len(chunk.Proposals) == 0 {
					return nil // exact terminal-outcome retry; source/span checked above
				}
				return enrich.ErrStale
			}
			if !chunk.Declined && progress.Next == chunk.Next && progress.Complete == chunk.Complete && chunk.Start < progress.Next {
				return nil
			} // committed retry
			if progress.Next != chunk.Start || progress.Complete {
				return enrich.ErrStale
			}
		} else if chunk.Start != 0 {
			return enrich.ErrStale
		}
		span := strings.TrimSpace(text[chunk.Start:chunk.End])
		if chunk.Declined || source.AppID() == appidentity.BundleID || span == "" {
			if len(chunk.Proposals) > 0 {
				return enrich.ErrInvalid
			}
			if chunk.Declined && (source.AppID() == appidentity.BundleID || span == "") {
				return enrich.ErrInvalid // no model request was necessary for this span
			}
		} else if derive.ValidateSuggestions([]derive.Evidence{{SourceID: chunk.SourceID, Text: span}}, chunk.Proposals) != nil {
			return enrich.ErrInvalid
		}
		if exists && progress.ModelKey != chunk.ModelKey {
			retained, err := tx.pruneInferredClaims(progress, true)
			if err != nil {
				return err
			}
			progress.Entities = retained
		}
		progress.SourceID, progress.ModelKey, progress.Next, progress.Complete = chunk.SourceID, chunk.ModelKey, next, complete
		progress.Declined = chunk.Declined
		progress.FailureCode = chunk.FailureCode
		if chunk.Declined {
			progress.Next, progress.Complete = chunk.Start, false
		}
		for _, proposal := range chunk.Proposals {
			domain := source.Domain()
			// A revision's unique evidence domain is not a new real-world
			// context. All user-authored note documents share the local scope.
			if source.AppID() == "sh.blur.notes" {
				domain = "local"
			}
			id := enrich.EntityID(proposal.Kind, proposal.Name, source.AppID(), domain)
			record, found, err := s.readInferredRecord(tx.writer, id)
			if err != nil {
				return err
			}
			if !found {
				record.Entity = knowledge.Entity{ID: id, Kind: proposal.Kind, Name: proposal.Name, State: knowledge.Proposed, Inference: &knowledge.Inference{AppID: source.AppID(), Domain: domain}}
			}
			muted := s.knowledgeAliasKey("fact:" + proposal.Fact)
			if slices.Contains(record.Muted, muted) {
				continue
			}
			factID := enrich.FactID(chunk.SourceID, proposal.Fact)
			duplicate := false
			for _, fact := range record.Entity.Facts {
				if fact.ID == factID {
					duplicate = true
					break
				}
			}
			if !duplicate {
				record.Entity.Facts = append(record.Entity.Facts, knowledge.Fact{ID: factID, Text: proposal.Fact, State: knowledge.Proposed, Sources: []knowledge.Source{{ID: chunk.SourceID}}})
			}
			if !slices.Contains(record.Sources, chunk.SourceID) {
				record.Sources = append(record.Sources, chunk.SourceID)
				sort.Strings(record.Sources)
			}
			if !slices.Contains(progress.Entities, id) {
				progress.Entities = append(progress.Entities, id)
				sort.Strings(progress.Entities)
			}
			if len(progress.Entities) > enrich.MaxSourceEntities {
				return enrich.ErrCapacity
			}
			if err := tx.putInferredRecord(record, found); err != nil {
				return err
			}
		}
		return tx.putEnrichmentProgress(progress)
	})
}

func (s *Store) ListInferredKnowledge(ctx context.Context, request enrich.PageRequest) (knowledge.Page, error) {
	if ctx == nil || request.Limit < 1 || request.Limit > knowledge.MaxPageSize || request.After != "" && !enrich.IsEntityID(request.After) {
		return knowledge.Page{}, enrich.ErrInvalid
	}
	var after []byte
	if request.After != "" {
		var err error
		after, err = s.cursorKey(groupOrder, kindInferredEntity, inferredEntityNS, request.After)
		if err != nil {
			return knowledge.Page{}, err
		}
		defer clear(after)
	}
	page := knowledge.Page{Entities: []knowledge.Entity{}}
	err := s.view(ctx, func(reader bolt.Reader) error {
		entries, more, err := reader.Scan([]byte{groupOrder, kindInferredEntity}, after, request.Limit, 256<<10)
		if err != nil {
			return mapStorePhysicalError(err)
		}
		defer clearBoltEntries(entries)
		page.HasMore = more
		used := 0
		for _, entry := range entries {
			order, err := s.decodeInferredOrder(kindInferredEntity, entry.Key, entry.Value)
			if err != nil {
				return err
			}
			if used+order.Bytes > knowledge.MaxPageBytes {
				page.HasMore = true
				break
			}
			record, found, err := s.readInferredRecord(reader, order.ID)
			if err != nil {
				return err
			}
			if !found {
				return ErrCorrupt
			}
			page.Next = order.ID
			if err := s.currentInferredRecord(reader, &record); err != nil {
				return err
			}
			if len(record.Entity.Facts) > 0 {
				page.Entities = append(page.Entities, record.Entity)
			}
			used += order.Bytes
		}
		return nil
	})
	if err != nil {
		return knowledge.Page{}, err
	}
	if !page.HasMore {
		page.Next = ""
	}
	return page, nil
}

func (s *Store) connectInferred(reader bolt.Reader, record *inferredRecord) error {
	seen := map[string]bool{}
	for _, sourceID := range record.Sources {
		progress, found, err := s.readEnrichmentProgress(reader, sourceID)
		if err != nil {
			return err
		}
		if !found {
			return ErrCorrupt
		}
		for _, id := range progress.Entities {
			if id != record.Entity.ID {
				seen[id] = true
			}
		}
	}
	for id := range seen {
		record.Entity.RelatedIDs = append(record.Entity.RelatedIDs, id)
	}
	sort.Strings(record.Entity.RelatedIDs)
	return nil
}

func (s *Store) GetInferredKnowledge(ctx context.Context, id string) (knowledge.Entity, bool, error) {
	if ctx == nil || !enrich.IsEntityID(id) {
		return knowledge.Entity{}, false, enrich.ErrInvalid
	}
	var record inferredRecord
	var found bool
	err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		record, found, err = s.readInferredRecord(reader, id)
		if err != nil || !found {
			return err
		}
		if err := s.currentInferredRecord(reader, &record); err != nil {
			return err
		}
		if len(record.Entity.Facts) == 0 {
			record, found = inferredRecord{}, false
			return nil
		}
		return s.connectInferred(reader, &record)
	})
	return record.Entity, found, err
}

func (s *Store) FindInferredKnowledge(ctx context.Context, name string) ([]knowledge.Entity, error) {
	if ctx == nil || len(name) > knowledge.MaxTextBytes || strings.TrimSpace(name) == "" {
		return nil, enrich.ErrInvalid
	}
	entities := []knowledge.Entity{}
	err := s.view(ctx, func(reader bolt.Reader) error {
		ids, err := s.inferredNameIDs(reader, name)
		if err != nil {
			return err
		}
		used := 0
		for _, id := range ids {
			record, found, err := s.readInferredRecord(reader, id)
			if err != nil {
				return err
			}
			if !found {
				return ErrCorrupt
			}
			if err := s.currentInferredRecord(reader, &record); err != nil {
				return err
			}
			if len(record.Entity.Facts) == 0 {
				continue
			}
			used += len(mustJSON(record))
			if used > knowledge.MaxPageBytes {
				return enrich.ErrCapacity
			}
			entities = append(entities, record.Entity)
		}
		return nil
	})
	return entities, err
}

const maxInferredKnowledgeNameBatch = 16

// FindInferredKnowledgeByNames resolves several exact subject-name/alias keys
// under one consistent encrypted-store read snapshot. Results follow the
// caller's query priority, then the durable sorted ID order for each name.
func (s *Store) FindInferredKnowledgeByNames(ctx context.Context, names []string, limit int) ([]knowledge.Entity, bool, error) {
	if ctx == nil || len(names) == 0 || len(names) > maxInferredKnowledgeNameBatch || limit < 1 || limit > maxInferredKnowledgeNameBatch {
		return nil, false, enrich.ErrInvalid
	}
	queries := make([]string, 0, len(names))
	querySeen := make(map[string]bool, len(names))
	for _, name := range names {
		normalized := knowledge.NormalizeExact(name)
		if normalized == "" || len(normalized) > knowledge.MaxTextBytes {
			return nil, false, enrich.ErrInvalid
		}
		if !querySeen[normalized] {
			querySeen[normalized] = true
			queries = append(queries, name)
		}
	}
	entities := make([]knowledge.Entity, 0, limit)
	seen := make(map[string]bool, limit)
	used, hasMore := 0, false
	err := s.viewWithReadLimit(ctx, bolt.MaxTransactionOps, func(reader bolt.Reader) error {
		for _, name := range queries {
			if err := ctx.Err(); err != nil {
				return err
			}
			ids, err := s.inferredNameIDs(reader, name)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if err := ctx.Err(); err != nil {
					return err
				}
				if seen[id] {
					continue
				}
				seen[id] = true
				record, found, err := s.readInferredRecord(reader, id)
				if err != nil {
					return err
				}
				if !found {
					return ErrCorrupt
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
				used += len(mustJSON(record))
				if used > knowledge.MaxPageBytes {
					return enrich.ErrCapacity
				}
				entities = append(entities, record.Entity)
			}
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return entities, hasMore, nil
}

func (tx *Tx) suppressInferredSources(sources []string) error {
	for _, sourceID := range sources {
		progress, found, err := tx.store.readEnrichmentProgress(tx.writer, sourceID)
		if err != nil {
			return err
		}
		if !found {
			return ErrCorrupt
		}
		progress.Suppressed = true
		if err := tx.putEnrichmentProgress(progress); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) changeInferredFact(ctx context.Context, id, factID, text string, dismiss bool) error {
	if ctx == nil || !enrich.IsEntityID(id) || !validOpaqueID(factID) || !dismiss && !enrich.ValidCorrection(text) {
		return enrich.ErrInvalid
	}
	return s.inferredUpdate(ctx, func(tx *Tx) error {
		record, found, err := s.readInferredRecord(tx.writer, id)
		if err != nil {
			return err
		}
		if !found {
			return enrich.ErrInvalid
		}
		for i, fact := range record.Entity.Facts {
			if fact.ID != factID {
				continue
			}
			var sources []string
			for _, source := range fact.Sources {
				if source.ID != "" {
					sources = append(sources, source.ID)
				}
			}
			if err := tx.suppressInferredSources(sources); err != nil {
				return err
			}
			muted := s.knowledgeAliasKey("fact:" + fact.Text)
			if !slices.Contains(record.Muted, muted) {
				record.Muted = append(record.Muted, muted)
				sort.Strings(record.Muted)
			}
			if dismiss {
				record.Entity.Facts = append(record.Entity.Facts[:i], record.Entity.Facts[i+1:]...)
			} else {
				record.Entity.Facts[i] = knowledge.Fact{ID: factID, Text: text, State: knowledge.Confirmed, Sources: []knowledge.Source{{UserAuthored: true}}, Supersedes: append([]string(nil), sources...)}
			}
			return tx.putInferredRecord(record, true)
		}
		return enrich.ErrInvalid
	})
}

func (s *Store) CorrectInferredFact(ctx context.Context, id, factID, text string) error {
	return s.changeInferredFact(ctx, id, factID, text, false)
}
func (s *Store) DismissInferredFact(ctx context.Context, id, factID string) error {
	return s.changeInferredFact(ctx, id, factID, "", true)
}

func (s *Store) DeleteInferredKnowledge(ctx context.Context, id string) error {
	if ctx == nil || !enrich.IsEntityID(id) {
		return enrich.ErrInvalid
	}
	return s.inferredUpdate(ctx, func(tx *Tx) error {
		record, found, err := s.readInferredRecord(tx.writer, id)
		if err != nil {
			return err
		}
		if !found {
			return enrich.ErrInvalid
		}
		for _, sourceID := range record.Sources {
			progress, _, err := s.readEnrichmentProgress(tx.writer, sourceID)
			if err != nil {
				return err
			}
			progress.Entities = removeString(progress.Entities, id)
			progress.Suppressed = true
			if err := tx.putEnrichmentProgress(progress); err != nil {
				return err
			}
		}
		return tx.deleteInferredRecord(record)
	})
}

func (tx *Tx) removeInferredClaims(progress enrich.Progress) error {
	_, err := tx.pruneInferredClaims(progress, false)
	return err
}

// Regeneration can replace proposals, but an explicit confirmation survives.
// Source erasure always passes false and removes every source-backed claim.
// Retained entity IDs stay in the source receipt for later bounded erasure.
func (tx *Tx) pruneInferredClaims(progress enrich.Progress, preserveConfirmed bool) ([]string, error) {
	var retained []string
	for _, id := range progress.Entities {
		record, found, err := tx.store.readInferredRecord(tx.writer, id)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, ErrCorrupt
		}
		facts := record.Entity.Facts[:0]
		keepSource := false
		for _, fact := range record.Entity.Facts {
			remove := false
			for _, source := range fact.Sources {
				if source.ID == progress.SourceID {
					remove = true
				}
			}
			fact.Supersedes = removeString(fact.Supersedes, progress.SourceID)
			if remove && preserveConfirmed && fact.State == knowledge.Confirmed {
				remove, keepSource = false, true
			}
			if !remove {
				facts = append(facts, fact)
			}
		}
		clear(record.Entity.Facts[len(facts):])
		record.Entity.Facts = facts
		if keepSource {
			retained = append(retained, id)
		} else {
			record.Sources = removeString(record.Sources, progress.SourceID)
		}
		if len(facts) == 0 && len(record.Sources) == 0 {
			if err := tx.deleteInferredRecord(record); err != nil {
				return nil, err
			}
		} else {
			if len(record.Sources) == 0 {
				record.Entity.Inference = nil
			}
			if err := tx.putInferredRecord(record, true); err != nil {
				return nil, err
			}
		}
	}
	return retained, nil
}

// Called inside the same source-deletion transaction, before deleting primary
// evidence. Reverse provenance bounds this work independently of Library size.
func (tx *Tx) removeEnrichmentSource(sourceID string) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	progress, found, err := tx.store.readEnrichmentProgress(tx.writer, sourceID)
	if err != nil {
		return err
	}
	if found {
		if err := tx.removeInferredClaims(progress); err != nil {
			return err
		}
		if err := tx.deleteInferredIndex(inferredSourceNS, sourceID); err != nil {
			return err
		}
		key := tx.store.logicalKey(groupOrder, kindInferredSource, inferredSourceNS, sourceID)
		err := tx.deleteEntryLocked(key)
		clear(key)
		if err != nil {
			return err
		}
	}
	checkpoint, exists, err := tx.store.readEnrichmentCheckpoint(tx.writer)
	if err != nil {
		return err
	}
	if exists && (checkpoint.After == sourceID || found && progress.Declined && progress.ModelKey == checkpoint.ModelKey && checkpoint.CoverageLimited) {
		checkpoint.After = ""
		checkpoint.Complete = false
		checkpoint.CoverageLimited = false // recomputed from live receipts on repair
		checkpoint.CaptureAfter = ""
		checkpoint.CaptureComplete = false
		return tx.putIndexValueLocked(inferredMetaNS, inferredCheckpointID, mustJSON(checkpoint))
	}
	return nil
}

func (s *Store) readEnrichmentCheckpoint(reader bolt.Reader) (enrich.Checkpoint, bool, error) {
	encoded, found, err := s.readIndexValueReader(reader, inferredMetaNS, inferredCheckpointID, 512)
	if err != nil || !found {
		return enrich.Checkpoint{}, found, err
	}
	defer clear(encoded)
	var checkpoint enrich.Checkpoint
	if json.Unmarshal(encoded, &checkpoint) != nil || !validHash(checkpoint.ModelKey) || checkpoint.After != "" && !validHash(checkpoint.After) || !storage.ValidCaptureEvidenceCursor(checkpoint.CaptureAfter) {
		return enrich.Checkpoint{}, false, ErrCorrupt
	}
	return checkpoint, true, nil
}
func (s *Store) GetEnrichmentCheckpoint(ctx context.Context) (enrich.Checkpoint, bool, error) {
	var checkpoint enrich.Checkpoint
	var found bool
	err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		checkpoint, found, err = s.readEnrichmentCheckpoint(reader)
		return err
	})
	return checkpoint, found, err
}
func (s *Store) PutEnrichmentCheckpoint(ctx context.Context, checkpoint enrich.Checkpoint) error {
	if ctx == nil || !validHash(checkpoint.ModelKey) || checkpoint.After != "" && !validHash(checkpoint.After) || !storage.ValidCaptureEvidenceCursor(checkpoint.CaptureAfter) {
		return enrich.ErrInvalid
	}
	return s.inferredUpdate(ctx, func(tx *Tx) error {
		if checkpoint.After != "" {
			key := s.logicalKey(groupRecord, kindObservation, "", checkpoint.After)
			found, err := tx.writer.Has(key)
			clear(key)
			if err != nil {
				return mapStorePhysicalError(err)
			}
			if !found {
				checkpoint.After = ""
				checkpoint.Complete = false
			}
		}
		return tx.putIndexValueLocked(inferredMetaNS, inferredCheckpointID, mustJSON(checkpoint))
	})
}

// Caller holds knowledgeMu exclusively. One entity/source per transaction keeps
// erasure bounded even with a large Library and retained user corrections.
func (s *Store) deleteAllInferredKnowledge(ctx context.Context) error {
	for _, kind := range []byte{kindInferredEntity, kindInferredSource} {
		for {
			var order inferredOrder
			found := false
			err := s.view(ctx, func(reader bolt.Reader) error {
				entries, _, err := reader.Scan([]byte{groupOrder, kind}, nil, 1, 1024)
				if err != nil {
					return mapStorePhysicalError(err)
				}
				defer clearBoltEntries(entries)
				if len(entries) == 0 {
					return nil
				}
				order, err = s.decodeInferredOrder(kind, entries[0].Key, entries[0].Value)
				found = err == nil
				return err
			})
			if err != nil {
				return err
			}
			if !found {
				break
			}
			err = s.Update(ctx, func(tx *Tx) error {
				tx.mu.Lock()
				defer tx.mu.Unlock()
				if kind == kindInferredEntity {
					record, exists, err := s.readInferredRecord(tx.writer, order.ID)
					if err != nil {
						return err
					}
					if !exists {
						return ErrCorrupt
					}
					return tx.deleteInferredRecord(record)
				}
				if err := tx.deleteInferredIndex(inferredSourceNS, order.ID); err != nil {
					return err
				}
				key := s.logicalKey(groupOrder, kind, inferredSourceNS, order.ID)
				defer clear(key)
				return tx.deleteEntryLocked(key)
			})
			if err != nil {
				return err
			}
		}
	}
	return s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		_, found, err := s.readEnrichmentCheckpoint(tx.writer)
		if err != nil || !found {
			return err
		}
		return tx.deleteInferredIndex(inferredMetaNS, inferredCheckpointID)
	})
}
