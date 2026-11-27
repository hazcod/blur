package durable

import (
	"encoding/json"
	"fmt"
	"sort"
)

const activitySourceNodesNamespace = "activity-src-nodes-v2"

// Legacy remains bounded by the old record format. New references use
// independently encrypted linked nodes, so a source has no lifetime link cap.
type activitySourceList struct {
	Version int      `json:"version"`
	Legacy  []string `json:"legacy,omitempty"`
	First   string   `json:"first,omitempty"`
	Last    string   `json:"last,omitempty"`
	Count   uint64   `json:"count"`
}

type activitySourceNode struct {
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

func activitySourceNodeID(sourceID, segmentID string) string {
	return fmt.Sprintf("%d:%s:%s", len(sourceID), sourceID, segmentID)
}

func (tx *Tx) activitySourceList(namespace, sourceID string) (activitySourceList, error) {
	encoded, found, err := tx.readIndexValue(namespace, sourceID, 16<<10)
	if err != nil || !found {
		return activitySourceList{Version: 2}, err
	}
	defer clear(encoded)
	var list activitySourceList
	if len(encoded) > 0 && encoded[0] == '[' {
		if json.Unmarshal(encoded, &list.Legacy) != nil || len(list.Legacy) > legacyActivitySourceIDs {
			return activitySourceList{}, ErrCorrupt
		}
		list.Version, list.Count = 2, uint64(len(list.Legacy))
	} else if json.Unmarshal(encoded, &list) != nil || list.Version != 2 {
		return activitySourceList{}, ErrCorrupt
	}
	if list.Count < uint64(len(list.Legacy)) || len(list.Legacy) > legacyActivitySourceIDs || (list.First == "") != (list.Last == "") || (list.First == "") != (list.Count == uint64(len(list.Legacy))) || list.First != "" && !validOpaqueID(list.First) || list.Last != "" && !validOpaqueID(list.Last) {
		return activitySourceList{}, ErrCorrupt
	}
	for i, id := range list.Legacy {
		if !validOpaqueID(id) || i > 0 && list.Legacy[i-1] >= id {
			return activitySourceList{}, ErrCorrupt
		}
	}
	return list, nil
}

func (tx *Tx) activitySourceNode(namespace, sourceID, segmentID string) (activitySourceNode, bool, error) {
	encoded, found, err := tx.readIndexValue(namespace, activitySourceNodeID(sourceID, segmentID), 1024)
	if err != nil || !found {
		return activitySourceNode{}, found, err
	}
	defer clear(encoded)
	var node activitySourceNode
	if json.Unmarshal(encoded, &node) != nil || node.Before != "" && !validOpaqueID(node.Before) || node.After != "" && !validOpaqueID(node.After) || node.Before == segmentID || node.After == segmentID {
		return activitySourceNode{}, false, ErrCorrupt
	}
	return node, true, nil
}

func (tx *Tx) trackActivityLink(namespace, nodes, sourceID, segmentID string) error {
	list, err := tx.activitySourceList(namespace, sourceID)
	if err != nil {
		return err
	}
	if _, found, err := tx.activitySourceNode(nodes, sourceID, segmentID); err != nil || found {
		if err != nil {
			return err
		}
		return ErrCorrupt
	}
	i := sort.SearchStrings(list.Legacy, segmentID)
	if i < len(list.Legacy) && list.Legacy[i] == segmentID || list.Count == ^uint64(0) {
		return ErrCorrupt
	}
	if list.Last != "" {
		tail, found, err := tx.activitySourceNode(nodes, sourceID, list.Last)
		if err != nil || !found || tail.After != "" {
			if err != nil {
				return err
			}
			return ErrCorrupt
		}
		tail.After = segmentID
		if err := tx.PutIndexRecord(nodes, activitySourceNodeID(sourceID, list.Last), mustJSON(tail)); err != nil {
			return err
		}
	} else {
		list.First = segmentID
	}
	if err := tx.PutIndexRecord(nodes, activitySourceNodeID(sourceID, segmentID), mustJSON(activitySourceNode{Before: list.Last})); err != nil {
		return err
	}
	list.Last, list.Count = segmentID, list.Count+1
	return tx.PutIndexRecord(namespace, sourceID, mustJSON(list))
}

func (tx *Tx) untrackActivityLink(namespace, nodes, sourceID, segmentID string) error {
	list, err := tx.activitySourceList(namespace, sourceID)
	if err != nil {
		return err
	}
	i := sort.SearchStrings(list.Legacy, segmentID)
	if i < len(list.Legacy) && list.Legacy[i] == segmentID {
		list.Legacy = append(list.Legacy[:i], list.Legacy[i+1:]...)
	} else {
		node, found, err := tx.activitySourceNode(nodes, sourceID, segmentID)
		if err != nil || !found || list.Count == 0 {
			if err != nil {
				return err
			}
			return ErrCorrupt
		}
		if node.Before == "" {
			if list.First != segmentID {
				return ErrCorrupt
			}
			list.First = node.After
		} else {
			prior, found, err := tx.activitySourceNode(nodes, sourceID, node.Before)
			if err != nil || !found || prior.After != segmentID {
				if err != nil {
					return err
				}
				return ErrCorrupt
			}
			prior.After = node.After
			if err := tx.PutIndexRecord(nodes, activitySourceNodeID(sourceID, node.Before), mustJSON(prior)); err != nil {
				return err
			}
		}
		if node.After == "" {
			if list.Last != segmentID {
				return ErrCorrupt
			}
			list.Last = node.Before
		} else {
			next, found, err := tx.activitySourceNode(nodes, sourceID, node.After)
			if err != nil || !found || next.Before != segmentID {
				if err != nil {
					return err
				}
				return ErrCorrupt
			}
			next.Before = node.Before
			if err := tx.PutIndexRecord(nodes, activitySourceNodeID(sourceID, node.After), mustJSON(next)); err != nil {
				return err
			}
		}
		if err := tx.deleteKnowledgeRecord(nodes, activitySourceNodeID(sourceID, segmentID)); err != nil {
			return err
		}
	}
	list.Count--
	if list.Count == 0 {
		return tx.deleteKnowledgeRecord(namespace, sourceID)
	}
	return tx.PutIndexRecord(namespace, sourceID, mustJSON(list))
}
