package replica

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxStateBytes = 1 << 20

// State is the per-device replication state. It lives in replica/state.json
// inside the app data directory and holds no secrets; the derived key is kept
// in the OS secret store.
type State struct {
	Version       int               `json:"version"`
	DeviceID      string            `json:"device_id"`
	Enabled       bool              `json:"enabled"`
	FolderPath    string            `json:"folder_path,omitempty"`
	Bookmark      []byte            `json:"bookmark,omitempty"`
	KeySet        bool              `json:"key_set,omitempty"`
	ObsWatermark  int64             `json:"obs_watermark,omitempty"`  // unix nanos of last exported capture time
	TombWatermark int64             `json:"tomb_watermark,omitempty"` // unix nanos of last exported tombstone
	LastSeq       uint64            `json:"last_seq,omitempty"`
	LastBackupAt  time.Time         `json:"last_backup_at,omitempty"`
	OtherDevices  int               `json:"other_devices,omitempty"`
	Imported      map[string]uint64 `json:"imported,omitempty"`
}

// StateFile serializes access to State and persists changes atomically.
type StateFile struct {
	path string
	mu   sync.Mutex
	st   State
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
		id, err := NewDeviceID()
		if err != nil {
			return nil, err
		}
		f.st.DeviceID = id
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
	next.Version = 1
	if err := writeStateFile(f.path, next); err != nil {
		return err
	}
	f.st = next
	return nil
}

func cloneState(s State) State {
	s.Bookmark = append([]byte(nil), s.Bookmark...)
	imported := make(map[string]uint64, len(s.Imported))
	for k, v := range s.Imported {
		imported[k] = v
	}
	s.Imported = imported
	return s
}

func writeStateFile(path string, s State) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}
