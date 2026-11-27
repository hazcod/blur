package replica

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"github.com/hazcod/blur/internal/storage/durable"
)

// This private envelope never leaves the device. The public segment inside is
// byte-for-byte the existing v1 peer format; receipt revisions stay local.
type pendingExport struct {
	segment           []byte
	items             []durable.ExportedObservation
	records           []Record
	obsMark, tombMark int64
	tombSequence      uint64
	nextCursor        []byte
}

func (p *pendingExport) clear() {
	clear(p.segment)
	for _, item := range p.items {
		clear(item.Record)
	}
	for _, record := range p.records {
		clear(record.Observation)
	}
}

func (f *StateFile) outboxPath(device, hash string) string {
	return filepath.Join(filepath.Dir(f.path), "outbox-"+device+"-"+hash+".sealed")
}

func (f *StateFile) captureOutboxPath(device, hash string) string {
	return filepath.Join(filepath.Dir(f.path), "capture-outbox-"+device+"-"+hash+".sealed")
}

func encodePending(key []byte, p pendingExport) ([]byte, error) {
	if len(p.segment) > MaxSegmentPlaintext+headerSize+64 || len(p.items) > flushMaxObservations || len(p.nextCursor) > 34 {
		return nil, ErrFormat
	}
	plain := make([]byte, 34, 34+len(p.nextCursor)+len(p.segment)+32*len(p.items))
	plain[0] = 1
	binary.BigEndian.PutUint64(plain[1:9], p.tombSequence)
	binary.BigEndian.PutUint64(plain[9:17], uint64(p.obsMark))
	binary.BigEndian.PutUint64(plain[17:25], uint64(p.tombMark))
	binary.BigEndian.PutUint32(plain[25:29], uint32(len(p.segment)))
	binary.BigEndian.PutUint32(plain[29:33], uint32(len(p.items)))
	plain[33] = byte(len(p.nextCursor))
	plain = append(plain, p.nextCursor...)
	plain = append(plain, p.segment...)
	for _, item := range p.items {
		plain = append(plain, item.Revision[:]...)
	}
	defer clear(plain)
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, []byte("blur-replica-local-outbox-v1")), nil
}

func decodePending(key, sealed []byte, device string, sequence uint64) (pendingExport, error) {
	var p pendingExport
	gcm, err := newGCM(key)
	if err != nil {
		return p, err
	}
	if len(sealed) < gcm.NonceSize()+gcm.Overhead() {
		return p, ErrFormat
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], []byte("blur-replica-local-outbox-v1"))
	if err != nil {
		return p, ErrAuth
	}
	defer clear(plain)
	if len(plain) < 34 || plain[0] != 1 {
		return p, ErrFormat
	}
	size, count := int(binary.BigEndian.Uint32(plain[25:29])), int(binary.BigEndian.Uint32(plain[29:33]))
	cursorSize := int(plain[33])
	if cursorSize > 34 || size > MaxSegmentPlaintext+headerSize+64 || count > flushMaxObservations || size > len(plain)-34-cursorSize || len(plain)-34-cursorSize-size != 32*count {
		return p, ErrFormat
	}
	p.nextCursor = bytes.Clone(plain[34 : 34+cursorSize])
	p.segment = bytes.Clone(plain[34+cursorSize : 34+cursorSize+size])
	header, records, err := OpenSegment(key, p.segment)
	if err != nil || FormatDeviceID(header.DeviceID) != device || header.Seq != sequence {
		p.clear()
		if err != nil {
			return pendingExport{}, err
		}
		return pendingExport{}, ErrFormat
	}
	p.records = records
	p.tombSequence = binary.BigEndian.Uint64(plain[1:9])
	p.obsMark = int64(binary.BigEndian.Uint64(plain[9:17]))
	p.tombMark = int64(binary.BigEndian.Uint64(plain[17:25]))
	revisions := plain[34+cursorSize+size:]
	for _, record := range records {
		if record.Kind == KindObservation {
			if len(revisions) < 32 {
				p.clear()
				return pendingExport{}, ErrFormat
			}
			item := durable.ExportedObservation{Record: bytes.Clone(record.Observation), CapturedAt: record.At}
			copy(item.Revision[:], revisions[:32])
			revisions = revisions[32:]
			p.items = append(p.items, item)
		}
	}
	if len(revisions) != 0 || len(p.items) != count {
		p.clear()
		return pendingExport{}, ErrFormat
	}
	return p, nil
}

func outboxHash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, ErrFormat
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if int64(len(data)) > limit {
		clear(data)
		return nil, ErrFormat
	}
	return data, err
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// Exclusive rename publishes only a complete synced temporary file, without
// replacing an existing identity. Replays accept exact bytes and reject conflicts.
func publishImmutable(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".segment-*")
	if err != nil {
		return err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	target := filepath.Join(dir, name)
	if err = renameExclusive(path, target); os.IsExist(err) {
		old, readErr := readBoundedFile(target, int64(MaxSegmentPlaintext+headerSize+64))
		defer clear(old)
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(old, data) {
			return ErrFormat
		}
		return nil
	}
	if err != nil {
		return err
	}
	return syncDirectory(dir)
}
