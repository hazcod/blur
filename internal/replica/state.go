package replica

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxStateBytes = 1 << 20

// State is the per-device replication state. It lives in replica/state.json
// inside the app data directory and holds no secrets; the derived key is kept
// in the OS secret store.
type State struct {
	Version           int               `json:"version"`
	DeviceID          string            `json:"device_id"`
	Enabled           bool              `json:"enabled"`
	FolderPath        string            `json:"folder_path,omitempty"`
	Bookmark          []byte            `json:"bookmark,omitempty"`
	KeySet            bool              `json:"key_set,omitempty"`
	ObsWatermark      int64             `json:"obs_watermark,omitempty"`  // diagnostic/legacy maximum exported capture time; not an export cursor
	TombWatermark     int64             `json:"tomb_watermark,omitempty"` // diagnostic/legacy maximum exported tombstone time; not an export cursor
	TombSequence      uint64            `json:"tomb_sequence,omitempty"`  // committed local cutoff change cursor
	LastSeq           uint64            `json:"last_seq,omitempty"`
	LastBackupAt      time.Time         `json:"last_backup_at,omitempty"`
	OtherDevices      int               `json:"other_devices,omitempty"`
	Imported          map[string]uint64 `json:"imported,omitempty"`
	ConfigVersion     uint64            `json:"config_version,omitempty"`
	Pending           *PendingState     `json:"pending_export,omitempty"`
	OutboxInvalid     bool              `json:"outbox_invalid,omitempty"`
	ObservationCursor []byte            `json:"observation_cursor,omitempty"`
	CaptureLastSeq    uint64            `json:"capture_last_seq,omitempty"`
	CaptureCursor     string            `json:"capture_cursor,omitempty"`
	CaptureImported   map[string]uint64 `json:"capture_imported,omitempty"`
	CapturePending    *PendingState     `json:"capture_pending_export,omitempty"`
}

// PendingState binds one private sealed outbox to its immutable public identity.
// No captured text, observation identifiers, or key material enters state.json.
type PendingState struct {
	Sequence uint64 `json:"sequence"`
	Hash     string `json:"hash"`
}

// StateFile serializes access to State and persists changes atomically.
type StateFile struct {
	path       string
	mu         sync.Mutex
	st         State
	exportMu   sync.Mutex
	operations map[*stateOperation]struct{}
}

type stateOperation struct {
	cancel context.CancelFunc
	export bool
}

// OpenState loads path, or starts empty with a fresh device ID if missing.
func OpenState(path string) (*StateFile, error) {
	f := &StateFile{path: path}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	case len(data) > maxStateBytes || json.Unmarshal(data, &f.st) != nil:
		return nil, ErrFormat
	}
	if _, ok := ParseDeviceID(f.st.DeviceID); !ok {
		if f.st.Pending != nil {
			return nil, ErrFormat
		}
		id, err := NewDeviceID()
		if err != nil {
			return nil, err
		}
		f.st.DeviceID = id
	}
	if f.st.Pending != nil {
		pending := f.st.Pending
		if pending.Sequence != f.st.LastSeq+1 || pending.Sequence > 999999999999 || len(pending.Hash) != 64 || pending.Hash != strings.ToLower(pending.Hash) {
			return nil, ErrFormat
		}
		if _, err := hex.DecodeString(pending.Hash); err != nil {
			return nil, ErrFormat
		}
	}
	if f.st.CapturePending != nil {
		pending := f.st.CapturePending
		if pending.Sequence != f.st.CaptureLastSeq+1 || pending.Sequence > 999999999999 || len(pending.Hash) != 64 || pending.Hash != strings.ToLower(pending.Hash) {
			return nil, ErrFormat
		}
		if _, err := hex.DecodeString(pending.Hash); err != nil {
			return nil, ErrFormat
		}
	}
	if len(f.st.CaptureCursor) > 256 || f.st.CaptureLastSeq > 999999999999 {
		return nil, ErrFormat
	}
	f.st.Version = 1
	return f, nil
}

// Get returns a copy of the current state.
func (f *StateFile) Get() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneState(f.st)
}

// Update applies fn and persists the result. On a write error the in-memory
// state is rolled back.
func (f *StateFile) Update(fn func(*State)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	next := cloneState(f.st)
	fn(&next)
	changed := !sameConfiguration(f.st, next)
	if changed {
		if f.st.ConfigVersion == ^uint64(0) {
			return ErrFormat
		}
		next.ConfigVersion = f.st.ConfigVersion + 1
	}
	next.Version = 1
	if err := writeStateFile(f.path, next); err != nil {
		return err
	}
	f.st = next
	if changed {
		for op := range f.operations {
			op.cancel()
		}
	}
	return nil
}

func sameConfiguration(a, b State) bool {
	return a.DeviceID == b.DeviceID && a.Enabled == b.Enabled && a.FolderPath == b.FolderPath && a.KeySet == b.KeySet && bytes.Equal(a.Bookmark, b.Bookmark) && a.ConfigVersion == b.ConfigVersion
}

func (f *StateFile) beginOperation(parent context.Context, exporting bool) (context.Context, State, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := parent.Err(); err != nil {
		return nil, State{}, nil, err
	}
	if !f.st.Enabled {
		return nil, State{}, nil, ErrDisabled
	}
	ctx, cancel := context.WithCancel(parent)
	op := &stateOperation{cancel: cancel, export: exporting}
	if f.operations == nil {
		f.operations = make(map[*stateOperation]struct{})
	}
	f.operations[op] = struct{}{}
	done := func() {
		cancel()
		f.mu.Lock()
		delete(f.operations, op)
		f.mu.Unlock()
	}
	return ctx, cloneState(f.st), done, nil
}

// Configuration commits and external publication share this lock. A successful
// disable/folder change therefore excludes every later publication by old work.
func (f *StateFile) withCurrent(ctx context.Context, snapshot State, fn func() error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !f.st.Enabled || !sameConfiguration(snapshot, f.st) {
		return ErrDisabled
	}
	return fn()
}

func (f *StateFile) updateCurrent(ctx context.Context, snapshot State, fn func(*State)) error {
	return f.withCurrent(ctx, snapshot, func() error {
		next := cloneState(f.st)
		fn(&next)
		if err := writeStateFile(f.path, next); err != nil {
			return err
		}
		f.st = next
		return nil
	})
}

func cloneState(s State) State {
	s.ObservationCursor = append([]byte(nil), s.ObservationCursor...)
	s.CaptureImported = make(map[string]uint64, len(s.CaptureImported))
	for k, v := range s.CaptureImported {
		s.CaptureImported[k] = v
	}
	s.Bookmark = append([]byte(nil), s.Bookmark...)
	imported := make(map[string]uint64, len(s.Imported))
	for k, v := range s.Imported {
		imported[k] = v
	}
	s.Imported = imported
	if s.Pending != nil {
		pending := *s.Pending
		s.Pending = &pending
	}
	if s.CapturePending != nil {
		pending := *s.CapturePending
		s.CapturePending = &pending
	}
	return s
}

func writeStateFile(path string, s State) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if len(data) > maxStateBytes {
		return ErrFormat
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil { // #nosec G703 -- path is the application-owned replica state location, not remote input.
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, path) // #nosec G703 -- Publish the app-owned temporary state file within its private parent directory.
	}
	if err == nil {
		err = syncDirectory(filepath.Dir(path))
	}
	if err != nil {
		_ = os.Remove(name) // #nosec G703 -- name comes directly from os.CreateTemp in the private state directory.
	}
	return err
}
