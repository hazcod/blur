package durable

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"unicode/utf8"

	"github.com/nielsgroen/blur/internal/knowledge"
	"github.com/nielsgroen/blur/internal/storage"
	"github.com/nielsgroen/blur/internal/storage/physical/bolt"
)

const (
	knowledgeEntityNS    = "know-ent"
	knowledgeAliasNS     = "know-alias"
	knowledgeMetaNS      = "know-meta"
	knowledgeMergeNS     = "know-merge"
	knowledgeDirectoryID = "entities"
)

func (s *Store) knowledgeAliasKey(alias string) string {
	mac := hmac.New(sha256.New, s.state.idKey)
	_, _ = io.WriteString(mac, "blur.knowledge.exact.v1\x00")
	_, _ = io.WriteString(mac, knowledge.NormalizeExact(alias))
	sum := mac.Sum(nil)
	defer clear(sum)
	return hex.EncodeToString(sum)
}

func (s *Store) PutKnowledgeEntity(ctx context.Context, entity knowledge.Entity) error {
	if ctx == nil || entity.Validate() != nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	encoded, err := json.Marshal(entity)
	if err != nil || len(encoded) > knowledge.MaxEntityBytes {
		clear(encoded)
		return &storage.StoreError{Code: storage.Invalid}
	}
	defer clear(encoded)
	return s.Update(ctx, func(tx *Tx) error {
		ids, err := tx.knowledgeDirectory()
		if err != nil {
			return err
		}
		old, exists, err := tx.knowledgeEntity(entity.ID)
		if err != nil {
			return err
		}
		if !exists && len(ids) >= knowledge.MaxEntities {
			return &storage.StoreError{Code: storage.Invalid}
		}
		if old.ID != "" && (old.State == knowledge.Merged || entity.State == knowledge.Merged && old.MergedInto != entity.MergedInto) {
			return &storage.StoreError{Code: storage.Invalid}
		}
		if err := tx.checkKnowledgeAliases(entity, entity.ID); err != nil {
			return err
		}
		if exists {
			if err := tx.removeKnowledgeAliases(old, entity.ID); err != nil {
				return err
			}
		} else {
			ids = append(ids, entity.ID)
			sort.Strings(ids)
		}
		if err := tx.putKnowledgeAliases(entity); err != nil {
			return err
		}
		if err := tx.PutIndexRecord(knowledgeEntityNS, entity.ID, encoded); err != nil {
			return err
		}
		return tx.PutIndexRecord(knowledgeMetaNS, knowledgeDirectoryID, mustJSON(ids))
	})
}

func (s *Store) GetKnowledgeEntity(ctx context.Context, id string) (knowledge.Entity, bool, error) {
	if ctx == nil || !validOpaqueID(id) {
		return knowledge.Entity{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	var entity knowledge.Entity
	found, err := s.readKnowledgeEntity(ctx, id, &entity)
	if err != nil {
		return knowledge.Entity{}, false, err
	}
	if !found {
		return knowledge.Entity{}, false, nil
	}
	return knowledge.Clone(entity), true, nil
}

// DeleteKnowledgeEntity removes a user-selected entity and its exact lookup
// entries. Merge participants must first be unmerged to preserve lineage.
func (s *Store) DeleteKnowledgeEntity(ctx context.Context, id string) error {
	if ctx == nil || !validOpaqueID(id) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	return s.Update(ctx, func(tx *Tx) error {
		entity, found, err := tx.knowledgeEntity(id)
		if err != nil {
			return err
		}
		if !found || entity.State == knowledge.Merged {
			return &storage.StoreError{Code: storage.Invalid}
		}
		ids, err := tx.knowledgeDirectory()
		if err != nil {
			return err
		}
		for _, otherID := range ids {
			other, ok, err := tx.knowledgeEntity(otherID)
			if err != nil {
				return err
			}
			if ok && other.MergedInto == id {
				return &storage.StoreError{Code: storage.Invalid}
			}
		}
		if err := tx.removeKnowledgeAliases(entity, id); err != nil {
			return err
		}
		if err := tx.deleteKnowledgeRecord(knowledgeEntityNS, id); err != nil {
			return err
		}
		ids = removeString(ids, id)
		return tx.PutIndexRecord(knowledgeMetaNS, knowledgeDirectoryID, mustJSON(ids))
	})
}

func (s *Store) ListKnowledge(ctx context.Context, request knowledge.PageRequest) (knowledge.Page, error) {
	if ctx == nil || request.Limit < 1 || request.Limit > knowledge.MaxPageSize || request.After != "" && !validOpaqueID(request.After) {
		return knowledge.Page{}, &storage.StoreError{Code: storage.Invalid}
	}
	ids, err := s.readKnowledgeDirectory(ctx)
	if err != nil {
		return knowledge.Page{}, err
	}
	start := sort.SearchStrings(ids, request.After)
	if request.After != "" {
		for start < len(ids) && ids[start] <= request.After {
			start++
		}
	}
	page := knowledge.Page{Entities: make([]knowledge.Entity, 0, request.Limit)}
	usedBytes := 0
	index := start
	for index < len(ids) && len(page.Entities) < request.Limit {
		id := ids[index]
		e, ok, err := s.GetKnowledgeEntity(ctx, id)
		if err != nil {
			return knowledge.Page{}, err
		}
		if ok {
			encoded, err := json.Marshal(e)
			if err != nil {
				return knowledge.Page{}, ErrCorrupt
			}
			if len(encoded) > knowledge.MaxPageBytes-usedBytes {
				clear(encoded)
				if len(page.Entities) == 0 {
					return knowledge.Page{}, &storage.StoreError{Code: storage.Invalid}
				}
				break
			}
			usedBytes += len(encoded)
			clear(encoded)
			page.Entities = append(page.Entities, e)
		}
		index++
	}
	page.HasMore = index < len(ids)
	if page.HasMore && len(page.Entities) > 0 {
		page.Next = page.Entities[len(page.Entities)-1].ID
	}
	return page, nil
}

func (s *Store) SearchKnowledgeExact(ctx context.Context, query string) (knowledge.Entity, bool, error) {
	if ctx == nil || len(query) > knowledge.MaxTextBytes || !utf8.ValidString(query) || knowledge.NormalizeExact(query) == "" {
		return knowledge.Entity{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	if s == nil || s.state == nil {
		return knowledge.Entity{}, false, &storage.StoreError{Code: storage.Closed}
	}
	id := s.knowledgeAliasKey(query)
	value, found, err := s.readKnowledgeRecord(ctx, knowledgeAliasNS, id)
	if err != nil || !found {
		return knowledge.Entity{}, false, err
	}
	if len(value) == 0 || len(value) > 128 || !validOpaqueID(string(value)) {
		clear(value)
		return knowledge.Entity{}, false, ErrCorrupt
	}
	entityID := string(value)
	clear(value)
	return s.GetKnowledgeEntity(ctx, entityID)
}

func (s *Store) QuarantineKnowledgeSource(ctx context.Context, sourceID string) error {
	if ctx == nil || !validHash(sourceID) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	return s.Update(ctx, func(tx *Tx) error {
		ids, err := tx.knowledgeDirectory()
		if err != nil {
			return err
		}
		for _, id := range ids {
			e, ok, err := tx.knowledgeEntity(id)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			changed := false
			facts := e.Facts[:0]
			for _, f := range e.Facts {
				f.Sources, changed = withoutSource(f.Sources, sourceID, changed)
				if len(f.Sources) > 0 {
					facts = append(facts, f)
				} else {
					changed = true
				}
			}
			e.Facts = facts
			loops := e.OpenLoops[:0]
			for _, l := range e.OpenLoops {
				l.Sources, changed = withoutSource(l.Sources, sourceID, changed)
				if len(l.Sources) > 0 {
					loops = append(loops, l)
				} else {
					changed = true
				}
			}
			e.OpenLoops = loops
			for _, sid := range e.ActivitySourceIDs {
				if sid == sourceID {
					changed = true
					break
				}
			}
			if changed {
				filtered := e.ActivitySourceIDs[:0]
				for _, sid := range e.ActivitySourceIDs {
					if sid != sourceID {
						filtered = append(filtered, sid)
					}
				}
				e.ActivitySourceIDs = filtered
				if err := tx.PutIndexRecord(knowledgeEntityNS, id, mustJSON(e)); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func withoutSource(sources []knowledge.Source, id string, changed bool) ([]knowledge.Source, bool) {
	out := sources[:0]
	for _, source := range sources {
		if source.ID == id {
			changed = true
		} else {
			out = append(out, source)
		}
	}
	return out, changed
}

func (s *Store) MergeKnowledge(ctx context.Context, sourceID, targetID string) error {
	if ctx == nil || !validOpaqueID(sourceID) || !validOpaqueID(targetID) || sourceID == targetID {
		return &storage.StoreError{Code: storage.Invalid}
	}
	return s.Update(ctx, func(tx *Tx) error {
		source, sok, err := tx.knowledgeEntity(sourceID)
		if err != nil {
			return err
		}
		target, tok, err := tx.knowledgeEntity(targetID)
		if err != nil {
			return err
		}
		if !sok || !tok || source.State == knowledge.Merged || target.State == knowledge.Merged || source.Kind != target.Kind {
			return &storage.StoreError{Code: storage.Invalid}
		}
		if _, found, err := tx.readKnowledgeRecordLocked(knowledgeMergeNS, sourceID); err != nil {
			return err
		} else if found {
			return &storage.StoreError{Code: storage.Invalid}
		}
		snapshot := mergeSnapshot{Source: source, Target: target}
		if err := tx.PutIndexRecord(knowledgeMergeNS, sourceID, mustJSON(snapshot)); err != nil {
			return err
		}
		if err := tx.removeKnowledgeAliases(source, source.ID); err != nil {
			return err
		}
		if err := tx.removeKnowledgeAliases(target, target.ID); err != nil {
			return err
		}
		target = mergedKnowledgeTarget(source, target)
		source.State = knowledge.Merged
		source.MergedInto = target.ID
		if err := tx.checkKnowledgeAliases(target, target.ID); err != nil {
			return err
		}
		if err := tx.putKnowledgeAliases(target); err != nil {
			return err
		}
		if err := tx.PutIndexRecord(knowledgeEntityNS, target.ID, mustJSON(target)); err != nil {
			return err
		}
		return tx.PutIndexRecord(knowledgeEntityNS, source.ID, mustJSON(source))
	})
}

func (s *Store) UnmergeKnowledge(ctx context.Context, sourceID, targetID string) error {
	if ctx == nil || !validOpaqueID(sourceID) || !validOpaqueID(targetID) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	return s.Update(ctx, func(tx *Tx) error {
		stored, found, err := tx.readKnowledgeRecordLocked(knowledgeMergeNS, sourceID)
		if err != nil {
			return err
		}
		if !found {
			return &storage.StoreError{Code: storage.Invalid}
		}
		var snapshot mergeSnapshot
		if json.Unmarshal(stored, &snapshot) != nil || snapshot.Source.ID != sourceID || snapshot.Target.ID != targetID {
			return ErrCorrupt
		}
		clear(stored)
		currentTarget, ok, err := tx.knowledgeEntity(targetID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrCorrupt
		}
		projection := mergedKnowledgeTarget(snapshot.Source, snapshot.Target)
		currentJSON, currentErr := json.Marshal(currentTarget)
		projectionJSON, projectionErr := json.Marshal(projection)
		if currentErr != nil || projectionErr != nil || !hmac.Equal(currentJSON, projectionJSON) {
			clear(currentJSON)
			clear(projectionJSON)
			return &storage.StoreError{Code: storage.Invalid}
		}
		clear(currentJSON)
		clear(projectionJSON)
		if err := tx.removeKnowledgeAliases(currentTarget, targetID); err != nil {
			return err
		}
		if err := tx.checkKnowledgeAliases(snapshot.Source, sourceID); err != nil {
			return err
		}
		if err := tx.checkKnowledgeAliases(snapshot.Target, targetID); err != nil {
			return err
		}
		if err := tx.putKnowledgeAliases(snapshot.Target); err != nil {
			return err
		}
		if err := tx.putKnowledgeAliases(snapshot.Source); err != nil {
			return err
		}
		if err := tx.PutIndexRecord(knowledgeEntityNS, targetID, mustJSON(snapshot.Target)); err != nil {
			return err
		}
		if err := tx.PutIndexRecord(knowledgeEntityNS, sourceID, mustJSON(snapshot.Source)); err != nil {
			return err
		}
		return tx.deleteKnowledgeRecord(knowledgeMergeNS, sourceID)
	})
}

type mergeSnapshot struct {
	Source knowledge.Entity `json:"source"`
	Target knowledge.Entity `json:"target"`
}

func (tx *Tx) knowledgeDirectory() ([]string, error) {
	b, found, err := tx.readKnowledgeRecordLocked(knowledgeMetaNS, knowledgeDirectoryID)
	if err != nil {
		return nil, err
	}
	if !found {
		return []string{}, nil
	}
	defer clear(b)
	var ids []string
	if json.Unmarshal(b, &ids) != nil || len(ids) > knowledge.MaxEntities {
		return nil, ErrCorrupt
	}
	for i, id := range ids {
		if !validOpaqueID(id) || i > 0 && ids[i-1] >= id {
			return nil, ErrCorrupt
		}
	}
	return ids, nil
}
func (tx *Tx) knowledgeEntity(id string) (knowledge.Entity, bool, error) {
	b, found, err := tx.readKnowledgeRecordLocked(knowledgeEntityNS, id)
	if err != nil || !found {
		return knowledge.Entity{}, found, err
	}
	defer clear(b)
	var e knowledge.Entity
	if json.Unmarshal(b, &e) != nil || e.ID != id || e.Validate() != nil {
		return knowledge.Entity{}, false, ErrCorrupt
	}
	return e, true, nil
}
func (tx *Tx) checkKnowledgeAliases(e knowledge.Entity, except string) error {
	for _, alias := range knowledgeNames(e) {
		id := tx.store.knowledgeAliasKey(alias)
		b, found, err := tx.readKnowledgeRecordLocked(knowledgeAliasNS, id)
		if err != nil {
			return err
		}
		if found {
			owner := string(b)
			clear(b)
			if owner != except {
				return &storage.StoreError{Code: storage.Duplicate}
			}
		}
	}
	return nil
}
func (tx *Tx) putKnowledgeAliases(e knowledge.Entity) error {
	for _, alias := range knowledgeNames(e) {
		id := tx.store.knowledgeAliasKey(alias)
		if err := tx.PutIndexRecord(knowledgeAliasNS, id, []byte(e.ID)); err != nil {
			return err
		}
	}
	return nil
}
func (tx *Tx) removeKnowledgeAliases(e knowledge.Entity, owner string) error {
	for _, alias := range knowledgeNames(e) {
		id := tx.store.knowledgeAliasKey(alias)
		b, found, err := tx.readKnowledgeRecordLocked(knowledgeAliasNS, id)
		if err != nil {
			return err
		}
		if found {
			if string(b) == owner {
				key := tx.store.logicalKey(groupRecord, kindIndex, "index:"+knowledgeAliasNS, id)
				tx.mu.Lock()
				err = tx.deleteEntryLocked(key)
				tx.mu.Unlock()
				clear(key)
				clear(b)
				if err != nil {
					return err
				}
			} else {
				clear(b)
			}
		}
	}
	return nil
}
func (tx *Tx) deleteKnowledgeRecord(namespace, id string) error {
	key := tx.store.logicalKey(groupRecord, kindIndex, "index:"+namespace, id)
	defer clear(key)
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.checkLocked(); err != nil {
		return tx.failLocked(err)
	}
	return tx.deleteEntryLocked(key)
}
func knowledgeNames(e knowledge.Entity) []string {
	names := append([]string{e.Name}, e.Aliases...)
	return names
}

func (tx *Tx) readKnowledgeRecordLocked(namespace, id string) ([]byte, bool, error) {
	key := tx.store.logicalKey(groupRecord, kindIndex, "index:"+namespace, id)
	defer clear(key)
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.checkLocked(); err != nil {
		return nil, false, err
	}
	stored, found, err := tx.writer.Get(key)
	if err != nil {
		return nil, false, mapStorePhysicalError(err)
	}
	if !found {
		return nil, false, nil
	}
	if len(stored) > knowledgeStoredLimit(namespace) {
		clear(stored)
		return nil, false, ErrCorrupt
	}
	plain, err := tx.store.open(key, "index:"+namespace, stored)
	clear(stored)
	if err != nil {
		return nil, false, ErrCorrupt
	}
	if len(plain) < 5 || int(binaryBigEndian(plain[1:5])) != len(plain)-5 {
		clear(plain)
		return nil, false, ErrCorrupt
	}
	out := append([]byte(nil), plain[5:]...)
	clear(plain)
	return out, true, nil
}

func (s *Store) readKnowledgeRecord(ctx context.Context, namespace, id string) ([]byte, bool, error) {
	if s == nil || s.state == nil {
		return nil, false, &storage.StoreError{Code: storage.Closed}
	}
	key := s.logicalKey(groupRecord, kindIndex, "index:"+namespace, id)
	defer clear(key)
	var out []byte
	var found bool
	err := s.view(ctx, func(r bolt.Reader) error {
		stored, ok, err := r.Get(key)
		if err != nil {
			return mapStorePhysicalError(err)
		}
		if !ok {
			return nil
		}
		found = true
		if len(stored) > knowledgeStoredLimit(namespace) {
			clear(stored)
			return ErrCorrupt
		}
		plain, err := s.open(key, "index:"+namespace, stored)
		clear(stored)
		if err != nil {
			return ErrCorrupt
		}
		defer clear(plain)
		if len(plain) < 5 || int(binaryBigEndian(plain[1:5])) != len(plain)-5 {
			return ErrCorrupt
		}
		out = append([]byte(nil), plain[5:]...)
		return nil
	})
	if err != nil {
		clear(out)
		return nil, false, err
	}
	return out, found, nil
}

func knowledgeStoredLimit(namespace string) int {
	const overhead = storedValueHeader + 5 + 1 + 12 + 16
	switch namespace {
	case knowledgeEntityNS:
		return knowledge.MaxEntityBytes + overhead
	case knowledgeAliasNS:
		return 128 + overhead
	case knowledgeMetaNS:
		return knowledge.MaxEntities*130 + overhead
	case knowledgeMergeNS:
		return 2*knowledge.MaxEntityBytes + 1024 + overhead
	default:
		return 4 << 20
	}
}

func (s *Store) readKnowledgeEntity(ctx context.Context, id string, dst *knowledge.Entity) (bool, error) {
	b, found, err := s.readKnowledgeRecord(ctx, knowledgeEntityNS, id)
	if err != nil || !found {
		return found, err
	}
	defer clear(b)
	if json.Unmarshal(b, dst) != nil || dst.ID != id || dst.Validate() != nil {
		return false, ErrCorrupt
	}
	return true, nil
}
func (s *Store) readKnowledgeDirectory(ctx context.Context) ([]string, error) {
	b, found, err := s.readKnowledgeRecord(ctx, knowledgeMetaNS, knowledgeDirectoryID)
	if err != nil {
		return nil, err
	}
	if !found {
		return []string{}, nil
	}
	defer clear(b)
	var ids []string
	if json.Unmarshal(b, &ids) != nil || len(ids) > knowledge.MaxEntities {
		return nil, ErrCorrupt
	}
	for i, id := range ids {
		if !validOpaqueID(id) || i > 0 && ids[i-1] >= id {
			return nil, ErrCorrupt
		}
	}
	return ids, nil
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func appendUnique(a []string, more ...string) []string {
	seen := map[string]bool{}
	for _, v := range a {
		seen[v] = true
	}
	for _, v := range more {
		if v != "" && !seen[v] {
			a = append(a, v)
			seen[v] = true
		}
	}
	sort.Strings(a)
	return a
}
func removeString(a []string, v string) []string {
	out := a[:0]
	for _, x := range a {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}
func appendUniqueFacts(a []knowledge.Fact, more ...knowledge.Fact) []knowledge.Fact {
	seen := map[string]bool{}
	for _, v := range a {
		seen[v.ID] = true
	}
	for _, v := range more {
		if !seen[v.ID] {
			a = append(a, v)
			seen[v.ID] = true
		}
	}
	return a
}
func appendUniqueLoops(a []knowledge.OpenLoop, more ...knowledge.OpenLoop) []knowledge.OpenLoop {
	seen := map[string]bool{}
	for _, v := range a {
		seen[v.ID] = true
	}
	for _, v := range more {
		if !seen[v.ID] {
			a = append(a, v)
			seen[v.ID] = true
		}
	}
	return a
}

func mergedKnowledgeTarget(source, target knowledge.Entity) knowledge.Entity {
	target = knowledge.Clone(target)
	target.Aliases = appendUnique(target.Aliases, append([]string{source.Name}, source.Aliases...)...)
	target.Facts = appendUniqueFacts(target.Facts, source.Facts...)
	target.OpenLoops = appendUniqueLoops(target.OpenLoops, source.OpenLoops...)
	target.RelatedIDs = appendUnique(target.RelatedIDs, source.RelatedIDs...)
	target.ActivitySourceIDs = appendUnique(target.ActivitySourceIDs, source.ActivitySourceIDs...)
	target.RelatedIDs = appendUnique(target.RelatedIDs, source.ID)
	target.RelatedIDs = removeString(target.RelatedIDs, target.ID)
	return target
}

func binaryBigEndian(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
