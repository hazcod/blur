package durable

import (
	"crypto/sha256"
	"github.com/hazcod/blur/internal/capture"
)

const capturePartSearchGroup byte = 0x41

// capturePartIndex borrows the open store's lifetime. It must not be closed or
// used for observation bootstrap. It owns no database, key bytes, mutex or gate.
func (s *Store) capturePartIndex() *Store {
	if s == nil {
		return nil
	}
	return &Store{state: s.state, captureSearch: true}
}
func (s *Store) exactGroup() byte {
	if s != nil && s.captureSearch {
		return capturePartSearchGroup
	}
	return searchIndexGroup
}
func (s *Store) exactNamespace(namespace string) string {
	if s != nil && s.captureSearch {
		return "capture-part." + namespace
	}
	return namespace
}
func (s *Store) exactStateKey() []byte      { return []byte{s.exactGroup(), searchIndexState} }
func (s *Store) exactPrefix() []byte        { return []byte{s.exactGroup()} }
func (s *Store) exactPendingPrefix() []byte { return []byte{s.exactGroup(), searchIndexPending} }

// searchIndexTx changes only index addressing. The embedded pointer is the
// original live transaction: its writer, mutex, manifest, dirty bit and failure
// state are never copied. Callers hold that transaction's mutex throughout.
type searchIndexTx struct {
	*Tx
	store *Store
}

func (tx *Tx) observationSearch() *searchIndexTx {
	return &searchIndexTx{Tx: tx, store: tx.store}
}
func (tx *Tx) capturePartSearch() *searchIndexTx {
	return &searchIndexTx{Tx: tx, store: tx.store.capturePartIndex()}
}

// Preserve the original observation transaction interfaces. Scoped views bind
// the same helpers to their own immutable addressing without changing Tx.store.
func (tx *Tx) searchIndexPlainLocked(key []byte, namespace string) ([]byte, bool, error) {
	return tx.observationSearch().searchIndexPlainLocked(key, namespace)
}

func (tx *Tx) putSearchIndexValueLocked(key []byte, namespace string, plaintext []byte, replace bool) error {
	return tx.observationSearch().putSearchIndexValueLocked(key, namespace, plaintext, replace)
}

func (tx *Tx) addSearchIndexPostingLocked(kind byte, value, sourceID string, fields byte) (uint32, [sha256.Size]byte, error) {
	return tx.observationSearch().addSearchIndexPostingLocked(kind, value, sourceID, fields)
}

func (tx *Tx) indexObservationLocked(observation capture.SanitizedObservation) error {
	return tx.observationSearch().indexObservationLocked(observation)
}

func (tx *Tx) removeSearchIndexSourceLocked(sourceID string) error {
	return tx.observationSearch().removeSearchIndexSourceLocked(sourceID)
}

func (tx *Tx) writeSearchIndexStateLocked() error {
	return tx.observationSearch().writeSearchIndexStateLocked()
}

func (tx *Tx) readSearchIndexTreeNodeLocked(kind byte, digest [sha256.Size]byte, nodeID [sha256.Size]byte) (searchIndexTreeNode, []byte, bool, error) {
	return tx.observationSearch().readSearchIndexTreeNodeLocked(kind, digest, nodeID)
}

func (tx *Tx) putSearchIndexTreeNodeLocked(kind byte, digest [sha256.Size]byte, nodeID [sha256.Size]byte, node searchIndexTreeNode, oldEnvelope []byte) error {
	return tx.observationSearch().putSearchIndexTreeNodeLocked(kind, digest, nodeID, node, oldEnvelope)
}

func (tx *Tx) insertSearchIndexTreeLocked(kind byte, digest [sha256.Size]byte, ordered []byte, posting searchIndexPosting) error {
	return tx.observationSearch().insertSearchIndexTreeLocked(kind, digest, ordered, posting)
}

func (tx *Tx) removeSearchIndexTreePostingLocked(kind byte, digest [sha256.Size]byte, sourceID string, fields byte) error {
	return tx.observationSearch().removeSearchIndexTreePostingLocked(kind, digest, sourceID, fields)
}

func (tx *Tx) queueObservationExactLocked(observation capture.SanitizedObservation) error {
	return tx.observationSearch().queueObservationExactLocked(observation)
}

func (tx *Tx) removePagedSearchBatchLocked(sourceID string, drain bool) (bool, error) {
	return tx.observationSearch().removePagedSearchBatchLocked(sourceID, drain)
}
