// Package durable provides authenticated encrypted persistence for storage
// observations, derivations, and generations.
package durable

import (
	"bytes"
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/privacy"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/storage/recordcodec"
)

const (
	OpenSchemaVersion = 2
	valueVersion      = 1
	recordRevision    = 1
	storedValueHeader = 1 + 4
	identityHeader    = 1 + 16 + 4
	identityPlainSize = 1 + 16 + 4

	groupRecord byte = 0x10
	groupOrder  byte = 0x20
	groupActive byte = 0x30

	kindObservation byte = 0x01
	kindDerivation  byte = 0x02
	kindGeneration  byte = 0x03
	kindIndex       byte = 0x04

	physicalKeyBytes      = 34
	legacySizeRecordBytes = 1 + 4 + physicalKeyBytes
	sizeRecordBytes       = 1 + 4 + 8 + physicalKeyBytes

	maxLogicalIDBytes = 4096
)

var (
	ErrOpenMode                         = errors.New("durable store: invalid open mode")
	ErrInvalidPath                      = errors.New("durable store: invalid path")
	ErrAlreadyExists                    = errors.New("durable store: store already exists")
	ErrMissing                          = errors.New("durable store: store not found")
	ErrWrongKey                         = errors.New("durable store: key rejected")
	ErrCorrupt                    error = &classifiedStoreError{code: "corrupt", message: "durable store: corrupt store"}
	ErrUnsupportedSchema                = errors.New("durable store: unsupported schema")
	ErrMigration                        = errors.New("durable store: migration failed")
	ErrProjectionIndexUnavailable       = errors.New("durable store: projection index migration required")
	ErrUnavailable                      = errors.New("durable store: unavailable")
	ErrTransaction                error = &classifiedStoreError{code: "transaction", message: "durable store: transaction failed"}

	errAbortTransaction = errors.New("durable store: abort")
)

type classifiedStoreError struct{ code, message string }

func (e *classifiedStoreError) Error() string { return e.message }
func (e *classifiedStoreError) SafeFailureCode() string {
	if e == nil {
		return ""
	}
	return e.code
}

var (
	schemaKey   = []byte{0x00, 's', 'c', 'h', 1}
	manifestKey = []byte{0x00, 'm', 'a', 'n', 1}
	identityKey = []byte{0x00, 'i', 'd', 'e', 1}
	activeKey   = []byte{groupActive, 0x01}

	observationRecordPrefix = []byte{groupRecord, kindObservation}
	observationOrderPrefix  = []byte{groupOrder, kindObservation}
	derivationRecordPrefix  = []byte{groupRecord, kindDerivation}
	generationRecordPrefix  = []byte{groupRecord, kindGeneration}
)

// OpenMode prevents an implicit create-on-open transition. CreateNew fails
// when any file already occupies the target path; OpenExisting never creates.
type OpenMode uint8

const (
	CreateNew OpenMode = iota + 1
	OpenExisting
)

// Store is an authenticated encrypted implementation of storage.EncryptedStore.
// Secret state is held behind a pointer so formatting cannot traverse key bytes.
type Store struct{ state *storeState }

type storeState struct {
	mu          sync.RWMutex
	knowledgeMu sync.RWMutex
	noteMu      sync.Mutex
	db          *bolt.DB
	codec       *recordcodec.Codec
	idKey       []byte
	manifestKey []byte
	storeID     [16]byte
	schemaVer   uint32
	closed      bool
}

var _ storage.EncryptedStore = (*Store)(nil)
var _ storage.ChronicleStore = (*Store)(nil)

// Tx groups record and future index mutations into one bbolt transaction. The
// extension writes are keyed and encrypted by the same record/manifest boundary.
type Tx struct {
	mu       sync.Mutex
	store    *Store
	writer   bolt.Writer
	manifest manifestState
	failure  error
	active   bool
	dirty    bool
}

// OpenDurable opens or creates a private encrypted store according to mode.
// It never creates or rewrites an existing store while handling open failures.
func OpenDurable(path string, key securestore.Key, mode OpenMode) (*Store, error) {
	return openDurableWithHook(path, key, mode, nil)
}

func openDurableWithHook(path string, key securestore.Key, mode OpenMode, migrationHook func() error) (*Store, error) {
	if mode != CreateNew && mode != OpenExisting {
		return nil, ErrOpenMode
	}
	if path == "" {
		return nil, ErrInvalidPath
	}
	state, err := newStoreState(key, mode == CreateNew)
	if err != nil {
		return nil, err
	}
	store := &Store{state: state}
	var database *bolt.DB
	if mode == CreateNew {
		database, err = bolt.CreateNew(path)
	} else {
		database, err = bolt.OpenExisting(path)
	}
	if err != nil {
		store.clearState()
		return nil, mapOpenError(err)
	}
	state.db = database
	if mode == CreateNew {
		err = store.initialize()
	} else {
		err = store.validateAndMigrate(context.Background(), migrationHook)
	}
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := store.ensureSearchIndex(context.Background()); err != nil {
		_ = store.Close()
		return nil, err
	}
	if mode == OpenExisting {
		if err := store.ensureCaptureTimeMetadata(context.Background()); err != nil {
			_ = store.Close()
			return nil, err
		}
	}
	return store, nil
}

func newStoreState(key securestore.Key, createNew bool) (*storeState, error) {
	dataKey := key.Bytes()
	defer clear(dataKey[:])
	if dataKey == ([32]byte{}) {
		return nil, ErrWrongKey
	}
	codec, err := recordcodec.NewCodec(dataKey[:])
	if err != nil {
		return nil, ErrWrongKey
	}
	derive := func(label string) ([]byte, error) {
		derived, err := hkdf.Key(sha256.New, dataKey[:], []byte("blur.durable.hkdf.v1"), label, sha256.Size)
		if err != nil {
			return nil, ErrWrongKey
		}
		return derived, nil
	}
	idKey, err := derive("domain-separated logical identifier key v1")
	if err != nil {
		_ = codec.Close()
		return nil, err
	}
	manifestKey, err := derive("record-set manifest key v1")
	if err != nil {
		clear(idKey)
		_ = codec.Close()
		return nil, err
	}
	state := &storeState{codec: codec, idKey: idKey, manifestKey: manifestKey}
	if createNew {
		if _, err := io.ReadFull(rand.Reader, state.storeID[:]); err != nil {
			clear(idKey)
			clear(manifestKey)
			_ = codec.Close()
			return nil, ErrUnavailable
		}
	}
	return state, nil
}

func mapOpenError(err error) error {
	switch {
	case errors.Is(err, bolt.ErrInvalid):
		return ErrInvalidPath
	case errors.Is(err, bolt.ErrExists):
		return ErrAlreadyExists
	case errors.Is(err, bolt.ErrNotFound):
		return ErrMissing
	case errors.Is(err, bolt.ErrCorrupt):
		return ErrCorrupt
	default:
		return ErrUnavailable
	}
}

func (s *Store) initialize() error {
	state := s.state
	if state == nil || state.db == nil {
		return ErrUnavailable
	}
	state.schemaVer = OpenSchemaVersion
	var callbackErr error
	err := state.db.Update(context.Background(), func(writer bolt.Writer) error {
		tx := &Tx{store: s, writer: writer, active: true}
		identityEnvelope, identityErr := s.sealIdentity(state.storeID, OpenSchemaVersion)
		if identityErr != nil {
			callbackErr = identityErr
			tx.active = false
			return errAbortTransaction
		}
		if err := tx.writeEntryLocked(identityKey, identityEnvelope, false); err != nil {
			clear(identityEnvelope)
			callbackErr = err
			tx.active = false
			return errAbortTransaction
		}
		clear(identityEnvelope)
		plain := encodeSchema(OpenSchemaVersion)
		envelope, sealErr := s.seal(schemaKey, "schema", plain)
		clear(plain)
		if sealErr != nil {
			callbackErr = sealErr
			tx.active = false
			return errAbortTransaction
		}
		if err := tx.writeEntryLocked(schemaKey, envelope, false); err != nil {
			clear(envelope)
			callbackErr = err
			tx.active = false
			return errAbortTransaction
		}
		clear(envelope)
		if err := tx.PutIndexRecord(projectionIndexNamespace, projectionIndexMarkerID, projectionIndexMarkerValue); err != nil {
			callbackErr = err
			tx.active = false
			return errAbortTransaction
		}
		captureTimeBaseline := time.Now().UTC()
		if !validCaptureTime(captureTimeBaseline) {
			callbackErr = ErrMigration
			tx.active = false
			return errAbortTransaction
		}
		if err := tx.PutIndexRecord(captureTimeMigrationNamespace, captureTimeMigrationID, encodeCaptureTimeMigration(captureTimeBaseline, true)); err != nil {
			callbackErr = err
			tx.active = false
			return errAbortTransaction
		}
		if err := tx.flushManifestLocked(); err != nil {
			callbackErr = err
			tx.active = false
			return errAbortTransaction
		}
		tx.active = false
		return nil
	})
	if callbackErr != nil {
		return callbackErr
	}
	if err != nil {
		return mapStorePhysicalError(err)
	}
	return nil
}

func (s *Store) validateAndMigrate(ctx context.Context, migrationHook func() error) error {
	if err := s.readIdentity(ctx); err != nil {
		return err
	}
	version, manifest, err := s.readOpenMetadata(ctx)
	if err != nil {
		return err
	}
	if version > OpenSchemaVersion {
		return ErrUnsupportedSchema
	}
	if version != s.state.schemaVer {
		return ErrCorrupt
	}
	if err := s.verifyManifest(ctx, manifest); err != nil {
		return err
	}
	for version < OpenSchemaVersion {
		switch version {
		case 1:
			if err := s.migrateOneToTwo(ctx, migrationHook); err != nil {
				return err
			}
			version = 2
		default:
			return ErrUnsupportedSchema
		}
	}
	if version != OpenSchemaVersion {
		return ErrUnsupportedSchema
	}
	_, manifest, err = s.readOpenMetadata(ctx)
	if err != nil {
		return err
	}
	return s.verifyManifest(ctx, manifest)
}

func (s *Store) readIdentity(ctx context.Context) error {
	var encoded []byte
	err := s.view(ctx, func(reader bolt.Reader) error {
		var found bool
		var err error
		encoded, found, err = reader.Get(identityKey)
		if err != nil {
			return mapStorePhysicalError(err)
		}
		if !found {
			return ErrCorrupt
		}
		return nil
	})
	if err != nil {
		clear(encoded)
		return err
	}
	defer clear(encoded)
	if len(encoded) < identityHeader+1+12+16 || encoded[0] != valueVersion {
		return ErrCorrupt
	}
	var storeID [16]byte
	copy(storeID[:], encoded[1:17])
	schemaVersion := binary.BigEndian.Uint32(encoded[17:identityHeader])
	plain, err := s.openEnvelopeAtContext(identityKey, "store-identity", storeID, schemaVersion, encoded[identityHeader:])
	if errors.Is(err, recordcodec.ErrAuthentication) {
		return ErrWrongKey
	}
	if err != nil {
		return ErrCorrupt
	}
	defer clear(plain)
	if len(plain) != identityPlainSize || plain[0] != valueVersion ||
		!bytes.Equal(plain[1:17], storeID[:]) || binary.BigEndian.Uint32(plain[17:]) != schemaVersion {
		return ErrCorrupt
	}
	if schemaVersion == 0 || schemaVersion > OpenSchemaVersion {
		return ErrUnsupportedSchema
	}
	s.state.storeID = storeID
	s.state.schemaVer = schemaVersion
	return nil
}

func (s *Store) readOpenMetadata(ctx context.Context) (uint32, manifestState, error) {
	var schemaVersion uint32
	var manifest manifestState
	err := s.view(ctx, func(reader bolt.Reader) error {
		schemaEnvelope, found, err := reader.Get(schemaKey)
		if err != nil {
			return mapStorePhysicalError(err)
		}
		if !found {
			return ErrCorrupt
		}
		manifestEnvelope, found, err := reader.Get(manifestKey)
		if err != nil {
			clear(schemaEnvelope)
			return mapStorePhysicalError(err)
		}
		if !found {
			clear(schemaEnvelope)
			return ErrCorrupt
		}
		schemaPlain, schemaOpenErr := s.open(schemaKey, "schema", schemaEnvelope)
		manifestPlain, manifestOpenErr := s.open(manifestKey, "manifest", manifestEnvelope)
		clear(schemaEnvelope)
		clear(manifestEnvelope)
		if schemaOpenErr != nil {
			clear(manifestPlain)
			if errors.Is(schemaOpenErr, recordcodec.ErrAuthentication) && manifestOpenErr != nil && errors.Is(manifestOpenErr, recordcodec.ErrAuthentication) {
				return ErrWrongKey
			}
			return ErrCorrupt
		}
		if manifestOpenErr != nil {
			clear(schemaPlain)
			return ErrCorrupt
		}
		schemaVersion, err = decodeSchema(schemaPlain)
		clear(schemaPlain)
		if err != nil {
			clear(manifestPlain)
			return ErrCorrupt
		}
		manifest, err = decodeManifest(manifestPlain)
		clear(manifestPlain)
		if err != nil {
			return ErrCorrupt
		}
		return nil
	})
	if err != nil {
		return 0, manifestState{}, err
	}
	return schemaVersion, manifest, nil
}

func (s *Store) verifyManifest(ctx context.Context, want manifestState) error {
	var actual manifestState
	var after []byte
	defer func() { clear(after) }()
	defer clear(want.digest[:])
	defer clear(actual.digest[:])
	for {
		var entries []bolt.Entry
		var more bool
		err := s.view(ctx, func(reader bolt.Reader) error {
			var err error
			entries, more, err = reader.Scan(nil, after, 256, bolt.MaxTransactionBytes-bolt.MaxKeyBytes)
			return mapStorePhysicalError(err)
		})
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			if more {
				return ErrCorrupt
			}
			break
		}
		for _, entry := range entries {
			if !bytes.Equal(entry.Key, manifestKey) {
				actual.addKey(entry.Key, entry.Value, s.state.manifestKey)
			}
			clear(after)
			after = bytes.Clone(entry.Key)
			clear(entry.Key)
			clear(entry.Value)
		}
		if !more {
			break
		}
	}
	if actual.count != want.count || subtle.ConstantTimeCompare(actual.digest[:], want.digest[:]) != 1 {
		return ErrCorrupt
	}
	return nil
}

func (s *Store) migrateOneToTwo(ctx context.Context, migrationHook func() error) error {
	const oldVersion = uint32(1)
	const newVersion = uint32(2)
	if s.state.schemaVer != oldVersion {
		return ErrUnsupportedSchema
	}
	var callbackErr error
	err := s.state.db.Update(ctx, func(writer bolt.Writer) error {
		tx := &Tx{store: s, writer: writer, active: true}
		if err := tx.loadManifestLocked(); err != nil {
			callbackErr = err
			tx.active = false
			return errAbortTransaction
		}
		schemaEnvelope, found, err := writer.Get(schemaKey)
		if err != nil || !found {
			callbackErr = ErrCorrupt
			tx.active = false
			return errAbortTransaction
		}
		schemaPlain, err := s.open(schemaKey, "schema", schemaEnvelope)
		clear(schemaEnvelope)
		if err != nil {
			callbackErr = ErrCorrupt
			tx.active = false
			return errAbortTransaction
		}
		version, err := decodeSchema(schemaPlain)
		clear(schemaPlain)
		if err != nil || version != oldVersion {
			callbackErr = ErrUnsupportedSchema
			tx.active = false
			return errAbortTransaction
		}
		identityEnvelope, err := s.sealIdentity(s.state.storeID, newVersion)
		if err != nil {
			callbackErr = ErrMigration
			tx.active = false
			return errAbortTransaction
		}
		if err := tx.writeEntryLocked(identityKey, identityEnvelope, true); err != nil {
			clear(identityEnvelope)
			callbackErr = ErrMigration
			tx.active = false
			return errAbortTransaction
		}
		clear(identityEnvelope)
		plain := encodeSchema(newVersion)
		envelope, err := s.sealAtSchema(schemaKey, "schema", plain, newVersion)
		clear(plain)
		if err != nil {
			callbackErr = ErrMigration
			tx.active = false
			return errAbortTransaction
		}
		if err := tx.writeEntryLocked(schemaKey, envelope, true); err != nil {
			clear(envelope)
			callbackErr = ErrMigration
			tx.active = false
			return errAbortTransaction
		}
		clear(envelope)
		// Existing value envelopes carry their authenticated schema context and
		// remain readable; new and rewritten records use the new context.
		s.state.schemaVer = newVersion
		if migrationHook != nil {
			if err := migrationHook(); err != nil {
				callbackErr = ErrMigration
				tx.active = false
				return errAbortTransaction
			}
		}
		if err := tx.flushManifestLocked(); err != nil {
			callbackErr = ErrMigration
			tx.active = false
			return errAbortTransaction
		}
		tx.active = false
		return nil
	})
	if callbackErr != nil || err != nil {
		s.state.schemaVer = oldVersion
		if errors.Is(callbackErr, ErrCorrupt) || errors.Is(callbackErr, ErrUnsupportedSchema) {
			return callbackErr
		}
		return ErrMigration
	}
	return nil
}

func (s *Store) view(ctx context.Context, callback func(bolt.Reader) error) error {
	if ctx == nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.state == nil {
		return &storage.StoreError{Code: storage.Closed}
	}
	state := s.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if state.closed || state.db == nil {
		return &storage.StoreError{Code: storage.Closed}
	}
	var callbackErr error
	err := state.db.View(ctx, func(reader bolt.Reader) error {
		callbackErr = callback(reader)
		if callbackErr != nil {
			return errAbortTransaction
		}
		return nil
	})
	if callbackErr != nil {
		return callbackErr
	}
	return mapStorePhysicalError(err)
}

// Update atomically applies store records and keyed index extensions. Task 3
// can add index mutations in the same transaction as observations.
func (s *Store) Update(ctx context.Context, callback func(*Tx) error) error {
	if ctx == nil || callback == nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.state == nil {
		return &storage.StoreError{Code: storage.Closed}
	}
	state := s.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if state.closed || state.db == nil {
		return &storage.StoreError{Code: storage.Closed}
	}
	var callbackErr error
	err := state.db.Update(ctx, func(writer bolt.Writer) error {
		tx := &Tx{store: s, writer: writer, active: true}
		tx.mu.Lock()
		if err := tx.loadManifestLocked(); err != nil {
			tx.failure = err
		}
		tx.mu.Unlock()
		if tx.failure != nil {
			callbackErr = tx.failure
			tx.finish()
			return errAbortTransaction
		}

		callbackError := callback(tx)
		tx.mu.Lock()
		if tx.failure == nil && callbackError != nil {
			tx.failure = sanitizeCallbackError(callbackError)
		}
		if tx.failure == nil {
			if err := tx.flushManifestLocked(); err != nil {
				tx.failure = err
			}
		}
		tx.active = false
		callbackErr = tx.failure
		tx.mu.Unlock()
		if callbackErr != nil {
			return errAbortTransaction
		}
		return nil
	})
	if callbackErr != nil {
		return callbackErr
	}
	return mapStorePhysicalError(err)
}

func sanitizeCallbackError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var storeErr *storage.StoreError
	if errors.As(err, &storeErr) && storeErr != nil {
		switch storeErr.Code {
		case storage.Invalid, storage.Duplicate, storage.Closed, storage.Incomplete:
			return &storage.StoreError{Code: storeErr.Code}
		default:
			return ErrTransaction
		}
	}
	switch {
	case errors.Is(err, ErrCorrupt):
		return ErrCorrupt
	case errors.Is(err, ErrWrongKey):
		return ErrWrongKey
	case errors.Is(err, ErrUnsupportedSchema):
		return ErrUnsupportedSchema
	case errors.Is(err, ErrMigration):
		return ErrMigration
	case errors.Is(err, ErrProjectionIndexUnavailable):
		return ErrProjectionIndexUnavailable
	case errors.Is(err, ErrInvalidRetentionPolicy):
		return ErrInvalidRetentionPolicy
	case errors.Is(err, ErrTransaction):
		return ErrTransaction
	}
	return ErrTransaction
}

func mapStorePhysicalError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, bolt.ErrClosed):
		return &storage.StoreError{Code: storage.Closed}
	case errors.Is(err, bolt.ErrInvalid), errors.Is(err, bolt.ErrInvalidTx):
		return &storage.StoreError{Code: storage.Invalid}
	case errors.Is(err, bolt.ErrCorrupt):
		return ErrCorrupt
	default:
		return ErrTransaction
	}
}

func (s *Store) EncryptedAtRest() bool { return true }

func (s *Store) Close() error {
	if s == nil || s.state == nil {
		return nil
	}
	state := s.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil
	}
	state.closed = true
	database := state.db
	state.db = nil
	var closeErr error
	if database != nil {
		closeErr = database.Close()
	}
	if state.codec != nil {
		_ = state.codec.Close()
		state.codec = nil
	}
	clear(state.idKey)
	clear(state.manifestKey)
	state.idKey, state.manifestKey = nil, nil
	clear(state.storeID[:])
	state.schemaVer = 0
	if closeErr != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *Store) clearState() {
	if s == nil || s.state == nil {
		return
	}
	state := s.state
	if state.codec != nil {
		_ = state.codec.Close()
		state.codec = nil
	}
	clear(state.idKey)
	clear(state.manifestKey)
	state.idKey, state.manifestKey = nil, nil
	clear(state.storeID[:])
	state.schemaVer = 0
}

const redactedStore = "durable.Store([redacted])"

func (Store) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, redactedStore) }
func (Store) String() string                 { return redactedStore }
func (Store) GoString() string               { return redactedStore }
func (Store) MarshalJSON() ([]byte, error)   { return nil, recordcodec.ErrSerialization }
func (Store) MarshalText() ([]byte, error)   { return nil, recordcodec.ErrSerialization }

const redactedTx = "durable.Tx([redacted])"

func (*Tx) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, redactedTx) }
func (*Tx) String() string                 { return redactedTx }
func (*Tx) GoString() string               { return redactedTx }

type manifestState struct {
	count  uint64
	digest [sha256.Size]byte
}

func (m *manifestState) addKey(key []byte, value []byte, manifestKey []byte) {
	leaf := manifestLeaf(manifestKey, key, value)
	for index := range m.digest {
		m.digest[index] ^= leaf[index]
	}
	m.count++
	clear(leaf[:])
}

func (m *manifestState) removeKey(key []byte, value []byte, manifestKey []byte) error {
	if m.count == 0 {
		return ErrCorrupt
	}
	leaf := manifestLeaf(manifestKey, key, value)
	for index := range m.digest {
		m.digest[index] ^= leaf[index]
	}
	m.count--
	clear(leaf[:])
	return nil
}

func manifestLeaf(key []byte, physicalKey, value []byte) [sha256.Size]byte {
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, "blur.durable.manifest.leaf.v1\x00")
	writeLength(mac, uint64(len(physicalKey)))
	_, _ = mac.Write(physicalKey)
	writeLength(mac, uint64(len(value)))
	_, _ = mac.Write(value)
	var leaf [sha256.Size]byte
	sum := mac.Sum(nil)
	copy(leaf[:], sum)
	clear(sum)
	return leaf
}

func writeLength(writer io.Writer, length uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], length)
	_, _ = writer.Write(encoded[:])
}

func clearBoltEntries(entries []bolt.Entry) {
	for index := range entries {
		clear(entries[index].Key)
		clear(entries[index].Value)
	}
}

func encodeSchema(version uint32) []byte {
	encoded := make([]byte, 5)
	encoded[0] = valueVersion
	binary.BigEndian.PutUint32(encoded[1:], version)
	return encoded
}

func decodeSchema(encoded []byte) (uint32, error) {
	if len(encoded) != 5 || encoded[0] != valueVersion {
		return 0, ErrCorrupt
	}
	return binary.BigEndian.Uint32(encoded[1:]), nil
}

func encodeManifest(manifest manifestState) []byte {
	encoded := make([]byte, 1+8+sha256.Size)
	encoded[0] = valueVersion
	binary.BigEndian.PutUint64(encoded[1:9], manifest.count)
	copy(encoded[9:], manifest.digest[:])
	return encoded
}

func decodeManifest(encoded []byte) (manifestState, error) {
	var manifest manifestState
	if len(encoded) != 1+8+sha256.Size || encoded[0] != valueVersion {
		return manifest, ErrCorrupt
	}
	manifest.count = binary.BigEndian.Uint64(encoded[1:9])
	copy(manifest.digest[:], encoded[9:])
	return manifest, nil
}

func (tx *Tx) loadManifestLocked() error {
	envelope, found, err := tx.writer.Get(manifestKey)
	if err != nil || !found {
		return ErrCorrupt
	}
	plain, err := tx.store.open(manifestKey, "manifest", envelope)
	clear(envelope)
	if err != nil {
		return ErrCorrupt
	}
	manifest, err := decodeManifest(plain)
	clear(plain)
	if err != nil {
		return ErrCorrupt
	}
	tx.manifest = manifest
	return nil
}

func (tx *Tx) flushManifestLocked() error {
	if !tx.dirty {
		return nil
	}
	plain := encodeManifest(tx.manifest)
	envelope, err := tx.store.seal(manifestKey, "manifest", plain)
	clear(plain)
	if err != nil {
		return err
	}
	err = tx.writer.Put(manifestKey, envelope)
	clear(envelope)
	if err != nil {
		return mapStorePhysicalError(err)
	}
	tx.dirty = false
	return nil
}

func (tx *Tx) writeEntryLocked(key, value []byte, replace bool) error {
	var old []byte
	var found bool
	var err error
	if replace {
		old, found, err = tx.writer.Get(key)
	} else {
		found, err = tx.writer.Has(key)
	}
	if err != nil {
		return mapStorePhysicalError(err)
	}
	if found && !replace {
		return &storage.StoreError{Code: storage.Duplicate}
	}
	if found {
		if err := tx.manifest.removeKey(key, old, tx.store.state.manifestKey); err != nil {
			clear(old)
			return err
		}
		clear(old)
	}
	if err := tx.writer.Put(key, value); err != nil {
		return mapStorePhysicalError(err)
	}
	tx.manifest.addKey(key, value, tx.store.state.manifestKey)
	tx.dirty = true
	return nil
}

func (s *Store) seal(key []byte, namespace string, plaintext []byte) ([]byte, error) {
	if s == nil || s.state == nil || s.state.codec == nil {
		return nil, &storage.StoreError{Code: storage.Closed}
	}
	return s.sealAtSchema(key, namespace, plaintext, s.state.schemaVer)
}

func (s *Store) sealAtSchema(key []byte, namespace string, plaintext []byte, schemaVersion uint32) ([]byte, error) {
	if s == nil || s.state == nil || s.state.codec == nil {
		return nil, &storage.StoreError{Code: storage.Closed}
	}
	if schemaVersion == 0 || s.state.storeID == ([16]byte{}) {
		return nil, ErrUnsupportedSchema
	}
	envelope, err := s.state.codec.Seal(recordcodec.RecordMetadata{
		StoreID:       hex.EncodeToString(s.state.storeID[:]),
		SchemaVersion: schemaVersion,
		Namespace:     namespace,
		Key:           hex.EncodeToString(key),
		Revision:      recordRevision,
	}, plaintext)
	if errors.Is(err, recordcodec.ErrOversize) {
		return nil, &storage.StoreError{Code: storage.Invalid}
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	encoded := make([]byte, storedValueHeader+len(envelope))
	encoded[0] = valueVersion
	binary.BigEndian.PutUint32(encoded[1:storedValueHeader], schemaVersion)
	copy(encoded[storedValueHeader:], envelope)
	clear(envelope)
	return encoded, nil
}

func (s *Store) open(key []byte, namespace string, envelope []byte) ([]byte, error) {
	if s == nil || s.state == nil || s.state.codec == nil {
		return nil, &storage.StoreError{Code: storage.Closed}
	}
	if len(envelope) < storedValueHeader+1+12+16 || envelope[0] != valueVersion {
		return nil, recordcodec.ErrInvalidEnvelope
	}
	schemaVersion := binary.BigEndian.Uint32(envelope[1:storedValueHeader])
	if schemaVersion == 0 || schemaVersion > s.state.schemaVer {
		return nil, ErrUnsupportedSchema
	}
	return s.openEnvelopeAtContext(key, namespace, s.state.storeID, schemaVersion, envelope[storedValueHeader:])
}

func (s *Store) openEnvelopeAtContext(key []byte, namespace string, storeID [16]byte, schemaVersion uint32, envelope []byte) ([]byte, error) {
	if s == nil || s.state == nil || s.state.codec == nil {
		return nil, &storage.StoreError{Code: storage.Closed}
	}
	plaintext, err := s.state.codec.Open(recordcodec.RecordMetadata{
		StoreID:       hex.EncodeToString(storeID[:]),
		SchemaVersion: schemaVersion,
		Namespace:     namespace,
		Key:           hex.EncodeToString(key),
		Revision:      recordRevision,
	}, envelope)
	if err != nil {
		return nil, err
	}
	return plaintext, nil
}

func (s *Store) sealIdentity(storeID [16]byte, schemaVersion uint32) ([]byte, error) {
	if s == nil || s.state == nil || s.state.codec == nil || storeID == ([16]byte{}) || schemaVersion == 0 {
		return nil, ErrUnsupportedSchema
	}
	plain := make([]byte, identityPlainSize)
	plain[0] = valueVersion
	copy(plain[1:17], storeID[:])
	binary.BigEndian.PutUint32(plain[17:], schemaVersion)
	defer clear(plain)
	envelope, err := s.state.codec.Seal(recordcodec.RecordMetadata{
		StoreID:       hex.EncodeToString(storeID[:]),
		SchemaVersion: schemaVersion,
		Namespace:     "store-identity",
		Key:           hex.EncodeToString(identityKey),
		Revision:      recordRevision,
	}, plain)
	if err != nil {
		return nil, ErrUnavailable
	}
	encoded := make([]byte, identityHeader+len(envelope))
	encoded[0] = valueVersion
	copy(encoded[1:17], storeID[:])
	binary.BigEndian.PutUint32(encoded[17:identityHeader], schemaVersion)
	copy(encoded[identityHeader:], envelope)
	clear(envelope)
	return encoded, nil
}

func (s *Store) logicalKey(group, kind byte, namespace, logicalID string) []byte {
	mac := hmac.New(sha256.New, s.state.idKey)
	_, _ = io.WriteString(mac, "blur.durable.logical-id.v1\x00")
	_, _ = mac.Write([]byte{group, kind})
	writeMACString(mac, namespace)
	writeMACString(mac, logicalID)
	sum := mac.Sum(nil)
	key := make([]byte, 2+len(sum))
	key[0], key[1] = group, kind
	copy(key[2:], sum)
	clear(sum)
	return key
}

func (s *Store) cursorKey(group, kind byte, namespace, logicalID string) ([]byte, error) {
	if s == nil || s.state == nil {
		return nil, &storage.StoreError{Code: storage.Closed}
	}
	state := s.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if state.closed || state.db == nil || len(state.idKey) == 0 {
		return nil, &storage.StoreError{Code: storage.Closed}
	}
	return s.logicalKey(group, kind, namespace, logicalID), nil
}

func writeMACString(mac hash.Hash, value string) {
	writeLength(mac, uint64(len(value)))
	encoded := []byte(value)
	_, _ = mac.Write(encoded)
	clear(encoded)
}

func (tx *Tx) checkLocked() error {
	if tx == nil || !tx.active {
		return &storage.StoreError{Code: storage.Closed}
	}
	if tx.failure != nil {
		return tx.failure
	}
	return nil
}

func (tx *Tx) failLocked(err error) error {
	err = sanitizeCallbackError(err)
	if tx.failure == nil {
		tx.failure = err
	}
	return tx.failure
}

func (tx *Tx) finish() {
	if tx == nil {
		return
	}
	tx.mu.Lock()
	tx.active = false
	tx.mu.Unlock()
}

func (tx *Tx) PutObservation(observation capture.SanitizedObservation) error {
	return tx.PutObservationAt(observation, time.Now())
}

func (tx *Tx) PutObservationAt(observation capture.SanitizedObservation, capturedAt time.Time) error {
	if tx == nil {
		return &storage.StoreError{Code: storage.Closed}
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.checkLocked(); err != nil {
		return tx.failLocked(err)
	}
	if observation.Validate() != nil || capturedAt.IsZero() || !validCaptureTime(capturedAt) {
		return tx.failLocked(&storage.StoreError{Code: storage.Invalid})
	}
	plaintext, err := capture.MarshalAuthenticatedObservation(observation)
	if err != nil {
		return tx.failLocked(&storage.StoreError{Code: storage.Invalid})
	}
	defer clear(plaintext)
	logicalID := observation.ContentID()
	searchBytes := len(observation.Text()) + len(observation.AppID()) + len(observation.Domain())
	recordKey := tx.store.logicalKey(groupRecord, kindObservation, "", logicalID)
	orderKey := tx.store.logicalKey(groupOrder, kindObservation, "", logicalID)
	envelope, err := tx.store.seal(recordKey, "observations", plaintext)
	if err != nil {
		clear(recordKey)
		clear(orderKey)
		return tx.failLocked(err)
	}
	if err := tx.writeEntryLocked(recordKey, envelope, false); err != nil {
		clear(envelope)
		clear(recordKey)
		clear(orderKey)
		return tx.failLocked(err)
	}
	clear(envelope)
	sizeValue, err := tx.store.encodeSizeEntry(orderKey, recordKey, searchBytes, capturedAt)
	if err != nil {
		clear(recordKey)
		clear(orderKey)
		return tx.failLocked(err)
	}
	if err := tx.writeEntryLocked(orderKey, sizeValue, false); err != nil {
		clear(sizeValue)
		clear(recordKey)
		clear(orderKey)
		return tx.failLocked(err)
	}
	clear(sizeValue)
	if err := tx.indexObservationLocked(observation); err != nil {
		clear(recordKey)
		clear(orderKey)
		return tx.failLocked(err)
	}
	if err := tx.indexCaptureHourLocked(logicalID, capturedAt); err != nil {
		clear(recordKey)
		clear(orderKey)
		return tx.failLocked(err)
	}
	clear(recordKey)
	clear(orderKey)
	return nil
}

func (tx *Tx) PutDerivation(derivation storage.Derivation) error {
	if tx == nil {
		return &storage.StoreError{Code: storage.Closed}
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.checkLocked(); err != nil {
		return tx.failLocked(err)
	}
	if !validDerivation(derivation) {
		return tx.failLocked(&storage.StoreError{Code: storage.Invalid})
	}
	plaintext, err := json.Marshal(derivationRecord{Version: valueVersion, Record: derivation})
	if err != nil {
		return tx.failLocked(&storage.StoreError{Code: storage.Invalid})
	}
	defer clear(plaintext)
	if err := tx.putRecordLocked(kindDerivation, "derivations", derivation.ID, plaintext, false); err != nil {
		return tx.failLocked(err)
	}
	return nil
}

func (tx *Tx) PutGeneration(generation storage.Generation) error {
	if tx == nil {
		return &storage.StoreError{Code: storage.Closed}
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.checkLocked(); err != nil {
		return tx.failLocked(err)
	}
	if !validGeneration(generation) {
		return tx.failLocked(&storage.StoreError{Code: storage.Invalid})
	}
	plaintext, err := json.Marshal(generationRecord{Version: valueVersion, Record: generation})
	if err != nil {
		return tx.failLocked(&storage.StoreError{Code: storage.Invalid})
	}
	defer clear(plaintext)
	if err := tx.putRecordLocked(kindGeneration, "generations", generation.ID, plaintext, false); err != nil {
		return tx.failLocked(err)
	}
	return nil
}

// PutIndexRecord stores a bounded encrypted extension value in the same
// transaction as observations. The logical identifier is HMAC-keyed on disk.
func (tx *Tx) PutIndexRecord(namespace, logicalID string, value []byte) error {
	if tx == nil {
		return &storage.StoreError{Code: storage.Closed}
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.checkLocked(); err != nil {
		return tx.failLocked(err)
	}
	if !validNamespace(namespace) || !validOpaqueID(logicalID) || len(value) > recordcodec.MaxPlaintextBytes-5 {
		return tx.failLocked(&storage.StoreError{Code: storage.Invalid})
	}
	plaintext := make([]byte, 5+len(value))
	plaintext[0] = valueVersion
	binary.BigEndian.PutUint32(plaintext[1:5], uint32(len(value)))
	copy(plaintext[5:], value)
	defer clear(plaintext)
	if err := tx.putRecordLocked(kindIndex, "index:"+namespace, logicalID, plaintext, true); err != nil {
		return tx.failLocked(err)
	}
	return nil
}

func (tx *Tx) putRecordLocked(kind byte, namespace, logicalID string, plaintext []byte, replace bool) error {
	recordKey := tx.store.logicalKey(groupRecord, kind, namespace, logicalID)
	defer clear(recordKey)
	envelope, err := tx.store.seal(recordKey, namespace, plaintext)
	if err != nil {
		return err
	}
	defer clear(envelope)
	return tx.writeEntryLocked(recordKey, envelope, replace)
}

func validNamespace(namespace string) bool {
	if namespace == "" || len(namespace) > 32 {
		return false
	}
	for i := range namespace {
		c := namespace[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func validOpaqueID(id string) bool {
	return id != "" && len(id) <= maxLogicalIDBytes && utf8.ValidString(id) && !strings.ContainsRune(id, 0)
}

func (s *Store) encodeSizeEntry(orderKey, recordKey []byte, size int, capturedAt time.Time) ([]byte, error) {
	if len(recordKey) != physicalKeyBytes || size < 0 || size > storage.MaxObservationPageBytes {
		return nil, ErrCorrupt
	}
	if capturedAt.IsZero() || !validCaptureTime(capturedAt) {
		return nil, &storage.StoreError{Code: storage.Invalid}
	}
	plain := make([]byte, sizeRecordBytes)
	plain[0] = 2
	binary.BigEndian.PutUint32(plain[1:5], uint32(size))
	binary.BigEndian.PutUint64(plain[5:13], uint64(capturedAt.UTC().UnixNano()))
	copy(plain[13:], recordKey)
	envelope, err := s.seal(orderKey, "observation-size", plain)
	clear(plain)
	if err != nil {
		return nil, err
	}
	return envelope, nil
}

func (s *Store) decodeSizeEntry(orderKey, encoded []byte) (int, time.Time, []byte, error) {
	plain, err := s.open(orderKey, "observation-size", encoded)
	if err != nil {
		return 0, time.Time{}, nil, ErrCorrupt
	}
	defer clear(plain)
	if len(plain) == legacySizeRecordBytes && plain[0] == 1 {
		recordKey := bytes.Clone(plain[5:])
		if !bytes.HasPrefix(recordKey, observationRecordPrefix) || binary.BigEndian.Uint32(plain[1:5]) > uint32(storage.MaxObservationTextBytes+128+253) {
			clear(recordKey)
			return 0, time.Time{}, nil, ErrCorrupt
		}
		return int(binary.BigEndian.Uint32(plain[1:5])), time.Time{}, recordKey, nil
	}
	if len(plain) != sizeRecordBytes || plain[0] != 2 {
		return 0, time.Time{}, nil, ErrCorrupt
	}
	capturedAt := time.Unix(0, int64(binary.BigEndian.Uint64(plain[5:13]))).UTC()
	recordKey := bytes.Clone(plain[13:])
	if !bytes.HasPrefix(recordKey, observationRecordPrefix) {
		clear(recordKey)
		return 0, time.Time{}, nil, ErrCorrupt
	}
	size := binary.BigEndian.Uint32(plain[1:5])
	maxSize := uint32(storage.MaxObservationTextBytes + 128 + 253)
	if size > maxSize {
		clear(recordKey)
		return 0, time.Time{}, nil, ErrCorrupt
	}
	if !validCaptureTime(capturedAt) {
		clear(recordKey)
		return 0, time.Time{}, nil, ErrCorrupt
	}
	return int(size), capturedAt, recordKey, nil
}

func validCaptureTime(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	return time.Unix(0, value.UTC().UnixNano()).UTC().Equal(value.UTC())
}

// ValidCapturedAt reports whether a timestamp can be represented losslessly in
// the authenticated nanosecond metadata format.
func ValidCapturedAt(value time.Time) bool { return validCaptureTime(value) }

func (s *Store) PutObservation(ctx context.Context, observation capture.SanitizedObservation) error {
	return s.PutObservationAt(ctx, observation, time.Now())
}

func (s *Store) PutObservationAt(ctx context.Context, observation capture.SanitizedObservation, capturedAt time.Time) error {
	if capturedAt.IsZero() || !validCaptureTime(capturedAt) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	return s.Update(ctx, func(tx *Tx) error { return tx.PutObservationAt(observation, capturedAt) })
}

// DeleteObservationRecord atomically removes a source and its exact-search
// postings. Separate materialized projections must be invalidated by callers.
func (s *Store) DeleteObservationRecord(ctx context.Context, logicalID string) error {
	if ctx == nil || !validHash(logicalID) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	return s.Update(ctx, func(tx *Tx) error { return tx.deleteObservationRecord(logicalID) })
}

// MaxObservationDeleteBatch bounds deletion work per atomic transaction. It is
// deliberately far below the physical transaction operation cap because each
// source can touch several authenticated projection indexes.
const MaxObservationDeleteBatch = 8

// DeleteObservationRecords atomically removes a small batch of sources. Every
// ID is validated before opening the transaction, so malformed input cannot
// cause a partial delete. Callers can safely retry a failed batch.
func (s *Store) DeleteObservationRecords(ctx context.Context, logicalIDs []string) error {
	if ctx == nil || len(logicalIDs) == 0 || len(logicalIDs) > MaxObservationDeleteBatch {
		return &storage.StoreError{Code: storage.Invalid}
	}
	seen := make(map[string]struct{}, len(logicalIDs))
	for _, id := range logicalIDs {
		if !validHash(id) {
			return &storage.StoreError{Code: storage.Invalid}
		}
		if _, exists := seen[id]; exists {
			return &storage.StoreError{Code: storage.Invalid}
		}
		seen[id] = struct{}{}
	}
	return s.Update(ctx, func(tx *Tx) error {
		operationBudget := 2 // manifest read/write and shared transaction overhead
		for _, id := range logicalIDs {
			observation, found, err := s.getObservationReader(tx.writer, id)
			if err != nil {
				return err
			}
			if found {
				cost, err := estimatedSearchIndexOperations(observation)
				if err != nil {
					return err
				}
				operationBudget += cost
			}
		}
		if operationBudget > bolt.MaxTransactionOps-64 {
			return ErrTransaction
		}
		for _, id := range logicalIDs {
			if err := tx.deleteObservationRecord(id); err != nil {
				return err
			}
		}
		return nil
	})
}

// MaxObservationDeleteBatchWithinSearchBudget returns the largest prefix that
// fits the estimated search-index transaction budget with headroom. Other
// source-linked projections still run inside the transaction; the physical
// writer remains authoritative and aborts atomically if its hard cap is hit.
func (s *Store) MaxObservationDeleteBatchWithinSearchBudget(observations []capture.SanitizedObservation) (int, error) {
	if s == nil || len(observations) == 0 {
		return 0, &storage.StoreError{Code: storage.Invalid}
	}
	limit := len(observations)
	if limit > MaxObservationDeleteBatch {
		limit = MaxObservationDeleteBatch
	}
	operations := 2
	for i := 0; i < limit; i++ {
		cost, err := estimatedSearchIndexOperations(observations[i])
		if err != nil {
			return 0, err
		}
		if operations+cost > bolt.MaxTransactionOps-64 {
			if i == 0 {
				return 0, ErrTransaction
			}
			return i, nil
		}
		operations += cost
	}
	return limit, nil
}

func (s *Store) GetObservation(ctx context.Context, logicalID string) (capture.SanitizedObservation, bool, error) {
	if !validHash(logicalID) {
		return capture.SanitizedObservation{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	var observation capture.SanitizedObservation
	found := false
	err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		observation, found, err = s.getObservationReader(reader, logicalID)
		return err
	})
	if err != nil {
		return capture.SanitizedObservation{}, false, err
	}
	return observation, found, nil
}

func (s *Store) getObservationReader(reader bolt.Reader, logicalID string) (capture.SanitizedObservation, bool, error) {
	recordKey := s.logicalKey(groupRecord, kindObservation, "", logicalID)
	defer clear(recordKey)
	orderKey := s.logicalKey(groupOrder, kindObservation, "", logicalID)
	defer clear(orderKey)
	envelope, found, err := reader.Get(recordKey)
	if err != nil {
		return capture.SanitizedObservation{}, false, mapStorePhysicalError(err)
	}
	if !found {
		indexed, err := reader.Has(orderKey)
		if err != nil {
			return capture.SanitizedObservation{}, false, mapStorePhysicalError(err)
		}
		if indexed {
			return capture.SanitizedObservation{}, false, ErrCorrupt
		}
		return capture.SanitizedObservation{}, false, nil
	}
	plain, err := s.open(recordKey, "observations", envelope)
	clear(envelope)
	if err != nil {
		return capture.SanitizedObservation{}, false, ErrCorrupt
	}
	defer clear(plain)
	observation, err := capture.RestoreAuthenticatedObservation(plain)
	if err != nil || observation.ContentID() != logicalID {
		return capture.SanitizedObservation{}, false, ErrCorrupt
	}
	indexValue, indexed, err := reader.Get(orderKey)
	if err != nil || !indexed {
		clear(indexValue)
		return capture.SanitizedObservation{}, false, ErrCorrupt
	}
	pageBytes, _, indexedRecordKey, err := s.decodeSizeEntry(orderKey, indexValue)
	clear(indexValue)
	if err != nil {
		return capture.SanitizedObservation{}, false, ErrCorrupt
	}
	defer clear(indexedRecordKey)
	if !bytes.Equal(indexedRecordKey, recordKey) || pageBytes != observationSearchBytes(observation) {
		return capture.SanitizedObservation{}, false, ErrCorrupt
	}
	return observation, true, nil
}

func observationSearchBytes(observation capture.SanitizedObservation) int {
	return len(observation.Text()) + len(observation.AppID()) + len(observation.Domain())
}

func (s *Store) ListObservationPage(ctx context.Context, request storage.ObservationPageRequest) (storage.ObservationPage, error) {
	if !validObservationPage(request) {
		return storage.ObservationPage{}, &storage.StoreError{Code: storage.Invalid}
	}
	var after []byte
	if request.After != "" {
		var err error
		after, err = s.cursorKey(groupOrder, kindObservation, "", request.After)
		if err != nil {
			return storage.ObservationPage{}, err
		}
		defer clear(after)
	}
	page := storage.ObservationPage{Observations: make([]capture.SanitizedObservation, 0, request.Limit), CapturedAt: make([]time.Time, 0, request.Limit)}
	err := s.view(ctx, func(reader bolt.Reader) error {
		entries, hasMore, err := reader.Scan(observationOrderPrefix, after, request.Limit, bolt.MaxTransactionBytes-bolt.MaxKeyBytes)
		if err != nil {
			return mapStorePhysicalError(err)
		}
		defer clearBoltEntries(entries)
		page.HasMore = hasMore
		for _, entry := range entries {
			size, capturedAt, recordKey, err := s.decodeSizeEntry(entry.Key, entry.Value)
			if err != nil {
				return ErrCorrupt
			}
			if size > request.MaxBytes-page.Bytes {
				page.HasMore = true
				clear(recordKey)
				break
			}
			envelope, found, err := reader.Get(recordKey)
			if err != nil || !found {
				clear(recordKey)
				return ErrCorrupt
			}
			plain, err := s.open(recordKey, "observations", envelope)
			clear(envelope)
			if err != nil {
				clear(recordKey)
				return ErrCorrupt
			}
			observation, err := capture.RestoreAuthenticatedObservation(plain)
			clear(plain)
			if err != nil {
				clear(recordKey)
				return ErrCorrupt
			}
			expectedRecordKey := s.logicalKey(groupRecord, kindObservation, "", observation.ContentID())
			matchesRecordKey := bytes.Equal(recordKey, expectedRecordKey)
			clear(expectedRecordKey)
			if !matchesRecordKey || size != observationSearchBytes(observation) {
				clear(recordKey)
				return ErrCorrupt
			}
			clear(recordKey)
			page.Observations = append(page.Observations, observation)
			page.CapturedAt = append(page.CapturedAt, capturedAt)
			page.Bytes += size
		}
		return nil
	})
	if err != nil {
		return storage.ObservationPage{}, err
	}
	return page, nil
}

func validObservationPage(request storage.ObservationPageRequest) bool {
	return request.Limit > 0 && request.Limit <= storage.MaxObservationPageRows &&
		request.MaxBytes > 0 && request.MaxBytes <= storage.MaxObservationPageBytes &&
		(request.After == "" || validHash(request.After))
}

func (s *Store) PutDerivation(ctx context.Context, derivation storage.Derivation) error {
	return s.Update(ctx, func(tx *Tx) error { return tx.PutDerivation(derivation) })
}

func (s *Store) GetDerivation(ctx context.Context, logicalID string) (storage.Derivation, bool, error) {
	if !validID(logicalID) {
		return storage.Derivation{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	var derivation storage.Derivation
	found := false
	err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		derivation, found, err = s.getDerivationReader(reader, logicalID)
		return err
	})
	if err != nil {
		return storage.Derivation{}, false, err
	}
	return derivation, found, nil
}

func (s *Store) getDerivationReader(reader bolt.Reader, logicalID string) (storage.Derivation, bool, error) {
	key := s.logicalKey(groupRecord, kindDerivation, "derivations", logicalID)
	defer clear(key)
	envelope, found, err := reader.Get(key)
	if err != nil || !found {
		if err != nil {
			return storage.Derivation{}, false, mapStorePhysicalError(err)
		}
		return storage.Derivation{}, false, nil
	}
	plain, err := s.open(key, "derivations", envelope)
	clear(envelope)
	if err != nil {
		return storage.Derivation{}, false, ErrCorrupt
	}
	defer clear(plain)
	derivation, err := decodeDerivation(plain)
	if err != nil || derivation.ID != logicalID {
		return storage.Derivation{}, false, ErrCorrupt
	}
	return derivation, true, nil
}

func (s *Store) ListDerivations(ctx context.Context, limit int, after string) ([]storage.Derivation, error) {
	if !validPage(limit, after, false) {
		return nil, &storage.StoreError{Code: storage.Invalid}
	}
	var cursor []byte
	if after != "" {
		var err error
		cursor, err = s.cursorKey(groupRecord, kindDerivation, "derivations", after)
		if err != nil {
			return nil, err
		}
		defer clear(cursor)
	}
	out := make([]storage.Derivation, 0, limit)
	err := s.view(ctx, func(reader bolt.Reader) error {
		entries, _, err := reader.Scan(derivationRecordPrefix, cursor, limit, bolt.MaxTransactionBytes-bolt.MaxKeyBytes)
		if err != nil {
			return mapStorePhysicalError(err)
		}
		defer clearBoltEntries(entries)
		for _, entry := range entries {
			plain, err := s.open(entry.Key, "derivations", entry.Value)
			if err != nil {
				return ErrCorrupt
			}
			derivation, err := decodeDerivation(plain)
			clear(plain)
			if err != nil {
				return ErrCorrupt
			}
			expectedKey := s.logicalKey(groupRecord, kindDerivation, "derivations", derivation.ID)
			matchesKey := bytes.Equal(entry.Key, expectedKey)
			clear(expectedKey)
			if !matchesKey {
				return ErrCorrupt
			}
			out = append(out, derivation)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) PutGeneration(ctx context.Context, generation storage.Generation) error {
	return s.Update(ctx, func(tx *Tx) error { return tx.PutGeneration(generation) })
}

func (s *Store) GetGeneration(ctx context.Context, logicalID string) (storage.Generation, bool, error) {
	if !validID(logicalID) {
		return storage.Generation{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	var generation storage.Generation
	found := false
	err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		generation, found, err = s.getGenerationReader(reader, logicalID)
		return err
	})
	if err != nil {
		return storage.Generation{}, false, err
	}
	return generation, found, nil
}

func (s *Store) getGenerationReader(reader bolt.Reader, logicalID string) (storage.Generation, bool, error) {
	key := s.logicalKey(groupRecord, kindGeneration, "generations", logicalID)
	defer clear(key)
	envelope, found, err := reader.Get(key)
	if err != nil || !found {
		if err != nil {
			return storage.Generation{}, false, mapStorePhysicalError(err)
		}
		return storage.Generation{}, false, nil
	}
	plain, err := s.open(key, "generations", envelope)
	clear(envelope)
	if err != nil {
		return storage.Generation{}, false, ErrCorrupt
	}
	defer clear(plain)
	generation, err := decodeGeneration(plain)
	if err != nil || generation.ID != logicalID {
		return storage.Generation{}, false, ErrCorrupt
	}
	return generation, true, nil
}

func (s *Store) ListGenerations(ctx context.Context, limit int, after string) ([]storage.Generation, error) {
	if !validPage(limit, after, false) {
		return nil, &storage.StoreError{Code: storage.Invalid}
	}
	var cursor []byte
	if after != "" {
		var err error
		cursor, err = s.cursorKey(groupRecord, kindGeneration, "generations", after)
		if err != nil {
			return nil, err
		}
		defer clear(cursor)
	}
	out := make([]storage.Generation, 0, limit)
	err := s.view(ctx, func(reader bolt.Reader) error {
		entries, _, err := reader.Scan(generationRecordPrefix, cursor, limit, bolt.MaxTransactionBytes-bolt.MaxKeyBytes)
		if err != nil {
			return mapStorePhysicalError(err)
		}
		defer clearBoltEntries(entries)
		for _, entry := range entries {
			plain, err := s.open(entry.Key, "generations", entry.Value)
			if err != nil {
				return ErrCorrupt
			}
			generation, err := decodeGeneration(plain)
			clear(plain)
			if err != nil {
				return ErrCorrupt
			}
			expectedKey := s.logicalKey(groupRecord, kindGeneration, "generations", generation.ID)
			matchesKey := bytes.Equal(entry.Key, expectedKey)
			clear(expectedKey)
			if !matchesKey {
				return ErrCorrupt
			}
			out = append(out, generation)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ActivateGeneration(ctx context.Context, logicalID string) error {
	if !validID(logicalID) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	generation, found, err := s.GetGeneration(ctx, logicalID)
	if err != nil {
		return err
	}
	if !found {
		return &storage.StoreError{Code: storage.Incomplete}
	}
	for _, derivationID := range generation.DerivationIDs {
		complete := false
		err := s.view(ctx, func(reader bolt.Reader) error {
			derivation, found, err := s.getDerivationReader(reader, derivationID)
			if err != nil {
				return err
			}
			if !found || derivation.GenerationID != logicalID || derivation.State != storage.Active || derivation.CoveredSources+derivation.OmittedSources != len(derivation.SourceIDs) {
				return &storage.StoreError{Code: storage.Incomplete}
			}
			for _, sourceID := range derivation.SourceIDs {
				key := s.logicalKey(groupRecord, kindObservation, "", sourceID)
				present, err := reader.Has(key)
				clear(key)
				if err != nil {
					return mapStorePhysicalError(err)
				}
				if !present {
					return &storage.StoreError{Code: storage.Incomplete}
				}
			}
			complete = true
			return nil
		})
		if err != nil {
			return err
		}
		if !complete {
			return &storage.StoreError{Code: storage.Incomplete}
		}
	}
	return s.Update(ctx, func(tx *Tx) error { return tx.putActive(logicalID) })
}

func (tx *Tx) putActive(logicalID string) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.checkLocked(); err != nil {
		return tx.failLocked(err)
	}
	plain, err := json.Marshal(activeRecord{Version: valueVersion, ID: logicalID})
	if err != nil {
		return tx.failLocked(ErrTransaction)
	}
	defer clear(plain)
	envelope, err := tx.store.seal(activeKey, "active", plain)
	if err != nil {
		return tx.failLocked(err)
	}
	defer clear(envelope)
	if err := tx.writeEntryLocked(activeKey, envelope, true); err != nil {
		return tx.failLocked(err)
	}
	return nil
}

func (s *Store) ActiveGeneration(ctx context.Context) (storage.Generation, bool, error) {
	var generation storage.Generation
	found := false
	err := s.view(ctx, func(reader bolt.Reader) error {
		envelope, present, err := reader.Get(activeKey)
		if err != nil || !present {
			if err != nil {
				return mapStorePhysicalError(err)
			}
			return nil
		}
		plain, err := s.open(activeKey, "active", envelope)
		clear(envelope)
		if err != nil {
			return ErrCorrupt
		}
		active, err := decodeActive(plain)
		clear(plain)
		if err != nil {
			return ErrCorrupt
		}
		generation, found, err = s.getGenerationReader(reader, active.ID)
		if err != nil || !found {
			return ErrCorrupt
		}
		return nil
	})
	if err != nil {
		return storage.Generation{}, false, err
	}
	return generation, found, nil
}

type derivationRecord struct {
	Version int                `json:"version"`
	Record  storage.Derivation `json:"record"`
}

type generationRecord struct {
	Version int                `json:"version"`
	Record  storage.Generation `json:"record"`
}

type activeRecord struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
}

func decodeDerivation(plain []byte) (storage.Derivation, error) {
	var decoded derivationRecord
	if err := decodeCanonicalJSON(plain, &decoded); err != nil || decoded.Version != valueVersion || !validDerivation(decoded.Record) {
		return storage.Derivation{}, ErrCorrupt
	}
	return decoded.Record, nil
}

func decodeGeneration(plain []byte) (storage.Generation, error) {
	var decoded generationRecord
	if err := decodeCanonicalJSON(plain, &decoded); err != nil || decoded.Version != valueVersion || !validGeneration(decoded.Record) {
		return storage.Generation{}, ErrCorrupt
	}
	return decoded.Record, nil
}

func decodeActive(plain []byte) (activeRecord, error) {
	var decoded activeRecord
	if err := decodeCanonicalJSON(plain, &decoded); err != nil || decoded.Version != valueVersion || !validID(decoded.ID) {
		return activeRecord{}, ErrCorrupt
	}
	return decoded, nil
}

func decodeCanonicalJSON(plain []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return ErrCorrupt
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrCorrupt
	}
	canonical, err := json.Marshal(output)
	if err != nil {
		return ErrCorrupt
	}
	valid := bytes.Equal(canonical, plain)
	clear(canonical)
	if !valid {
		return ErrCorrupt
	}
	return nil
}

func validPage(limit int, after string, hash bool) bool {
	return limit > 0 && limit <= storage.MaxListLimit &&
		(after == "" || (!hash && validID(after)) || (hash && validHash(after)))
}

func validID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := range id {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func validHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for i := range value {
		c := value[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validDerivation(derivation storage.Derivation) bool {
	if !validID(derivation.ID) || !validID(derivation.GenerationID) || !validHash(derivation.ProviderModelDigest) || !validHash(derivation.OutputHash) ||
		derivation.ProcessorVersion <= 0 || derivation.SchemaVersion <= 0 || derivation.PromptVersion <= 0 || derivation.RedactionVersion != privacy.Version ||
		len(derivation.SourceIDs) == 0 || len(derivation.SourceIDs) > storage.MaxListLimit || len(derivation.SourceIDs) != len(derivation.SourceHashes) ||
		derivation.CoveredSources < 0 || derivation.OmittedSources < 0 || derivation.CoveredSources > len(derivation.SourceIDs) || derivation.OmittedSources > len(derivation.SourceIDs) || derivation.CoveredSources+derivation.OmittedSources != len(derivation.SourceIDs) ||
		derivation.InputTokens < 0 || derivation.OutputTokens < 0 || derivation.Latency < 0 {
		return false
	}
	switch derivation.State {
	case storage.Building, storage.Active, storage.Stale, storage.Quarantined, storage.Failed, storage.Superseded:
	default:
		return false
	}
	if derivation.State == storage.Active && derivation.CoveredSources == 0 {
		return false
	}
	last := ""
	for i, id := range derivation.SourceIDs {
		if !validHash(id) || !validHash(derivation.SourceHashes[i]) || id <= last {
			return false
		}
		last = id
	}
	return true
}

func validGeneration(generation storage.Generation) bool {
	if !validID(generation.ID) || generation.SchemaVersion <= 0 || generation.RedactionVersion != privacy.Version ||
		len(generation.DerivationIDs) == 0 || len(generation.DerivationIDs) > storage.MaxListLimit {
		return false
	}
	last := ""
	for _, id := range generation.DerivationIDs {
		if !validID(id) || id <= last {
			return false
		}
		last = id
	}
	return true
}
