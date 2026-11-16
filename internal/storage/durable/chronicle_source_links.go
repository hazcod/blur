package durable

import (
	"encoding/json"
)

const chronicleLinksNS = "chronicle-links-v1"

type chronicleLinkRoot struct {
	Version    int
	Head, Tail string
	Count      int64
}
type chronicleSourceLink struct {
	Version    int
	Prev, Next string
}

func (tx *Tx) chronicleLinkRoot(source string) (chronicleLinkRoot, bool, error) {
	var root chronicleLinkRoot
	raw, found, err := tx.readIndexValue(chronicleLinksNS, source, 512)
	if err != nil || !found {
		return root, found, err
	}
	defer clear(raw)
	if json.Unmarshal(raw, &root) != nil || root.Version != 1 || root.Count < 1 || root.Count > int64(maxChronicleDays)*24 || !validChronicleHourID(root.Head) || !validChronicleHourID(root.Tail) || (root.Count == 1) != (root.Head == root.Tail) {
		return root, false, ErrCorrupt
	}
	return root, true, nil
}
func (tx *Tx) chronicleSourceLink(source, id string) (chronicleSourceLink, bool, error) {
	var link chronicleSourceLink
	raw, found, err := tx.readIndexValue(chronicleLinksNS, source+":"+id, 256)
	if err != nil || !found {
		return link, found, err
	}
	defer clear(raw)
	if json.Unmarshal(raw, &link) != nil || link.Version != 1 || link.Prev != "" && !validChronicleHourID(link.Prev) || link.Next != "" && !validChronicleHourID(link.Next) || link.Prev == id || link.Next == id {
		return link, false, ErrCorrupt
	}
	return link, true, nil
}

// Each write touches only a head, tail and immediate neighbors. A source may
// legitimately recur in many hours; its reverse links never become one array
// proportional to its lifetime. Legacy singleton arrays migrate atomically.
func (tx *Tx) migrateChronicleLinks(source string) (chronicleLinkRoot, bool, error) {
	root, found, err := tx.chronicleLinkRoot(source)
	if err != nil || found {
		return root, found, err
	}
	ids, legacy, err := tx.readChronicleSource(source)
	if err != nil || !legacy {
		return root, false, err
	}
	for _, id := range ids {
		if err := tx.appendChronicleLink(source, id, &root); err != nil {
			return root, false, err
		}
	}
	if err := tx.deleteKnowledgeRecord(chronicleSourceNS, source); err != nil {
		return root, false, err
	}
	return root, len(ids) > 0, nil
}
func (tx *Tx) appendChronicleLink(source, id string, root *chronicleLinkRoot) error {
	if !validHash(source) || !validChronicleHourID(id) {
		return ErrCorrupt
	}
	if _, exists, err := tx.chronicleSourceLink(source, id); err != nil {
		return err
	} else if exists {
		return ErrCorrupt
	}
	if root.Count >= int64(maxChronicleDays)*24 {
		return ErrCorrupt
	}
	link := chronicleSourceLink{Version: 1, Prev: root.Tail}
	if root.Count > 0 {
		tail, found, err := tx.chronicleSourceLink(source, root.Tail)
		if err != nil {
			return err
		}
		if !found || tail.Next != "" {
			return ErrCorrupt
		}
		tail.Next = id
		if err = tx.PutIndexRecord(chronicleLinksNS, source+":"+root.Tail, mustJSON(tail)); err != nil {
			return err
		}
	} else {
		root.Head = id
		root.Version = 1
	}
	root.Tail = id
	root.Count++
	if err := tx.PutIndexRecord(chronicleLinksNS, source+":"+id, mustJSON(link)); err != nil {
		return err
	}
	return tx.PutIndexRecord(chronicleLinksNS, source, mustJSON(root))
}
func (tx *Tx) unlinkChronicleLink(source, id string, root chronicleLinkRoot) error {
	link, found, err := tx.chronicleSourceLink(source, id)
	if err != nil {
		return err
	}
	if !found {
		return ErrCorrupt
	}
	if (link.Prev == "") != (root.Head == id) || (link.Next == "") != (root.Tail == id) {
		return ErrCorrupt
	}
	if link.Prev != "" {
		prev, found, err := tx.chronicleSourceLink(source, link.Prev)
		if err != nil {
			return err
		}
		if !found || prev.Next != id {
			return ErrCorrupt
		}
		prev.Next = link.Next
		if err = tx.PutIndexRecord(chronicleLinksNS, source+":"+link.Prev, mustJSON(prev)); err != nil {
			return err
		}
	} else {
		root.Head = link.Next
	}
	if link.Next != "" {
		next, found, err := tx.chronicleSourceLink(source, link.Next)
		if err != nil {
			return err
		}
		if !found || next.Prev != id {
			return ErrCorrupt
		}
		next.Prev = link.Prev
		if err = tx.PutIndexRecord(chronicleLinksNS, source+":"+link.Next, mustJSON(next)); err != nil {
			return err
		}
	} else {
		root.Tail = link.Prev
	}
	if err = tx.deleteKnowledgeRecord(chronicleLinksNS, source+":"+id); err != nil {
		return err
	}
	root.Count--
	if root.Count == 0 {
		if root.Head != "" || root.Tail != "" {
			return ErrCorrupt
		}
		return tx.deleteKnowledgeRecord(chronicleLinksNS, source)
	}
	if root.Head == "" || root.Tail == "" {
		return ErrCorrupt
	}
	return tx.PutIndexRecord(chronicleLinksNS, source, mustJSON(root))
}

// One summary per erasure transaction bounds derivative fanout even when a
// document has been revisited for years. Journal phase five resumes this head.
func (tx *Tx) removeChroniclesForSourceBatch(source string) (bool, error) {
	root, found, err := tx.migrateChronicleLinks(source)
	if err != nil || !found {
		return !found, err
	}
	if err = tx.deleteChronicle(root.Head); err != nil {
		return false, err
	}
	_, found, err = tx.chronicleLinkRoot(source)
	return !found, err
}
