package durable

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"

	"github.com/nielsgroen/blur/internal/search"
	"github.com/nielsgroen/blur/internal/storage/physical/bolt"
)

const (
	searchIndexTreeNodeKeyBytes = 2 + sha256.Size + sha256.Size
	searchIndexTreeHeaderBytes  = 4
	searchIndexTreeLeaf         = 1
	searchIndexTreeBranch       = 2
	searchIndexTreeOrderBytes   = 1 + sha256.Size
	searchIndexTreeChildBytes   = searchIndexTreeOrderBytes + sha256.Size
	searchIndexTreeMaxChildren  = 1024
	searchIndexTreeMaxDepth     = 4
)

type searchIndexTreeChild struct {
	upper [searchIndexTreeOrderBytes]byte
	id    [sha256.Size]byte
}

type searchIndexTreeNode struct {
	leaf     bool
	postings []searchIndexPosting
	children []searchIndexTreeChild
}

func (s *Store) searchIndexTreeKey(kind byte, digest [sha256.Size]byte, nodeID [sha256.Size]byte) []byte {
	mac := hmac.New(sha256.New, s.state.idKey)
	_, _ = io.WriteString(mac, "blur.durable.search.tree-node.v3\x00")
	_, _ = mac.Write([]byte{kind})
	_, _ = mac.Write(digest[:])
	_, _ = mac.Write(nodeID[:])
	sum := mac.Sum(nil)
	key := make([]byte, searchIndexTreeNodeKeyBytes)
	key[0], key[1] = searchIndexGroup, kind
	copy(key[2:2+sha256.Size], digest[:])
	copy(key[2+sha256.Size:], sum)
	clear(sum)
	return key
}

func (s *Store) searchIndexTreeChildID(kind byte, digest [sha256.Size]byte, parent [sha256.Size]byte, pivot [searchIndexTreeOrderBytes]byte, nodeType, side byte) [sha256.Size]byte {
	mac := hmac.New(sha256.New, s.state.idKey)
	_, _ = io.WriteString(mac, "blur.durable.search.tree-child.v3\x00")
	_, _ = mac.Write([]byte{kind, nodeType, side})
	_, _ = mac.Write(digest[:])
	_, _ = mac.Write(parent[:])
	_, _ = mac.Write(pivot[:])
	sum := mac.Sum(nil)
	var id [sha256.Size]byte
	copy(id[:], sum)
	clear(sum)
	return id
}

func newSearchIndexTreeNodeID() ([sha256.Size]byte, error) {
	var id [sha256.Size]byte
	for id == ([sha256.Size]byte{}) {
		if _, err := io.ReadFull(rand.Reader, id[:]); err != nil {
			clear(id[:])
			return [sha256.Size]byte{}, err
		}
	}
	return id, nil
}

func searchIndexWeight(fields byte) int {
	if fields&0x02 != 0 {
		return 3
	}
	if fields&0x04 != 0 {
		return 2
	}
	if fields&0x01 != 0 {
		return 1
	}
	return 0
}

func searchIndexOrderKey(kind byte, fields byte, sourceID string) ([]byte, error) {
	if !validHash(sourceID) {
		return nil, ErrCorrupt
	}
	rank := byte(0)
	if kind == searchIndexTerm {
		weight := searchIndexWeight(fields)
		if weight == 0 {
			return nil, ErrCorrupt
		}
		rank = byte(4 - weight)
	}
	id, err := hex.DecodeString(sourceID)
	if err != nil || len(id) != sha256.Size {
		clear(id)
		return nil, ErrCorrupt
	}
	ordered := make([]byte, searchIndexTreeOrderBytes)
	ordered[0] = rank
	copy(ordered[1:], id)
	clear(id)
	return ordered, nil
}

func searchIndexPostingLess(a, b searchIndexPosting) bool {
	weightA, weightB := searchIndexWeight(a.fields), searchIndexWeight(b.fields)
	if weightA != weightB {
		return weightA > weightB
	}
	return a.sourceID < b.sourceID
}

func encodeSearchIndexTreeNode(node searchIndexTreeNode) ([]byte, error) {
	if node.leaf {
		if len(node.postings) == 0 || len(node.postings) > searchIndexPageRows || len(node.children) != 0 {
			return nil, ErrCorrupt
		}
		page, err := encodeSearchIndexPage(node.postings)
		if err != nil {
			return nil, err
		}
		encoded := make([]byte, searchIndexTreeHeaderBytes+len(page))
		encoded[0], encoded[1] = searchIndexFormatVersion, searchIndexTreeLeaf
		encoded[2] = byte(len(node.postings) >> 8)
		encoded[3] = byte(len(node.postings))
		copy(encoded[searchIndexTreeHeaderBytes:], page)
		clear(page)
		return encoded, nil
	}
	if len(node.postings) != 0 || len(node.children) == 0 || len(node.children) > searchIndexTreeMaxChildren {
		return nil, ErrCorrupt
	}
	encoded := make([]byte, searchIndexTreeHeaderBytes+len(node.children)*searchIndexTreeChildBytes)
	encoded[0], encoded[1] = searchIndexFormatVersion, searchIndexTreeBranch
	encoded[2] = byte(len(node.children) >> 8)
	encoded[3] = byte(len(node.children))
	position := searchIndexTreeHeaderBytes
	var previous [searchIndexTreeOrderBytes]byte
	seen := make(map[[sha256.Size]byte]struct{}, len(node.children))
	for i, child := range node.children {
		if child.id == ([sha256.Size]byte{}) || i > 0 && bytes.Compare(previous[:], child.upper[:]) >= 0 {
			clear(encoded)
			return nil, ErrCorrupt
		}
		if _, exists := seen[child.id]; exists {
			clear(encoded)
			return nil, ErrCorrupt
		}
		seen[child.id] = struct{}{}
		if child.upper == searchIndexTreeInfinity && i != len(node.children)-1 {
			clear(encoded)
			return nil, ErrCorrupt
		}
		copy(encoded[position:position+searchIndexTreeOrderBytes], child.upper[:])
		copy(encoded[position+searchIndexTreeOrderBytes:position+searchIndexTreeChildBytes], child.id[:])
		previous = child.upper
		position += searchIndexTreeChildBytes
	}
	return encoded, nil
}

var searchIndexTreeInfinity = func() [searchIndexTreeOrderBytes]byte {
	var key [searchIndexTreeOrderBytes]byte
	for i := range key {
		key[i] = 0xff
	}
	return key
}()

func decodeSearchIndexTreeNode(encoded []byte, kind byte) (searchIndexTreeNode, error) {
	if len(encoded) < searchIndexTreeHeaderBytes || encoded[0] != searchIndexFormatVersion || !validSearchIndexKind(kind) {
		return searchIndexTreeNode{}, ErrCorrupt
	}
	count := int(encoded[2])<<8 | int(encoded[3])
	node := searchIndexTreeNode{}
	switch encoded[1] {
	case searchIndexTreeLeaf:
		postings, err := decodeSearchIndexPage(encoded[searchIndexTreeHeaderBytes:])
		if err != nil || len(postings) == 0 || len(postings) != count {
			return searchIndexTreeNode{}, ErrCorrupt
		}
		for _, posting := range postings {
			ordered, orderErr := searchIndexOrderKey(kind, posting.fields, posting.sourceID)
			if orderErr != nil {
				clear(ordered)
				return searchIndexTreeNode{}, ErrCorrupt
			}
			clear(ordered)
		}
		return searchIndexTreeNode{leaf: true, postings: postings}, nil
	case searchIndexTreeBranch:
		if count == 0 || count > searchIndexTreeMaxChildren || len(encoded) != searchIndexTreeHeaderBytes+count*searchIndexTreeChildBytes {
			return searchIndexTreeNode{}, ErrCorrupt
		}
		node.children = make([]searchIndexTreeChild, count)
		position := searchIndexTreeHeaderBytes
		var previous [searchIndexTreeOrderBytes]byte
		seen := make(map[[sha256.Size]byte]struct{}, count)
		for i := range node.children {
			child := &node.children[i]
			copy(child.upper[:], encoded[position:position+searchIndexTreeOrderBytes])
			copy(child.id[:], encoded[position+searchIndexTreeOrderBytes:position+searchIndexTreeChildBytes])
			if child.id == ([sha256.Size]byte{}) || i > 0 && bytes.Compare(previous[:], child.upper[:]) >= 0 || child.upper == searchIndexTreeInfinity && i != count-1 {
				return searchIndexTreeNode{}, ErrCorrupt
			}
			if _, exists := seen[child.id]; exists {
				return searchIndexTreeNode{}, ErrCorrupt
			}
			seen[child.id] = struct{}{}
			previous = child.upper
			position += searchIndexTreeChildBytes
		}
		return node, nil
	default:
		return searchIndexTreeNode{}, ErrCorrupt
	}
}

func validateSearchIndexTreeNodeRange(node searchIndexTreeNode, upper *[searchIndexTreeOrderBytes]byte, kind byte) error {
	if node.leaf {
		for _, posting := range node.postings {
			ordered, err := searchIndexOrderKey(kind, posting.fields, posting.sourceID)
			if err != nil {
				clear(ordered)
				return ErrCorrupt
			}
			if upper != nil && bytes.Compare(ordered, upper[:]) > 0 {
				clear(ordered)
				return ErrCorrupt
			}
			clear(ordered)
		}
		return nil
	}
	if upper == nil && node.children[len(node.children)-1].upper != searchIndexTreeInfinity {
		return ErrCorrupt
	}
	if upper != nil && node.children[len(node.children)-1].upper != *upper {
		return ErrCorrupt
	}
	return nil
}

func (tx *Tx) readSearchIndexTreeNodeLocked(kind byte, digest [sha256.Size]byte, nodeID [sha256.Size]byte) (searchIndexTreeNode, []byte, bool, error) {
	key := tx.store.searchIndexTreeKey(kind, digest, nodeID)
	envelope, found, err := tx.writer.Get(key)
	if err != nil {
		clear(key)
		return searchIndexTreeNode{}, nil, false, mapStorePhysicalError(err)
	}
	if !found {
		clear(key)
		return searchIndexTreeNode{}, nil, false, nil
	}
	plain, err := tx.store.open(key, "search-index-tree-v3", envelope)
	clear(key)
	if err != nil {
		clear(envelope)
		return searchIndexTreeNode{}, nil, false, ErrCorrupt
	}
	node, err := decodeSearchIndexTreeNode(plain, kind)
	clear(plain)
	if err != nil {
		clear(envelope)
		return searchIndexTreeNode{}, nil, false, err
	}
	return node, envelope, true, nil
}

func (tx *Tx) putSearchIndexTreeNodeLocked(kind byte, digest [sha256.Size]byte, nodeID [sha256.Size]byte, node searchIndexTreeNode, oldEnvelope []byte) error {
	key := tx.store.searchIndexTreeKey(kind, digest, nodeID)
	plain, err := encodeSearchIndexTreeNode(node)
	if err != nil {
		clear(key)
		return err
	}
	envelope, err := tx.store.seal(key, "search-index-tree-v3", plain)
	clear(plain)
	if err != nil {
		clear(key)
		return err
	}
	if len(oldEnvelope) > 0 {
		if err := tx.manifest.removeKey(key, oldEnvelope, tx.store.state.manifestKey); err != nil {
			clear(key)
			clear(envelope)
			return err
		}
	}
	if err := tx.writer.Put(key, envelope); err != nil {
		clear(key)
		clear(envelope)
		return mapStorePhysicalError(err)
	}
	tx.manifest.addKey(key, envelope, tx.store.state.manifestKey)
	tx.dirty = true
	clear(key)
	clear(envelope)
	return nil
}

type searchIndexTreeSplit struct {
	pivot [searchIndexTreeOrderBytes]byte
	right [sha256.Size]byte
}

func (tx *Tx) insertSearchIndexTreeLocked(kind byte, digest [sha256.Size]byte, ordered []byte, posting searchIndexPosting) error {
	if len(ordered) != searchIndexTreeOrderBytes {
		return ErrCorrupt
	}
	var wanted [searchIndexTreeOrderBytes]byte
	copy(wanted[:], ordered)
	var root [sha256.Size]byte
	var insert func([sha256.Size]byte, int, *[searchIndexTreeOrderBytes]byte) (*searchIndexTreeSplit, error)
	insert = func(nodeID [sha256.Size]byte, depth int, upper *[searchIndexTreeOrderBytes]byte) (*searchIndexTreeSplit, error) {
		if depth >= searchIndexTreeMaxDepth {
			return nil, ErrCorrupt
		}
		node, oldEnvelope, found, err := tx.readSearchIndexTreeNodeLocked(kind, digest, nodeID)
		if err != nil {
			return nil, err
		}
		if !found {
			if depth != 0 {
				return nil, ErrCorrupt
			}
			return nil, tx.putSearchIndexTreeNodeLocked(kind, digest, nodeID, searchIndexTreeNode{leaf: true, postings: []searchIndexPosting{posting}}, nil)
		}
		defer clear(oldEnvelope)
		if err := validateSearchIndexTreeNodeRange(node, upper, kind); err != nil {
			return nil, err
		}
		if node.leaf {
			for _, existing := range node.postings {
				if existing.sourceID == posting.sourceID {
					return nil, ErrCorrupt
				}
			}
			postings := append(append([]searchIndexPosting(nil), node.postings...), posting)
			sortSearchIndexPostings(postings)
			if len(postings) <= searchIndexPageRows {
				return nil, tx.putSearchIndexTreeNodeLocked(kind, digest, nodeID, searchIndexTreeNode{leaf: true, postings: postings}, oldEnvelope)
			}
			middle := len(postings) / 2
			leftPostings := postings[:middle]
			rightPostings := postings[middle:]
			pivotBytes, err := searchIndexOrderKey(kind, leftPostings[len(leftPostings)-1].fields, leftPostings[len(leftPostings)-1].sourceID)
			if err != nil {
				return nil, err
			}
			var pivot [searchIndexTreeOrderBytes]byte
			copy(pivot[:], pivotBytes)
			clear(pivotBytes)
			if depth == 0 {
				leftID, err := newSearchIndexTreeNodeID()
				if err != nil {
					return nil, err
				}
				rightID, err := newSearchIndexTreeNodeID()
				if err != nil {
					return nil, err
				}
				if err := tx.putSearchIndexTreeNodeLocked(kind, digest, leftID, searchIndexTreeNode{leaf: true, postings: leftPostings}, nil); err != nil {
					return nil, err
				}
				if err := tx.putSearchIndexTreeNodeLocked(kind, digest, rightID, searchIndexTreeNode{leaf: true, postings: rightPostings}, nil); err != nil {
					return nil, err
				}
				branch := searchIndexTreeNode{children: []searchIndexTreeChild{{upper: pivot, id: leftID}, {upper: searchIndexTreeInfinity, id: rightID}}}
				return nil, tx.putSearchIndexTreeNodeLocked(kind, digest, root, branch, oldEnvelope)
			}
			rightID, err := newSearchIndexTreeNodeID()
			if err != nil {
				return nil, err
			}
			if err := tx.putSearchIndexTreeNodeLocked(kind, digest, nodeID, searchIndexTreeNode{leaf: true, postings: leftPostings}, oldEnvelope); err != nil {
				return nil, err
			}
			if err := tx.putSearchIndexTreeNodeLocked(kind, digest, rightID, searchIndexTreeNode{leaf: true, postings: rightPostings}, nil); err != nil {
				return nil, err
			}
			return &searchIndexTreeSplit{pivot: pivot, right: rightID}, nil
		}
		if depth == searchIndexTreeMaxDepth-1 {
			return nil, ErrCorrupt
		}
		position := treeChildPosition(node.children, wanted)
		child := node.children[position]
		childUpper := child.upper
		childSplit, err := insert(child.id, depth+1, &childUpper)
		if err != nil || childSplit == nil {
			return nil, err
		}
		children := append([]searchIndexTreeChild(nil), node.children...)
		children[position].upper = childSplit.pivot
		children = append(children, searchIndexTreeChild{})
		copy(children[position+2:], children[position+1:])
		children[position+1] = searchIndexTreeChild{upper: childUpper, id: childSplit.right}
		if len(children) <= searchIndexTreeMaxChildren {
			return nil, tx.putSearchIndexTreeNodeLocked(kind, digest, nodeID, searchIndexTreeNode{children: children}, oldEnvelope)
		}
		middle := len(children) / 2
		leftNode := searchIndexTreeNode{children: append([]searchIndexTreeChild(nil), children[:middle]...)}
		rightNode := searchIndexTreeNode{children: append([]searchIndexTreeChild(nil), children[middle:]...)}
		pivot := leftNode.children[len(leftNode.children)-1].upper
		if depth == 0 {
			leftID, err := newSearchIndexTreeNodeID()
			if err != nil {
				return nil, err
			}
			rightID, err := newSearchIndexTreeNodeID()
			if err != nil {
				return nil, err
			}
			if err := tx.putSearchIndexTreeNodeLocked(kind, digest, leftID, leftNode, nil); err != nil {
				return nil, err
			}
			if err := tx.putSearchIndexTreeNodeLocked(kind, digest, rightID, rightNode, nil); err != nil {
				return nil, err
			}
			branch := searchIndexTreeNode{children: []searchIndexTreeChild{{upper: pivot, id: leftID}, {upper: searchIndexTreeInfinity, id: rightID}}}
			return nil, tx.putSearchIndexTreeNodeLocked(kind, digest, root, branch, oldEnvelope)
		}
		rightID, err := newSearchIndexTreeNodeID()
		if err != nil {
			return nil, err
		}
		if err := tx.putSearchIndexTreeNodeLocked(kind, digest, nodeID, leftNode, oldEnvelope); err != nil {
			return nil, err
		}
		if err := tx.putSearchIndexTreeNodeLocked(kind, digest, rightID, rightNode, nil); err != nil {
			return nil, err
		}
		return &searchIndexTreeSplit{pivot: pivot, right: rightID}, nil
	}
	_, err := insert(root, 0, nil)
	return err
}

func sortSearchIndexPostings(postings []searchIndexPosting) {
	for i := 1; i < len(postings); i++ {
		for j := i; j > 0 && searchIndexPostingLess(postings[j], postings[j-1]); j-- {
			postings[j], postings[j-1] = postings[j-1], postings[j]
		}
	}
}

func treeChildPosition(children []searchIndexTreeChild, ordered [searchIndexTreeOrderBytes]byte) int {
	for i := range children {
		if bytes.Compare(ordered[:], children[i].upper[:]) <= 0 {
			return i
		}
	}
	return len(children) - 1
}

func (tx *Tx) removeSearchIndexTreePostingLocked(kind byte, digest [sha256.Size]byte, sourceID string, fields byte) error {
	ordered, err := searchIndexOrderKey(kind, fields, sourceID)
	if err != nil {
		return err
	}
	defer clear(ordered)
	var wanted [searchIndexTreeOrderBytes]byte
	copy(wanted[:], ordered)
	var root [sha256.Size]byte
	var remove func([sha256.Size]byte, int, *[searchIndexTreeOrderBytes]byte) (bool, error)
	remove = func(nodeID [sha256.Size]byte, depth int, upper *[searchIndexTreeOrderBytes]byte) (bool, error) {
		if depth >= searchIndexTreeMaxDepth {
			return false, ErrCorrupt
		}
		node, oldEnvelope, found, err := tx.readSearchIndexTreeNodeLocked(kind, digest, nodeID)
		if err != nil || !found {
			clear(oldEnvelope)
			if err != nil {
				return false, err
			}
			return false, ErrCorrupt
		}
		defer clear(oldEnvelope)
		if err := validateSearchIndexTreeNodeRange(node, upper, kind); err != nil {
			return false, err
		}
		if node.leaf {
			kept := make([]searchIndexPosting, 0, len(node.postings)-1)
			removed := false
			for _, posting := range node.postings {
				if posting.sourceID == sourceID {
					if removed || posting.fields != fields {
						return false, ErrCorrupt
					}
					removed = true
					continue
				}
				kept = append(kept, posting)
			}
			if !removed {
				return false, ErrCorrupt
			}
			if len(kept) == 0 {
				key := tx.store.searchIndexTreeKey(kind, digest, nodeID)
				err := tx.deleteKnownEntryLocked(key, oldEnvelope)
				clear(key)
				return true, err
			}
			return false, tx.putSearchIndexTreeNodeLocked(kind, digest, nodeID, searchIndexTreeNode{leaf: true, postings: kept}, oldEnvelope)
		}
		if depth == searchIndexTreeMaxDepth-1 {
			return false, ErrCorrupt
		}
		position := treeChildPosition(node.children, wanted)
		child := node.children[position]
		childUpper := child.upper
		childEmpty, err := remove(child.id, depth+1, &childUpper)
		if err != nil || !childEmpty {
			return false, err
		}
		children := append([]searchIndexTreeChild(nil), node.children...)
		children = append(children[:position], children[position+1:]...)
		if len(children) == 0 {
			key := tx.store.searchIndexTreeKey(kind, digest, nodeID)
			err := tx.deleteKnownEntryLocked(key, oldEnvelope)
			clear(key)
			return true, err
		}
		if position == len(children) {
			if upper == nil {
				children[len(children)-1].upper = searchIndexTreeInfinity
			} else {
				children[len(children)-1].upper = *upper
			}
		}
		return false, tx.putSearchIndexTreeNodeLocked(kind, digest, nodeID, searchIndexTreeNode{children: children}, oldEnvelope)
	}
	_, err = remove(root, 0, nil)
	return err
}

type searchIndexTreeIterator struct {
	ctx     context.Context
	reader  bolt.Reader
	store   *Store
	kind    byte
	digest  [sha256.Size]byte
	stack   []searchIndexTreeFrame
	started bool
	done    bool
	lastKey []byte
}

type searchIndexTreeFrame struct {
	id         [sha256.Size]byte
	node       searchIndexTreeNode
	upper      [searchIndexTreeOrderBytes]byte
	hasUpper   bool
	nextChild  int
	postingPos int
	depth      int
}

func newSearchIndexTreeIterator(ctx context.Context, reader bolt.Reader, store *Store, kind byte, digest [sha256.Size]byte) *searchIndexTreeIterator {
	return &searchIndexTreeIterator{ctx: ctx, reader: reader, store: store, kind: kind, digest: digest}
}

func (it *searchIndexTreeIterator) next(result *search.ExactPage, maxBytes int) (searchIndexPosting, bool, bool, error) {
	if it.done {
		return searchIndexPosting{}, false, false, nil
	}
	load := func(nodeID [sha256.Size]byte, depth int, upper *[searchIndexTreeOrderBytes]byte, root bool) (searchIndexTreeFrame, bool, bool, error) {
		if result.Nodes >= search.MaxIndexNodes {
			return searchIndexTreeFrame{}, false, true, nil
		}
		result.Nodes++
		key := it.store.searchIndexTreeKey(it.kind, it.digest, nodeID)
		envelope, found, used, blocked, err := readBoundedIndexValue(it.reader, key, maxBytes-result.Bytes)
		if err != nil || blocked || !found {
			clear(key)
			clear(envelope)
			result.Bytes += used
			if err != nil {
				return searchIndexTreeFrame{}, false, blocked, err
			}
			if blocked {
				return searchIndexTreeFrame{}, false, true, nil
			}
			if root {
				return searchIndexTreeFrame{}, false, false, nil
			}
			return searchIndexTreeFrame{}, false, false, ErrCorrupt
		}
		result.Bytes += used
		plain, err := it.store.open(key, "search-index-tree-v3", envelope)
		clear(key)
		clear(envelope)
		if err != nil {
			return searchIndexTreeFrame{}, false, false, ErrCorrupt
		}
		node, err := decodeSearchIndexTreeNode(plain, it.kind)
		clear(plain)
		if err != nil {
			return searchIndexTreeFrame{}, false, false, err
		}
		if err := validateSearchIndexTreeNodeRange(node, upper, it.kind); err != nil {
			return searchIndexTreeFrame{}, false, false, err
		}
		if !node.leaf && depth >= searchIndexTreeMaxDepth-1 {
			return searchIndexTreeFrame{}, false, false, ErrCorrupt
		}
		frame := searchIndexTreeFrame{id: nodeID, node: node, depth: depth}
		if upper != nil {
			frame.upper = *upper
			frame.hasUpper = true
		}
		return frame, true, false, nil
	}
	if !it.started {
		it.started = true
		var root [sha256.Size]byte
		frame, found, blocked, err := load(root, 0, nil, true)
		if err != nil || blocked {
			return searchIndexPosting{}, false, blocked, err
		}
		if !found {
			it.done = true
			return searchIndexPosting{}, false, false, nil
		}
		it.stack = append(it.stack, frame)
	}
	for len(it.stack) > 0 {
		if err := it.ctx.Err(); err != nil {
			return searchIndexPosting{}, false, false, err
		}
		last := len(it.stack) - 1
		frame := &it.stack[last]
		if frame.node.leaf {
			if frame.postingPos < len(frame.node.postings) {
				posting := frame.node.postings[frame.postingPos]
				ordered, err := searchIndexOrderKey(it.kind, posting.fields, posting.sourceID)
				if err != nil {
					return searchIndexPosting{}, false, false, err
				}
				if it.lastKey != nil && bytes.Compare(it.lastKey, ordered) >= 0 {
					clear(ordered)
					return searchIndexPosting{}, false, false, ErrCorrupt
				}
				clear(it.lastKey)
				it.lastKey = ordered
				frame.postingPos++
				return posting, true, false, nil
			}
			it.stack = it.stack[:last]
			continue
		}
		if frame.nextChild >= len(frame.node.children) {
			it.stack = it.stack[:last]
			continue
		}
		child := frame.node.children[frame.nextChild]
		frame.nextChild++
		childID := child.id
		upper := child.upper
		childFrame, found, blocked, err := load(childID, frame.depth+1, &upper, false)
		if err != nil || blocked {
			return searchIndexPosting{}, false, blocked, err
		}
		if !found {
			return searchIndexPosting{}, false, false, ErrCorrupt
		}
		it.stack = append(it.stack, childFrame)
	}
	clear(it.lastKey)
	it.lastKey = nil
	it.done = true
	return searchIndexPosting{}, false, false, nil
}
