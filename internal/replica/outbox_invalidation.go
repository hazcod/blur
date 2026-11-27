package replica

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// InvalidateOutbox is called by the memory-erasure admission path, including
// while sync is off. Cleanup failures remain durable, visible, and retryable;
// no invalidated outbox can be published. It never waits for the runner.
func (f *StateFile) InvalidateOutbox() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for operation := range f.operations {
		if operation.export {
			operation.cancel()
		}
	}
	old := cloneState(f.st)
	// Persist the fail-closed gate before removing any referenced file. If the
	// state write fails, retain the gate in memory and still erase private data.
	f.st.OutboxInvalid = true
	firstErr := writeStateFile(f.path, f.st)
	entries, err := os.ReadDir(filepath.Dir(f.path))
	if os.IsNotExist(err) {
		err = nil
	}
	if err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".sealed") || !strings.HasPrefix(name, "outbox-") && !strings.HasPrefix(name, "capture-outbox-") {
				continue
			}
			start := len("outbox-")
			if strings.HasPrefix(name, "capture-outbox-") {
				start = len("capture-outbox-")
			}
			if len(name) != start+36+1+64+len(".sealed") || name[start+36] != '-' {
				continue
			}
			if _, ok := ParseDeviceID(name[start : start+36]); !ok {
				continue
			}
			if _, decodeErr := hex.DecodeString(name[start+37 : start+37+64]); decodeErr != nil {
				continue
			}
			if removeErr := os.Remove(filepath.Join(filepath.Dir(f.path), name)); removeErr != nil && !os.IsNotExist(removeErr) {
				err = errors.Join(err, removeErr)
			}
		}
	}
	if err == nil {
		err = syncDirectory(filepath.Dir(f.path))
	}
	if err != nil {
		return errors.Join(firstErr, err)
	}
	next := cloneState(f.st)
	if old.Pending != nil || old.CapturePending != nil {
		// Publication may already have happened. A new writer identity avoids
		// ever reusing an uncertain old sequence, even if its provider evicted it.
		id, err := NewDeviceID()
		if err != nil {
			return errors.Join(firstErr, err)
		}
		next.DeviceID = id
		next.LastSeq, next.TombSequence, next.CaptureLastSeq = 0, 0, 0
		next.ObsWatermark, next.TombWatermark = 0, 0
		next.ObservationCursor = nil
		next.CaptureCursor = ""
		next.LastBackupAt = time.Time{}
		if next.ConfigVersion == ^uint64(0) {
			return errors.Join(firstErr, ErrFormat)
		}
		next.ConfigVersion++
	}
	next.Pending, next.CapturePending, next.OutboxInvalid = nil, nil, false
	// A deletion can remove the publication pinned by the paged capture cursor.
	// Restart the bounded sweep; authenticated export receipts skip old parts.
	next.CaptureCursor = ""
	if err := writeStateFile(f.path, next); err != nil {
		return errors.Join(firstErr, err)
	}
	f.st = next
	return firstErr
}
