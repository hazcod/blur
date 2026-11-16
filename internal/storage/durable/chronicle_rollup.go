package durable

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// Day and week rollups are additive index records: opening a store written
// before they existed needs no migration, and the schema version is unchanged.
// Record IDs and namespaces are HMAC-keyed like every other index record.
const (
	chronicleRollupNS       = "chronicle-rollup"
	chronicleRollupIndexNS  = "chronicle-rollup-ix"  // per-month day shards and per-year week shards
	chronicleRollupSourceNS = "chronicle-rollup-src" // source ID -> rollups that cite it
	chronicleRollupShards   = "shards"
	maxRollupShards         = 4096
	maxRollupRange          = 400 * 24 * time.Hour
	maxRollupsPerSource     = 512
	maxRollupBytes          = 48 << 10
	rollupSourceDropBatch   = 8
)

// SetChronicleZone supplies the IANA zone used to date hour recaps when marking
// their day rollup dirty. Without it the process zone (TZ or UTC) is used.
func (s *Store) SetChronicleZone(zone func() string) {
	if s == nil || s.state == nil {
		return
	}
	if zone == nil {
		s.state.rollupZone.Store(nil)
		return
	}
	s.state.rollupZone.Store(&zone)
}

func (s *Store) rollupLocation() (*time.Location, string) {
	candidates := []string{}
	if fn := s.state.rollupZone.Load(); fn != nil {
		candidates = append(candidates, (*fn)())
	}
	candidates = append(candidates, strings.TrimPrefix(os.Getenv("TZ"), ":"), time.Local.String())
	for _, name := range candidates {
		if name == "" || name == "Local" {
			continue
		}
		if loc, err := time.LoadLocation(name); err == nil {
			return loc, name
		}
	}
	return time.UTC, "UTC"
}

func validRollupID(id string) bool {
	switch {
	case strings.HasPrefix(id, "day:"):
		return chronicle.ValidDayKey(id[len("day:"):])
	case strings.HasPrefix(id, "week:"):
		return chronicle.ValidWeekKey(id[len("week:"):])
	}
	return false
}

func rollupShard(id string) string {
	if strings.HasPrefix(id, "day:") {
		return id[:len("day:")+7] // day:YYYY-MM
	}
	week := id[len("week:"):]
	return "week:" + week[:4]
}

func (s *Store) PutChronicleRollup(ctx context.Context, rollup chronicle.Rollup) error {
	if ctx == nil || chronicle.ValidateRollup(rollup) != nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	encoded, err := json.Marshal(rollup)
	if err != nil || len(encoded) > maxRollupBytes {
		clear(encoded)
		return &storage.StoreError{Code: storage.Invalid}
	}
	defer clear(encoded)
	return s.Update(ctx, func(tx *Tx) error {
		// A rollup may only cite sources that still exist at write time, so a
		// source erased while it was being generated is never re-linked.
		admission := &chronicleAdmissionReader{Reader: tx.writer}
		defer admission.close()
		for _, sourceID := range rollup.SourceIDs {
			_, _, found, err := s.sourceEvidenceReader(admission, sourceID)
			if err != nil {
				return err
			}
			if !found {
				return &storage.StoreError{Code: storage.Invalid}
			}
		}
		admission.close()
		old, found, err := tx.readRollup(rollup.ID)
		if err != nil {
			return err
		}
		if !found {
			return tx.writeRollup(rollup, nil)
		}
		return tx.writeRollup(rollup, &old)
	})
}

func (s *Store) GetChronicleRollup(ctx context.Context, id string) (chronicle.Rollup, bool, error) {
	if ctx == nil || !validRollupID(id) {
		return chronicle.Rollup{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	var rollup chronicle.Rollup
	var found bool
	err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		rollup, found, err = s.readRollupReader(reader, id)
		return err
	})
	if err != nil || !found {
		return chronicle.Rollup{}, false, err
	}
	return rollup, true, nil
}

// ListChronicleRollups returns rollups of one level whose period starts in the
// half-open range, in ascending ID order. after is the last ID of the previous
// page; the returned cursor is empty on the last page.
func (s *Store) ListChronicleRollups(ctx context.Context, level string, start, end time.Time, after string, limit int) ([]chronicle.Rollup, string, error) {
	if ctx == nil || level != chronicle.RollupLevelDay && level != chronicle.RollupLevelWeek || start.IsZero() || end.IsZero() || !start.Before(end) || end.Sub(start) > maxRollupRange || limit < 1 || limit > 64 || after != "" && (!validRollupID(after) || !strings.HasPrefix(after, level+":")) {
		return nil, "", &storage.StoreError{Code: storage.Invalid}
	}
	start, end = start.UTC(), end.UTC()
	var rows []chronicle.Rollup
	var next string
	err := s.view(ctx, func(reader bolt.Reader) error {
		shards, err := s.readRollupShardsReader(reader)
		if err != nil {
			return err
		}
		// Local dates differ from UTC dates by under a day, so widen the shard
		// window by two days; each record is still checked against the range.
		first, last := start.AddDate(0, 0, -2), end.AddDate(0, 0, 2)
		for _, shard := range shards {
			if !strings.HasPrefix(shard, level+":") || !rollupShardOverlaps(shard, first, last) {
				continue
			}
			ids, err := s.readRollupShardReader(reader, shard)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if after != "" && id <= after {
					continue
				}
				rollup, found, err := s.readRollupReader(reader, id)
				if err != nil {
					return err
				}
				if !found {
					return ErrCorrupt
				}
				if rollup.Start.Before(start) || !rollup.Start.Before(end) {
					continue
				}
				rows = append(rows, rollup)
			}
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	if len(rows) > limit {
		next = rows[limit-1].ID
		rows = rows[:limit]
	}
	return rows, next, nil
}

func rollupShardOverlaps(shard string, first, last time.Time) bool {
	var from, to time.Time
	switch {
	case strings.HasPrefix(shard, "day:"):
		month, err := time.Parse("2006-01", shard[len("day:"):])
		if err != nil {
			return false
		}
		from, to = month, month.AddDate(0, 1, 0)
	case strings.HasPrefix(shard, "week:"):
		year, err := time.Parse("2006", shard[len("week:"):])
		if err != nil {
			return false
		}
		// An ISO week can start in the previous calendar year.
		from, to = year.AddDate(0, 0, -7), year.AddDate(1, 0, 7)
	default:
		return false
	}
	return from.Before(last) && to.After(first)
}

func (s *Store) readRollupReader(reader bolt.Reader, id string) (chronicle.Rollup, bool, error) {
	b, found, err := s.readIndexValueReader(reader, chronicleRollupNS, id, maxRollupBytes)
	if err != nil || !found {
		return chronicle.Rollup{}, false, err
	}
	defer clear(b)
	var rollup chronicle.Rollup
	if json.Unmarshal(b, &rollup) != nil || chronicle.ValidateRollup(rollup) != nil || rollup.ID != id {
		return chronicle.Rollup{}, false, ErrCorrupt
	}
	return rollup, true, nil
}

func (tx *Tx) readRollup(id string) (chronicle.Rollup, bool, error) {
	b, found, err := tx.readIndexValue(chronicleRollupNS, id, maxRollupBytes)
	if err != nil || !found {
		return chronicle.Rollup{}, false, err
	}
	defer clear(b)
	var rollup chronicle.Rollup
	if json.Unmarshal(b, &rollup) != nil || chronicle.ValidateRollup(rollup) != nil || rollup.ID != id {
		return chronicle.Rollup{}, false, ErrCorrupt
	}
	return rollup, true, nil
}

// writeRollup persists a rollup together with its shard and source links.
func (tx *Tx) writeRollup(rollup chronicle.Rollup, old *chronicle.Rollup) error {
	if chronicle.ValidateRollup(rollup) != nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	encoded, err := json.Marshal(rollup)
	if err != nil || len(encoded) > maxRollupBytes {
		clear(encoded)
		return &storage.StoreError{Code: storage.Invalid}
	}
	defer clear(encoded)
	var prior []string
	if old != nil {
		prior = old.SourceIDs
	}
	if err := tx.syncRollupSources(rollup.ID, prior, rollup.SourceIDs); err != nil {
		return err
	}
	if old == nil {
		if err := tx.addRollupShard(rollup.ID); err != nil {
			return err
		}
	}
	return tx.PutIndexRecord(chronicleRollupNS, rollup.ID, encoded)
}

// markChronicleDayDirty records that the local day containing an hour recap
// changed, creating a dirty placeholder when the day has no rollup yet.
func (tx *Tx) markChronicleDayDirty(hourStart time.Time) error {
	loc, zone := tx.store.rollupLocation()
	id := chronicle.DayRollupID(chronicle.DayKeyAt(hourStart, loc))
	rollup, found, err := tx.readRollup(id)
	if err != nil {
		return err
	}
	if !found {
		placeholder, err := chronicle.NewRollupPlaceholder(id, zone)
		if err != nil {
			return &storage.StoreError{Code: storage.Invalid}
		}
		return tx.writeRollup(placeholder, nil)
	}
	if rollup.Dirty {
		return nil
	}
	old := rollup
	rollup.Dirty = true
	return tx.writeRollup(rollup, &old)
}

func (tx *Tx) readRollupShards() ([]string, error) {
	b, found, err := tx.readIndexValue(chronicleRollupIndexNS, chronicleRollupShards, 256<<10)
	if err != nil || !found {
		return nil, err
	}
	defer clear(b)
	var shards []string
	if json.Unmarshal(b, &shards) != nil || len(shards) > maxRollupShards || !sort.StringsAreSorted(shards) {
		return nil, ErrCorrupt
	}
	return shards, nil
}

func (s *Store) readRollupShardsReader(reader bolt.Reader) ([]string, error) {
	b, found, err := s.readIndexValueReader(reader, chronicleRollupIndexNS, chronicleRollupShards, 256<<10)
	if err != nil || !found {
		return nil, err
	}
	defer clear(b)
	var shards []string
	if json.Unmarshal(b, &shards) != nil || len(shards) > maxRollupShards || !sort.StringsAreSorted(shards) {
		return nil, ErrCorrupt
	}
	return shards, nil
}

func (s *Store) readRollupShardReader(reader bolt.Reader, shard string) ([]string, error) {
	b, found, err := s.readIndexValueReader(reader, chronicleRollupIndexNS, shard, 8192)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrCorrupt
	}
	defer clear(b)
	var ids []string
	if json.Unmarshal(b, &ids) != nil || len(ids) > 64 || !sort.StringsAreSorted(ids) {
		return nil, ErrCorrupt
	}
	for _, id := range ids {
		if !validRollupID(id) || rollupShard(id) != shard {
			return nil, ErrCorrupt
		}
	}
	return ids, nil
}

func (tx *Tx) readRollupShard(shard string) ([]string, bool, error) {
	b, found, err := tx.readIndexValue(chronicleRollupIndexNS, shard, 8192)
	if err != nil || !found {
		return nil, found, err
	}
	defer clear(b)
	var ids []string
	if json.Unmarshal(b, &ids) != nil || len(ids) > 64 || !sort.StringsAreSorted(ids) {
		return nil, false, ErrCorrupt
	}
	return ids, true, nil
}

func (tx *Tx) addRollupShard(id string) error {
	shard := rollupShard(id)
	ids, found, err := tx.readRollupShard(shard)
	if err != nil {
		return err
	}
	if !found {
		shards, err := tx.readRollupShards()
		if err != nil {
			return err
		}
		if len(shards) >= maxRollupShards {
			return &storage.StoreError{Code: storage.Invalid}
		}
		i := sort.SearchStrings(shards, shard)
		shards = slices.Insert(shards, i, shard)
		if err := tx.PutIndexRecord(chronicleRollupIndexNS, chronicleRollupShards, mustJSON(shards)); err != nil {
			return err
		}
	}
	i := sort.SearchStrings(ids, id)
	if i < len(ids) && ids[i] == id {
		return nil
	}
	ids = slices.Insert(ids, i, id)
	return tx.PutIndexRecord(chronicleRollupIndexNS, shard, mustJSON(ids))
}

func (tx *Tx) removeRollupShardEntry(id string) error {
	shard := rollupShard(id)
	ids, found, err := tx.readRollupShard(shard)
	if err != nil || !found {
		return err
	}
	ids = removeString(ids, id)
	if len(ids) > 0 {
		return tx.PutIndexRecord(chronicleRollupIndexNS, shard, mustJSON(ids))
	}
	if err := tx.deleteKnowledgeRecord(chronicleRollupIndexNS, shard); err != nil {
		return err
	}
	shards, err := tx.readRollupShards()
	if err != nil {
		return err
	}
	shards = removeString(shards, shard)
	if len(shards) == 0 {
		return tx.deleteKnowledgeRecord(chronicleRollupIndexNS, chronicleRollupShards)
	}
	return tx.PutIndexRecord(chronicleRollupIndexNS, chronicleRollupShards, mustJSON(shards))
}

func (tx *Tx) readRollupSources(source string) ([]string, bool, error) {
	b, found, err := tx.readIndexValue(chronicleRollupSourceNS, source, 16<<10)
	if err != nil || !found {
		return nil, found, err
	}
	defer clear(b)
	var ids []string
	if json.Unmarshal(b, &ids) != nil || len(ids) > maxRollupsPerSource || !sort.StringsAreSorted(ids) {
		return nil, false, ErrCorrupt
	}
	for _, id := range ids {
		if !validRollupID(id) {
			return nil, false, ErrCorrupt
		}
	}
	return ids, true, nil
}

// syncRollupSources moves the reverse source index from the sources a rollup
// used to cite to the ones it cites now.
func (tx *Tx) syncRollupSources(id string, prior, current []string) error {
	for _, source := range prior {
		if slices.Contains(current, source) {
			continue
		}
		ids, found, err := tx.readRollupSources(source)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		ids = removeString(ids, id)
		if len(ids) == 0 {
			err = tx.deleteKnowledgeRecord(chronicleRollupSourceNS, source)
		} else {
			err = tx.PutIndexRecord(chronicleRollupSourceNS, source, mustJSON(ids))
		}
		if err != nil {
			return err
		}
	}
	for _, source := range current {
		if slices.Contains(prior, source) {
			continue
		}
		ids, _, err := tx.readRollupSources(source)
		if err != nil {
			return err
		}
		i := sort.SearchStrings(ids, id)
		if i < len(ids) && ids[i] == id {
			continue
		}
		if len(ids) >= maxRollupsPerSource {
			return &storage.StoreError{Code: storage.Invalid}
		}
		ids = slices.Insert(ids, i, id)
		if err := tx.PutIndexRecord(chronicleRollupSourceNS, source, mustJSON(ids)); err != nil {
			return err
		}
	}
	return nil
}

// removeRollupsForSourceBatch is the deletion fence. A rollup that cites an
// erased source drops that ID, discards its generated text (which may restate
// the erased capture) and becomes dirty so it is regenerated from the hour
// recaps that remain. A bounded number of rollups is handled per call.
func (tx *Tx) removeRollupsForSourceBatch(source string) (bool, error) {
	for range rollupSourceDropBatch {
		ids, found, err := tx.readRollupSources(source)
		if err != nil {
			return false, err
		}
		if !found || len(ids) == 0 {
			return true, nil
		}
		id := ids[0]
		rollup, exists, err := tx.readRollup(id)
		if err != nil {
			return false, err
		}
		if !exists || !slices.Contains(rollup.SourceIDs, source) {
			return false, ErrCorrupt
		}
		old := rollup
		rollup.SourceIDs = removeString(slices.Clone(rollup.SourceIDs), source)
		rollup.Text, rollup.InputHash, rollup.Model, rollup.GeneratedAt = "", "", "", time.Time{}
		rollup.Dirty = true
		if err := tx.writeRollup(rollup, &old); err != nil {
			return false, err
		}
	}
	_, found, err := tx.readRollupSources(source)
	return !found, err
}

func (tx *Tx) deleteRollup(id string) error {
	rollup, found, err := tx.readRollup(id)
	if err != nil || !found {
		return err
	}
	if err := tx.syncRollupSources(id, rollup.SourceIDs, nil); err != nil {
		return err
	}
	if err := tx.removeRollupShardEntry(id); err != nil {
		return err
	}
	return tx.deleteKnowledgeRecord(chronicleRollupNS, id)
}

// DeleteAllChronicleRollups removes every day and week rollup. It is part of
// deleting all recaps, since rollups are derived from them.
func (s *Store) DeleteAllChronicleRollups(ctx context.Context) error {
	if ctx == nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var ids []string
		err := s.view(ctx, func(reader bolt.Reader) error {
			shards, err := s.readRollupShardsReader(reader)
			if err != nil || len(shards) == 0 {
				return err
			}
			ids, err = s.readRollupShardReader(reader, shards[0])
			return err
		})
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			if err := s.Update(ctx, func(tx *Tx) error { return tx.deleteRollup(id) }); err != nil {
				return err
			}
		}
	}
}
