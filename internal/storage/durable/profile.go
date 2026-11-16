package durable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/hazcod/blur/internal/profile"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const (
	profileNS             = "profile-v1"
	profileOrderKind byte = 0x08
)

var _ profile.Repository = (*Store)(nil)

func (s *Store) profileUpdate(ctx context.Context, fn func(*Tx) error) error {
	if ctx == nil {
		return profile.ErrInvalid
	}
	if !s.state.profileMu.TryRLock() {
		return ErrUnavailable
	}
	defer s.state.profileMu.RUnlock()
	var outcome error
	err := s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		err := fn(tx)
		for _, expected := range []error{profile.ErrInvalid, profile.ErrConflict, profile.ErrCapacity} {
			if errors.Is(err, expected) {
				outcome = expected
				return &storage.StoreError{Code: storage.Invalid}
			}
		}
		return err
	})
	if err != nil && outcome != nil {
		return outcome
	}
	return err
}

func (s *Store) profileReader(reader bolt.Reader, id string) (profile.Record, bool, error) {
	encoded, found, err := s.readIndexValueReader(reader, profileNS, id, profile.MaxRecordBytes)
	if err != nil || !found {
		return profile.Record{}, found, err
	}
	defer clear(encoded)
	var record profile.Record
	if json.Unmarshal(encoded, &record) != nil || record.ID != id || record.Validate() != nil {
		return profile.Record{}, false, ErrCorrupt
	}
	return record, true, nil
}

func (s *Store) GetProfile(ctx context.Context, id string) (profile.Record, bool, error) {
	if ctx == nil || !profile.ValidID(id) {
		return profile.Record{}, false, profile.ErrInvalid
	}
	var record profile.Record
	var found bool
	err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		record, found, err = s.profileReader(reader, id)
		return err
	})
	return record, found, err
}

func (s *Store) PutProfile(ctx context.Context, record profile.Record, expected uint64) error {
	if ctx == nil || record.Validate() != nil || expected >= 1<<48 || record.Revision != expected+1 {
		return profile.ErrInvalid
	}
	record.CreatedAt, record.UpdatedAt = record.CreatedAt.UTC(), record.UpdatedAt.UTC()
	return s.profileUpdate(ctx, func(tx *Tx) error {
		old, found, err := s.profileReader(tx.writer, record.ID)
		if err != nil {
			return err
		}
		if found && (old.Revision != expected || !old.CreatedAt.Equal(record.CreatedAt) || old.Kind != record.Kind) || !found && expected != 0 {
			return profile.ErrConflict
		}
		if found && record.UpdatedAt.Before(old.UpdatedAt) {
			return profile.ErrInvalid
		}
		encoded := mustJSON(record)
		defer clear(encoded)
		if len(encoded) > profile.MaxRecordBytes {
			return profile.ErrCapacity
		}
		if err := tx.putIndexValueLocked(profileNS, record.ID, encoded); err != nil {
			return err
		}
		key := s.logicalKey(groupOrder, profileOrderKind, profileNS, record.ID)
		defer clear(key)
		envelope, err := s.seal(key, profileNS, []byte(record.ID))
		if err != nil {
			return err
		}
		defer clear(envelope)
		return tx.writeEntryLocked(key, envelope, true)
	})
}

func (s *Store) profileOrderID(key, value []byte) (string, error) {
	if len(value) > 256 {
		return "", ErrCorrupt
	}
	plain, err := s.open(key, profileNS, value)
	if err != nil {
		return "", ErrCorrupt
	}
	defer clear(plain)
	id := string(plain)
	if !profile.ValidID(id) {
		return "", ErrCorrupt
	}
	expected := s.logicalKey(groupOrder, profileOrderKind, profileNS, id)
	defer clear(expected)
	if !bytes.Equal(key, expected) {
		return "", ErrCorrupt
	}
	return id, nil
}

func (s *Store) ListProfile(ctx context.Context, request profile.PageRequest) (profile.Page, error) {
	if ctx == nil || request.Limit < 1 || request.Limit > profile.MaxPageSize || request.After != "" && !profile.ValidID(request.After) {
		return profile.Page{}, profile.ErrInvalid
	}
	var after []byte
	if request.After != "" {
		var err error
		after, err = s.cursorKey(groupOrder, profileOrderKind, profileNS, request.After)
		if err != nil {
			return profile.Page{}, err
		}
		defer clear(after)
	}
	page := profile.Page{Records: []profile.Record{}}
	err := s.view(ctx, func(reader bolt.Reader) error {
		entries, more, err := reader.Scan([]byte{groupOrder, profileOrderKind}, after, request.Limit, 32<<10)
		if err != nil {
			return mapStorePhysicalError(err)
		}
		defer clearBoltEntries(entries)
		page.HasMore = more
		for _, entry := range entries {
			id, err := s.profileOrderID(entry.Key, entry.Value)
			if err != nil {
				return err
			}
			record, found, err := s.profileReader(reader, id)
			if err != nil {
				return err
			}
			if !found {
				return ErrCorrupt
			}
			page.Records = append(page.Records, record)
			page.Next = id
		}
		if !more {
			page.Next = ""
		}
		return nil
	})
	if err != nil {
		return profile.Page{}, err
	}
	return page, nil
}

func (tx *Tx) deleteProfileLocked(id string) error {
	if err := tx.deleteInferredIndex(profileNS, id); err != nil {
		return err
	}
	key := tx.store.logicalKey(groupOrder, profileOrderKind, profileNS, id)
	defer clear(key)
	return tx.deleteEntryLocked(key)
}

func (s *Store) DeleteProfile(ctx context.Context, id string, revision uint64) error {
	if ctx == nil || !profile.ValidID(id) || revision == 0 {
		return profile.ErrInvalid
	}
	return s.profileUpdate(ctx, func(tx *Tx) error {
		record, found, err := s.profileReader(tx.writer, id)
		if err != nil {
			return err
		}
		if !found || record.Revision != revision {
			return profile.ErrConflict
		}
		return tx.deleteProfileLocked(id)
	})
}

func (s *Store) DeleteAllProfile(ctx context.Context) error {
	if ctx == nil {
		return profile.ErrInvalid
	}
	s.state.profileMu.Lock()
	defer s.state.profileMu.Unlock()
	for {
		page, err := s.ListProfile(ctx, profile.PageRequest{Limit: profile.MaxPageSize})
		if err != nil {
			return err
		}
		if len(page.Records) == 0 {
			return nil
		}
		if err := s.Update(ctx, func(tx *Tx) error {
			tx.mu.Lock()
			defer tx.mu.Unlock()
			for _, record := range page.Records {
				if err := tx.deleteProfileLocked(record.ID); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
}
