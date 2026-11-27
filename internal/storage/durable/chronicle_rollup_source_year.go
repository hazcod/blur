package durable

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const (
	rollupSourceYearNS         = "chronicle-rollup-source-years"
	rollupSourceDirectoryBytes = 10000 / 8 // complete four-digit period-year domain
	maxRollupSourcesPerYear    = 366 + 53 + 12 + 1
	maxRollupEmptyYearsPerDrop = 16
)

func rollupSourceYear(id string) (int, bool) {
	if !validRollupID(id) {
		return 0, false
	}
	_, period, _ := strings.Cut(id, ":")
	if len(period) < 4 {
		return 0, false
	}
	year, err := strconv.Atoi(period[:4])
	return year, err == nil && year >= 0 && year < 10000
}

func rollupSourceYearKey(source string, year int) string { return fmt.Sprintf("%s:%04d", source, year) }

func (s *Store) rollupSourceDirectory(reader bolt.Reader, source string) ([]byte, bool, error) {
	raw, found, err := s.readIndexValueReader(reader, rollupSourceYearNS, "directory:"+source, rollupSourceDirectoryBytes+1)
	if err != nil || !found {
		return raw, found, err
	}
	defer clear(raw)
	directory := make([]byte, rollupSourceDirectoryBytes)
	valid := false
	if len(raw) == rollupSourceDirectoryBytes+1 && raw[0] == 2 {
		copy(directory, raw[1:])
		for _, b := range directory {
			if b != 0 {
				valid = true
				break
			}
		}
	} else if len(raw) > 1 && raw[0] == 1 && (len(raw)-1)%3 == 0 {
		last := -1
		valid = true
		for i := 1; i < len(raw); i += 3 {
			index := int(binary.BigEndian.Uint16(raw[i : i+2]))
			if index <= last || index >= len(directory) || raw[i+2] == 0 {
				valid = false
				break
			}
			directory[index], last = raw[i+2], index
		}
	}
	if !valid {
		clear(directory)
		return nil, false, ErrCorrupt
	}
	return directory, true, nil
}

// Common directories contain one year and occupy four plaintext bytes rather
// than1250. The complete four-digit year domain still has a fixed ceiling.
func (tx *Tx) putRollupSourceDirectory(source string, directory []byte) error {
	if len(directory) != rollupSourceDirectoryBytes {
		return ErrCorrupt
	}
	encoded := []byte{1}
	for i, b := range directory {
		if b == 0 {
			continue
		}
		encoded = binary.BigEndian.AppendUint16(encoded, uint16(i))
		encoded = append(encoded, b)
	}
	if len(encoded) > rollupSourceDirectoryBytes+1 {
		clear(encoded)
		encoded = append([]byte{2}, directory...)
	}
	defer clear(encoded)
	if len(encoded) == 1 {
		return ErrCorrupt
	}
	return tx.PutIndexRecord(rollupSourceYearNS, "directory:"+source, encoded)
}

func (s *Store) rollupSourceYearReader(reader bolt.Reader, source string, year int) ([]string, bool, error) {
	raw, found, err := s.readIndexValueReader(reader, rollupSourceYearNS, rollupSourceYearKey(source, year), 16<<10)
	if err != nil || !found {
		return nil, found, err
	}
	defer clear(raw)
	var ids []string
	if json.Unmarshal(raw, &ids) != nil || len(ids) > maxRollupSourcesPerYear || !sort.StringsAreSorted(ids) {
		return nil, false, ErrCorrupt
	}
	for i, id := range ids {
		ownYear, ok := rollupSourceYear(id)
		if !ok || ownYear != year || i > 0 && ids[i-1] == id {
			return nil, false, ErrCorrupt
		}
	}
	return ids, true, nil
}

// Existing leaves, including empty ones, remain catalogued during ordinary
// rewrites. This bounds a disjoint64->64 update without touching directory
// records for every old source. Only the bounded erasure walker prunes them.
func (tx *Tx) appendRollupSourceYear(source, id string) error {
	year, ok := rollupSourceYear(id)
	if !ok || !validHash(source) {
		return ErrCorrupt
	}
	ids, found, err := tx.store.rollupSourceYearReader(tx.writer, source, year)
	if err != nil {
		return err
	}
	i := sort.SearchStrings(ids, id)
	if i < len(ids) && ids[i] == id {
		return nil
	}
	if len(ids) >= maxRollupSourcesPerYear {
		return ErrCorrupt
	}
	if !found {
		directory, exists, err := tx.store.rollupSourceDirectory(tx.writer, source)
		if err != nil {
			return err
		}
		if !exists {
			directory = make([]byte, rollupSourceDirectoryBytes)
		}
		defer clear(directory)
		mask := byte(1 << uint(year%8))
		if directory[year/8]&mask != 0 {
			return ErrCorrupt
		}
		directory[year/8] |= mask
		if err := tx.putRollupSourceDirectory(source, directory); err != nil {
			return err
		}
	}
	ids = slices.Insert(ids, i, id)
	return tx.PutIndexRecord(rollupSourceYearNS, rollupSourceYearKey(source, year), mustJSON(ids))
}

// A given edge stays in its original representation until removed. We never
// eagerly expand a legacy512array into hundreds of new records on publication.
func (tx *Tx) unlinkRollupSource(source, id string) (bool, error) {
	year, ok := rollupSourceYear(id)
	if !ok {
		return false, ErrCorrupt
	}
	ids, _, err := tx.store.rollupSourceYearReader(tx.writer, source, year)
	if err != nil {
		return false, err
	}
	i := sort.SearchStrings(ids, id)
	if i < len(ids) && ids[i] == id {
		ids = slices.Delete(ids, i, i+1)
		return true, tx.PutIndexRecord(rollupSourceYearNS, rollupSourceYearKey(source, year), mustJSON(ids))
	}
	ids, found, err := tx.readRollupSources(source)
	if err != nil || !found {
		return false, err
	}
	i = sort.SearchStrings(ids, id)
	if i == len(ids) || ids[i] != id {
		return false, nil
	}
	ids = slices.Delete(ids, i, i+1)
	if len(ids) == 0 {
		return true, tx.deleteKnowledgeRecord(chronicleRollupSourceNS, source)
	}
	return true, tx.PutIndexRecord(chronicleRollupSourceNS, source, mustJSON(ids))
}

// nextRollupSourceEdge never aggregates history. Empty-year cleanup has its
// own visit allowance, independent of the number of forward rows erased.
func (tx *Tx) nextRollupSourceEdge(source string, emptyVisits *int) (id string, done bool, err error) {
	legacy, _, err := tx.readRollupSources(source)
	if err != nil {
		return "", false, err
	}
	if len(legacy) != 0 {
		return legacy[0], false, nil
	}
	for {
		directory, found, err := tx.store.rollupSourceDirectory(tx.writer, source)
		if err != nil || !found {
			return "", !found && err == nil, err
		}
		year := -1
		for n, b := range directory {
			if b == 0 {
				continue
			}
			for bit := 0; bit < 8; bit++ {
				if b&byte(1<<uint(bit)) != 0 {
					year = n*8 + bit
					break
				}
			}
			break
		}
		if year < 0 {
			clear(directory)
			return "", false, ErrCorrupt
		}
		ids, found, err := tx.store.rollupSourceYearReader(tx.writer, source, year)
		if err != nil || !found {
			clear(directory)
			if err == nil {
				err = ErrCorrupt
			}
			return "", false, err
		}
		if len(ids) != 0 {
			clear(directory)
			return ids[0], false, nil
		}
		if *emptyVisits >= maxRollupEmptyYearsPerDrop {
			clear(directory)
			return "", false, nil
		}
		*emptyVisits++
		if err := tx.deleteKnowledgeRecord(rollupSourceYearNS, rollupSourceYearKey(source, year)); err != nil {
			clear(directory)
			return "", false, err
		}
		directory[year/8] &^= byte(1 << uint(year%8))
		empty := true
		for _, b := range directory {
			if b != 0 {
				empty = false
				break
			}
		}
		if empty {
			err = tx.deleteKnowledgeRecord(rollupSourceYearNS, "directory:"+source)
		} else {
			err = tx.putRollupSourceDirectory(source, directory)
		}
		clear(directory)
		if err != nil {
			return "", false, err
		}
	}
}
