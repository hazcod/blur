package durable

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const rollupWorkNS = "chronicle-rollup-work"
const rollupWorkBackfillVersion = 2 // adds calendar ancestors to version1 discovery

// The dirty index is additive, encrypted, and independent of the recent query
// window. A shard contains at most one month of days or one ISO year of weeks.
// Cursors use strict ordering, so deleting the cursor row cannot strand work.
type rollupWorkBackfill struct {
	Version int
	Epoch   uint64
	Zone    string
	Phase   int // 0: UTC hour dates; 1: existing rollups; 2: complete
	After   string
}

func (s *Store) workStrings(reader bolt.Reader, key string, limit int) ([]string, error) {
	raw, found, err := s.readIndexValueReader(reader, rollupWorkNS, key, 256<<10)
	if err != nil || !found {
		return nil, err
	}
	defer clear(raw)
	var values []string
	if json.Unmarshal(raw, &values) != nil || len(values) > limit || !sort.StringsAreSorted(values) {
		return nil, ErrCorrupt
	}
	for i, value := range values {
		if i > 0 && value == values[i-1] {
			return nil, ErrCorrupt
		}
		if key == "shards" {
			if !validWorkShard(value) {
				return nil, ErrCorrupt
			}
		} else if !validRollupID(value) || "shard:"+rollupShard(value) != key {
			return nil, ErrCorrupt
		}
	}
	return values, nil
}

func validWorkShard(shard string) bool {
	if strings.HasPrefix(shard, "day:") {
		_, err := time.Parse("2006-01", strings.TrimPrefix(shard, "day:"))
		return err == nil
	}
	if strings.HasPrefix(shard, "week:") || strings.HasPrefix(shard, "month:") || strings.HasPrefix(shard, "year:") {
		_, err := time.Parse("2006", shard[strings.IndexByte(shard, ':')+1:])
		return err == nil
	}
	return false
}

func (s *Store) rollupWorkCount(reader bolt.Reader) (int, error) {
	raw, found, err := s.readIndexValueReader(reader, rollupWorkNS, "count", 32)
	if err != nil || !found {
		return 0, err
	}
	defer clear(raw)
	var count int
	if json.Unmarshal(raw, &count) != nil || count < 0 || count > maxRollupShards*64 {
		return 0, ErrCorrupt
	}
	return count, nil
}

// syncRollupWork runs in the same transaction as every rollup write/deletion.
// Check membership itself rather than trusting old.Dirty: legacy dirty rows
// have no queue entry until the bounded backfill reaches them.
func (tx *Tx) syncRollupWork(id string, dirty bool) error {
	shard := rollupShard(id)
	key := "shard:" + shard
	ids, err := tx.store.workStrings(tx.writer, key, 64)
	if err != nil {
		return err
	}
	i := sort.SearchStrings(ids, id)
	present := i < len(ids) && ids[i] == id
	if present == dirty {
		return nil
	}
	count, err := tx.store.rollupWorkCount(tx.writer)
	if err != nil {
		return err
	}
	if dirty {
		if len(ids) >= 64 || count >= maxRollupShards*64 {
			return ErrCorrupt
		}
		ids = slices.Insert(ids, i, id)
		count++
	} else {
		if count == 0 {
			return ErrCorrupt
		}
		ids = slices.Delete(ids, i, i+1)
		count--
	}
	if dirty && len(ids) == 1 || !dirty && len(ids) == 0 {
		shards, err := tx.store.workStrings(tx.writer, "shards", maxRollupShards)
		if err != nil {
			return err
		}
		i := sort.SearchStrings(shards, shard)
		if dirty {
			if i < len(shards) && shards[i] == shard || len(shards) >= maxRollupShards {
				return ErrCorrupt
			}
			shards = slices.Insert(shards, i, shard)
		} else {
			if i >= len(shards) || shards[i] != shard {
				return ErrCorrupt
			}
			shards = slices.Delete(shards, i, i+1)
		}
		if err := tx.PutIndexRecord(rollupWorkNS, "shards", mustJSON(shards)); err != nil {
			return err
		}
	}
	if len(ids) == 0 {
		if err := tx.deleteKnowledgeRecord(rollupWorkNS, key); err != nil {
			return err
		}
	} else if err := tx.PutIndexRecord(rollupWorkNS, key, mustJSON(ids)); err != nil {
		return err
	}
	return tx.PutIndexRecord(rollupWorkNS, "count", mustJSON(count))
}

func (s *Store) rollupBackfillReader(reader bolt.Reader) (rollupWorkBackfill, error) {
	var state rollupWorkBackfill
	raw, found, err := s.readIndexValueReader(reader, rollupWorkNS, "backfill", 512)
	if err != nil || !found {
		return state, err
	}
	defer clear(raw)
	if json.Unmarshal(raw, &state) != nil || (state.Version != 1 && state.Version != rollupWorkBackfillVersion) || state.Phase < 0 || state.Phase > 2 {
		return state, ErrCorrupt
	}
	if _, err := time.LoadLocation(state.Zone); err != nil {
		return state, ErrCorrupt
	}
	if state.After != "" && (state.Phase == 0 && !chronicle.ValidDayKey(state.After) || state.Phase == 1 && !validRollupID(state.After) || state.Phase == 2) {
		return state, ErrCorrupt
	}
	return state, nil
}

// PrepareChronicleRollupWork processes at most four indexed UTC dates OR
// sixteen existing rows. No original text is read. Cursor and queue changes
// commit together; process restarts and erasure/zone epochs resume safely.
func (s *Store) PrepareChronicleRollupWork(ctx context.Context, zone string) (chronicle.RollupWorkStatus, error) {
	loc, err := time.LoadLocation(zone)
	if ctx == nil || err != nil {
		return chronicle.RollupWorkStatus{}, &storage.StoreError{Code: storage.Invalid}
	}
	err = s.Update(ctx, func(tx *Tx) error {
		if _, pending, err := s.chronicleErasureReader(tx.writer); err != nil {
			return err
		} else if pending {
			_, err := tx.resumeChronicleErasure()
			return err
		}
		state, err := s.rollupBackfillReader(tx.writer)
		if err != nil {
			return err
		}
		epoch, err := s.rollupEpochReader(tx.writer)
		if err != nil {
			return err
		}
		if state.Version != rollupWorkBackfillVersion || state.Epoch != epoch || state.Zone != zone {
			state = rollupWorkBackfill{Version: rollupWorkBackfillVersion, Epoch: epoch, Zone: zone}
		}
		if state.Phase == 2 {
			return nil
		}
		if state.Phase == 0 {
			dates, _, err := tx.readChronicleDays()
			if err != nil {
				return err
			}
			i := sort.Search(len(dates), func(i int) bool { return dates[i] > state.After })
			end := min(i+4, len(dates))
			seen := map[string]bool{}
			for ; i < end; i++ {
				ids, _, err := tx.readChronicleDay(dates[i])
				if err != nil {
					return err
				}
				for _, id := range ids {
					if !validChronicleHourID(id) {
						return ErrCorrupt
					}
					at, err := time.Parse("20060102T15Z", strings.TrimPrefix(id, "hour:"))
					if err != nil {
						return ErrCorrupt
					}
					// Include both sides of a fractional-offset midnight. The
					// worker excludes straddling hours and discloses that gap.
					for _, instant := range []time.Time{at, at.Add(time.Hour - time.Nanosecond)} {
						day := chronicle.DayRollupID(chronicle.DayKeyAt(instant, loc))
						if seen[day] {
							continue
						}
						seen[day] = true
						if err := tx.ensureRollupWork(day, zone, epoch); err != nil {
							return err
						}
						parents, _ := chronicle.RollupAncestorIDs(day)
						for _, parent := range parents {
							if err := tx.ensureRollupWork(parent, zone, epoch); err != nil {
								return err
							}
						}
					}
				}
				state.After = dates[i]
			}
			if end == len(dates) {
				state.Phase, state.After = 1, ""
			}
		} else {
			for n := 0; n < 16; n++ {
				id, err := s.nextExistingRollup(tx.writer, state.After)
				if err != nil {
					return err
				}
				if id == "" {
					state.Phase, state.After = 2, ""
					break
				}
				if err := tx.ensureRollupWork(id, zone, epoch); err != nil {
					return err
				}
				parents, _ := chronicle.RollupAncestorIDs(id)
				for _, parent := range parents {
					if err := tx.ensureRollupWork(parent, zone, epoch); err != nil {
						return err
					}
				}
				state.After = id
			}
		}
		return tx.PutIndexRecord(rollupWorkNS, "backfill", mustJSON(state))
	})
	if err != nil {
		return chronicle.RollupWorkStatus{}, err
	}
	return s.ChronicleRollupWorkStatus(ctx)
}

func (tx *Tx) ensureRollupWork(id, zone string, epoch uint64) error {
	r, found, err := tx.readRollup(id)
	if err != nil {
		return err
	}
	if !found {
		r, err = chronicle.NewRollupPlaceholder(id, zone)
		if err != nil {
			return err
		}
		return tx.writeRollup(r, nil)
	}
	old := r
	changed := false
	if r.Zone != zone || r.Epoch != epoch {
		fresh, err := chronicle.NewRollupPlaceholder(id, zone)
		if err != nil {
			return err
		}
		r.Zone, r.Start, r.End = zone, fresh.Start, fresh.End
		clearRollupContent(&r)
		r.Dirty, r.Revision = true, r.Revision+1
		changed = true
	} else if r.Text != "" && r.CoverageVersion == 0 && !r.Dirty {
		r.Dirty, r.Revision = true, r.Revision+1
		changed = true
	}
	if changed {
		return tx.writeRollup(r, &old)
	}
	if r.Dirty {
		return tx.syncRollupWork(id, true)
	}
	return nil
}

// Zone/epoch rebasing and staged legacy cleanup retain reverse receipts while
// discarding text. The erasure walker can still remove those receipts; actual
// hourly erasure additionally clears public child/source fields immediately.
func clearRollupContent(r *chronicle.Rollup) {
	r.Text, r.InputHash, r.Model, r.GeneratedAt = "", "", "", time.Time{}
	r.CoverageVersion, r.Sampled = 0, false
}

func (s *Store) nextExistingRollup(reader bolt.Reader, after string) (string, error) {
	shards, err := s.readRollupShardsReader(reader)
	if err != nil {
		return "", err
	}
	for _, shard := range shards {
		if after != "" && shard < rollupShard(after) {
			continue
		}
		ids, err := s.readRollupShardReader(reader, shard)
		if err != nil {
			return "", err
		}
		i := sort.Search(len(ids), func(i int) bool { return ids[i] > after })
		if i < len(ids) {
			return ids[i], nil
		}
	}
	return "", nil
}

func (s *Store) ChronicleRollupWorkStatus(ctx context.Context) (chronicle.RollupWorkStatus, error) {
	var status chronicle.RollupWorkStatus
	err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		_, status.ErasurePending, err = s.chronicleErasureReader(reader)
		if err != nil {
			return err
		}
		status.Dirty, err = s.rollupWorkCount(reader)
		if err != nil {
			return err
		}
		state, err := s.rollupBackfillReader(reader)
		if err != nil {
			return err
		}
		epoch, err := s.rollupEpochReader(reader)
		_, zone := s.rollupLocation()
		invalid := state.Version != rollupWorkBackfillVersion || state.Epoch != epoch || state.Zone != zone
		status.BackfillPending = status.ErasurePending || invalid || state.Phase != 2
		status.InputsPending = status.ErasurePending || invalid || state.Phase == 0
		return err
	})
	return status, err
}

// NextChronicleRollupWork rotates within one level, even for blocked periods.
// A claim refreshes old epochs/bounds atomically, without accepting a stale
// model publication. An outstanding generation still uses revision+epoch CAS.
func (s *Store) NextChronicleRollupWork(ctx context.Context, level, zone string) (chronicle.RollupWorkItem, bool, error) {
	if ctx == nil || !chronicle.ValidRollupLevel(level) {
		return chronicle.RollupWorkItem{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return chronicle.RollupWorkItem{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	var row chronicle.Rollup
	cleanupPending := false
	found := false
	err := s.Update(ctx, func(tx *Tx) error {
		if _, pending, err := s.chronicleErasureReader(tx.writer); err != nil {
			return err
		} else if pending {
			return nil
		}
		shards, err := s.workStrings(tx.writer, "shards", maxRollupShards)
		if err != nil {
			return err
		}
		raw, hasCursor, err := s.readIndexValueReader(tx.writer, rollupWorkNS, "cursor:"+level, 64)
		if err != nil {
			return err
		}
		after := string(raw)
		clear(raw)
		if hasCursor && (!validRollupID(after) || !strings.HasPrefix(after, level+":")) {
			return ErrCorrupt
		}
		first, next := "", ""
		for _, shard := range shards {
			if !strings.HasPrefix(shard, level+":") {
				continue
			}
			if first != "" && after != "" && shard < rollupShard(after) {
				continue
			}
			ids, err := s.workStrings(tx.writer, "shard:"+shard, 64)
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				return ErrCorrupt
			}
			if first == "" {
				first = ids[0]
			}
			i := sort.Search(len(ids), func(i int) bool { return ids[i] > after })
			if i < len(ids) {
				next = ids[i]
				break
			}
		}
		if next == "" {
			next = first
		}
		if next == "" {
			return nil
		}
		epoch, err := s.rollupEpochReader(tx.writer)
		if err != nil {
			return err
		}
		if err := tx.ensureRollupWork(next, zone, epoch); err != nil {
			return err
		}
		row, found, err = tx.readRollup(next)
		if err != nil {
			return err
		}
		if !found || !row.Dirty {
			return ErrCorrupt
		}
		// Drain old reverse obligations before generation. These are separate
		// from the now-empty public evidence fields after input erasure.
		if len(row.SourceIDs) > chronicle.MaxRollupGeneratedSources {
			if err := tx.stageRollupCleanup(&row, false); err != nil {
				return err
			}
		}
		var deleted bool
		cleanupPending, deleted, err = tx.drainRollupCleanup(&row)
		if err != nil {
			return err
		}
		if deleted {
			cleanupPending = true
		}
		return tx.PutIndexRecord(rollupWorkNS, "cursor:"+level, []byte(next))
	})
	return chronicle.RollupWorkItem{Rollup: row, CleanupPending: cleanupPending}, found, err
}
