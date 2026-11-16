package semantic

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/hazcod/blur/internal/semantic/hnsw"
)

const semanticNeighborPool = 1024

type indexedDocument struct {
	document Document
	keys     []uint64
}

type vectorReference struct {
	sourceID string
	chunk    int
}

// Index is a process-local accelerator over encrypted vector records. The
// graph is never exported to an unencrypted file. Scoped searches rank only
// eligible documents, so a rare app cannot disappear behind an unrelated ANN
// prefix. The default global lookup uses approximate HNSW neighbors.
type Index struct {
	mu                                     sync.RWMutex
	model                                  Model
	graph                                  *hnsw.Graph[uint64]
	documents                              map[string]indexedDocument
	vectors                                map[uint64]vectorReference
	nextKey                                uint64
	indexedCount, skippedCount, chunkCount int
}

func NewIndex(model Model) (*Index, error) {
	if model.Validate() != nil {
		return nil, ErrInvalid
	}
	graph := hnsw.NewGraph[uint64]()
	graph.EfSearch = 128
	return &Index{model: model, graph: graph, documents: make(map[string]indexedDocument), vectors: make(map[uint64]vectorReference)}, nil
}

func (i *Index) AddDocument(ctx context.Context, document Document) error {
	if i == nil || ctx == nil || document.Validate() != nil {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if document.Model.Key() != i.model.Key() {
		return ErrModelMismatch
	}
	i.removeLocked(document.SourceID)
	retained := document.Clone()
	keys := make([]uint64, 0, len(retained.Chunks))
	for index, chunk := range retained.Chunks {
		if err := ctx.Err(); err != nil {
			for _, key := range keys {
				i.graph.Delete(key)
				delete(i.vectors, key)
			}
			clearDocument(retained)
			return err
		}
		i.nextKey++
		key := i.nextKey
		i.graph.Add(hnsw.MakeNode(key, chunk.Vector))
		i.vectors[key] = vectorReference{sourceID: document.SourceID, chunk: index}
		keys = append(keys, key)
	}
	i.documents[document.SourceID] = indexedDocument{document: retained, keys: keys}
	if retained.SkipReason != "" {
		i.skippedCount++
	} else {
		i.indexedCount++
	}
	i.chunkCount += len(retained.Chunks)
	return nil
}

func (i *Index) Remove(sourceID string) {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.removeLocked(sourceID)
}

func (i *Index) HasDocument(sourceID string) bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	_, found := i.documents[sourceID]
	return found
}

func (i *Index) Counts() (indexed, skipped, chunks int) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.indexedCount, i.skippedCount, i.chunkCount
}

func (i *Index) removeLocked(sourceID string) {
	doc, found := i.documents[sourceID]
	if !found {
		return
	}
	if doc.document.SkipReason != "" {
		i.skippedCount--
	} else {
		i.indexedCount--
	}
	i.chunkCount -= len(doc.document.Chunks)
	for _, key := range doc.keys {
		i.graph.Delete(key)
		delete(i.vectors, key)
	}
	clearDocument(doc.document)
	delete(i.documents, sourceID)
}

func (i *Index) Purge() {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, item := range i.documents {
		clearDocument(item.document)
	}
	i.graph = hnsw.NewGraph[uint64]()
	i.graph.EfSearch = 128
	i.documents = make(map[string]indexedDocument)
	i.vectors = make(map[uint64]vectorReference)
	i.nextKey = 0
	i.indexedCount, i.skippedCount, i.chunkCount = 0, 0, 0
}

func (i *Index) Search(ctx context.Context, query Query) ([]Hit, error) {
	if i == nil || ctx == nil {
		return nil, ErrInvalid
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	if !validVector(query.Vector, i.model.Dimensions) || query.Limit < 1 || query.Limit > MaxResults || len(query.AppIDs)+len(query.Domains) > 8 || len(query.ScopeSourceIDs) > MaxScopeSources || query.Start.IsZero() != query.End.IsZero() || !query.Start.IsZero() && !query.Start.Before(query.End) {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	apps, domains := map[string]bool{}, map[string]bool{}
	for _, app := range query.AppIDs {
		if app == "" || !canonicalIdentifier(app, 128) {
			return nil, ErrInvalid
		}
		apps[app] = true
	}
	for _, domain := range query.Domains {
		if domain == "" || !canonicalIdentifier(domain, 253) {
			return nil, ErrInvalid
		}
		domains[domain] = true
	}
	scopeSources := make(map[string]bool, len(query.ScopeSourceIDs))
	for _, id := range query.ScopeSourceIDs {
		if !ValidID(id) {
			return nil, ErrInvalid
		}
		scopeSources[id] = true
	}
	best := make(map[string]Hit)
	consider := func(doc Document, chunk Chunk, capturedAt time.Time) {
		var score float64
		for index, value := range chunk.Vector {
			score += float64(value) * float64(query.Vector[index])
		}
		hit := Hit{SourceID: doc.SourceID, AppID: doc.AppID, Domain: doc.Domain, CapturedAt: capturedAt, SourceBytes: doc.TextBytes + len(doc.AppID) + len(doc.Domain), Score: float32(max(-1, min(1, score))), Start: chunk.Start, End: chunk.End}
		previous, found := best[doc.SourceID]
		if !found || hit.Score > previous.Score {
			best[doc.SourceID] = hit
		}
	}
	if len(apps)+len(domains) > 0 || !query.Start.IsZero() {
		for _, item := range i.documents {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			doc := item.document
			if len(apps) > 0 && !apps[doc.AppID] || len(domains) > 0 && !domains[doc.Domain] {
				continue
			}
			capturedAt := doc.CapturedAt
			if !query.Start.IsZero() {
				inWindow := !doc.CapturedAt.Before(query.Start) && doc.CapturedAt.Before(query.End)
				if query.resolveCapturedAt != nil {
					at, found, err := query.resolveCapturedAt(ctx, doc.SourceID, query.Start, query.End)
					if err != nil {
						return nil, err
					}
					inWindow = found
					if found {
						capturedAt = at
					}
				}
				if !scopeSources[doc.SourceID] && !inWindow {
					continue
				}
			}
			for _, chunk := range doc.Chunks {
				consider(doc, chunk, capturedAt)
			}
		}
	} else if i.graph.Len() > 0 {
		neighbors := i.graph.Search(query.Vector, min(semanticNeighborPool, i.graph.Len()))
		for _, node := range neighbors {
			ref, found := i.vectors[node.Key]
			if !found {
				continue
			}
			item, found := i.documents[ref.sourceID]
			if found && ref.chunk < len(item.document.Chunks) {
				consider(item.document, item.document.Chunks[ref.chunk], item.document.CapturedAt)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(best))
	for _, hit := range best {
		if !math.IsNaN(float64(hit.Score)) && !math.IsInf(float64(hit.Score), 0) {
			hits = append(hits, hit)
		}
	}
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].Score != hits[b].Score {
			return hits[a].Score > hits[b].Score
		}
		return hits[a].SourceID < hits[b].SourceID
	})
	if len(hits) > query.Limit {
		hits = hits[:query.Limit]
	}
	return hits, nil
}

func clearDocument(document Document) {
	for _, chunk := range document.Chunks {
		clear(chunk.Vector)
	}
}
