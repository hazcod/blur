package durable

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const activityDayLinksNamespace = "activity-day-links-v2"
const legacyActivityDayPages = 64

var errActivityCursor = &storage.StoreError{Code: storage.Invalid}

type activityDayLink struct {
	Before int `json:"before"`
	After  int `json:"after"`
}

func decodeActivityDayMeta(encoded []byte) (activityDayMeta, error) {
	var meta activityDayMeta
	if json.Unmarshal(encoded, &meta) != nil || meta.Pages < 1 || meta.Count < 1 || (meta.Count-1)/activityDayPageSize >= meta.Pages {
		return activityDayMeta{}, ErrCorrupt
	}
	switch meta.Version {
	case 0:
		if meta.Pages > legacyActivityDayPages {
			return activityDayMeta{}, ErrCorrupt
		}
	case 2:
		if meta.First < 0 || meta.First >= meta.Pages || meta.Last < 0 || meta.Last >= meta.Pages {
			return activityDayMeta{}, ErrCorrupt
		}
	default:
		return activityDayMeta{}, ErrCorrupt
	}
	return meta, nil
}

func parseActivityDayPageID(date, id string) (int, error) {
	prefix := date + ":p:"
	if !strings.HasPrefix(id, prefix) {
		return 0, ErrCorrupt
	}
	page, err := strconv.Atoi(strings.TrimPrefix(id, prefix))
	if err != nil || page < 0 || activityDayPageID(date, page) != id {
		return 0, ErrCorrupt
	}
	return page, nil
}

func (s *Store) readActivityDayLink(reader bolt.Reader, date string, page int, meta activityDayMeta) (activityDayLink, error) {
	encoded, found, err := s.readIndexValueReader(reader, activityDayLinksNamespace, activityDayPageID(date, page), 128)
	if err != nil {
		return activityDayLink{}, err
	}
	defer clear(encoded)
	var link activityDayLink
	if !found || json.Unmarshal(encoded, &link) != nil || link.Before < -1 || link.Before >= meta.Pages || link.After < -1 || link.After >= meta.Pages || link.Before == page || link.After == page {
		return activityDayLink{}, ErrCorrupt
	}
	return link, nil
}

// Legacy days contain at most 64 pages. Migrate only their directory links;
// authenticated interval payloads and caller-visible cursors stay in place.
func (tx *Tx) migrateActivityDay(date string, meta activityDayMeta) (activityDayMeta, error) {
	if meta.Version == 2 {
		return meta, nil
	}
	var pages []int
	count := 0
	for page := 0; page < meta.Pages; page++ {
		pageID := activityDayPageID(date, page)
		encoded, found, err := tx.readIndexValue(activityDayNamespace, pageID, 8192)
		if err != nil {
			return activityDayMeta{}, err
		}
		var ids []string
		valid := found && json.Unmarshal(encoded, &ids) == nil && validActivityDayIDs(ids)
		clear(encoded)
		if !valid {
			return activityDayMeta{}, ErrCorrupt
		}
		if len(ids) == 0 {
			if err := tx.deleteKnowledgeRecord(activityDayNamespace, pageID); err != nil {
				return activityDayMeta{}, err
			}
			continue
		}
		count += len(ids)
		pages = append(pages, page)
	}
	if count != meta.Count || len(pages) == 0 {
		return activityDayMeta{}, ErrCorrupt
	}
	for i, page := range pages {
		link := activityDayLink{Before: -1, After: -1}
		if i > 0 {
			link.Before = pages[i-1]
		}
		if i+1 < len(pages) {
			link.After = pages[i+1]
		}
		if err := tx.PutIndexRecord(activityDayLinksNamespace, activityDayPageID(date, page), mustJSON(link)); err != nil {
			return activityDayMeta{}, err
		}
	}
	meta.Version, meta.First, meta.Last = 2, pages[0], pages[len(pages)-1]
	return meta, nil
}

func (tx *Tx) activityDayLink(date string, page int, meta activityDayMeta) (activityDayLink, error) {
	return tx.store.readActivityDayLink(tx.writer, date, page, meta)
}

// activityDayIDs starts from the authenticated cursor's own page and walks
// only live pages. Its allocations and read work depend on this requested
// quantum, never the day's lifetime row count or highest allocated page.
func (s *Store) activityDayIDs(reader bolt.Reader, date, after string, meta activityDayMeta, limit int) ([]string, error) {
	page := meta.First
	if meta.Version == 0 {
		page = 0
	}
	foundCursor := after == ""
	if !foundCursor {
		ref, found, err := s.readIndexValueReader(reader, activityDayRefNamespace, after, 64)
		if err != nil {
			return nil, err
		}
		defer clear(ref)
		if !found {
			return nil, errActivityCursor
		}
		page, err = parseActivityDayPageID(date, string(ref))
		if err != nil || page >= meta.Pages {
			return nil, errActivityCursor
		}
	}
	selected := make([]string, 0, min(limit, meta.Count))
	seenPages := make(map[int]bool)
	seenIDs := make(map[string]bool)
	previous := -2
	for page >= 0 && page < meta.Pages && len(selected) < limit {
		if seenPages[page] {
			return nil, ErrCorrupt
		}
		seenPages[page] = true
		encoded, found, err := s.readIndexValueReader(reader, activityDayNamespace, activityDayPageID(date, page), 8192)
		if err != nil {
			return nil, err
		}
		var ids []string
		valid := found && json.Unmarshal(encoded, &ids) == nil && validActivityDayIDs(ids) && (meta.Version == 0 || len(ids) > 0)
		clear(encoded)
		if !valid {
			return nil, ErrCorrupt
		}
		for _, id := range ids {
			if seenIDs[id] {
				return nil, ErrCorrupt
			}
			seenIDs[id] = true
			if !foundCursor {
				if id == after {
					foundCursor = true
				}
				continue
			}
			selected = append(selected, id)
			if len(selected) == limit {
				break
			}
		}
		if !foundCursor {
			return nil, errActivityCursor
		}
		if meta.Version == 0 {
			page++
		} else {
			link, err := s.readActivityDayLink(reader, date, page, meta)
			if err != nil {
				return nil, err
			}
			if previous != -2 && link.Before != previous || after == "" && previous == -2 && link.Before != -1 {
				return nil, ErrCorrupt
			}
			previous, page = page, link.After
		}
	}
	if meta.Version == 2 && page == -1 && previous != meta.Last {
		return nil, ErrCorrupt
	}
	return selected, nil
}
