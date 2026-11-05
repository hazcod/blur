// Package bolt provides the bounded opaque byte persistence boundary for
// encrypted records. It has no domain knowledge and never logs record bytes.
package bolt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	bbolt "go.etcd.io/bbolt"
)

const (
	MaxKeyBytes         = 4096
	MaxValueBytes       = 5 << 20
	MaxTransactionBytes = 16 << 20
	MaxTransactionOps   = 1024

	openTimeout = 5 * time.Second
)

var (
	ErrInvalid     = errors.New("physical bolt: invalid input or path")
	ErrUnavailable = errors.New("physical bolt: unavailable")
	ErrCorrupt     = errors.New("physical bolt: corrupt database")
	ErrTransaction = errors.New("physical bolt: transaction failed")
	ErrClosed      = errors.New("physical bolt: closed")
	ErrInvalidTx   = errors.New("physical bolt: transaction limit or operation rejected")
)

var recordBucket = []byte("blur_records_v1")

// Reader exposes only bounded opaque byte lookups.
type Reader interface {
	Get(key []byte) (value []byte, found bool, err error)
}

// Writer exposes only bounded opaque byte mutations and lookups.
type Writer interface {
	Reader
	Put(key, value []byte) error
	Delete(key []byte) error
}

// DB is a single-file transactional opaque key/value store.
type DB struct {
	mu     sync.RWMutex
	db     *bbolt.DB
	closed bool
}

// Open opens or creates path only through a pinned private parent-directory
// handle. The platform boundary rejects links/reparse points and verifies the
// opened database object, rather than trusting pre-open pathname metadata.
func Open(path string) (*DB, error) {
	return openWithDirectoryHook(path, nil)
}

// openWithDirectoryHook places the test hook after the parent has been opened
// and verified but before the database is opened relative to that handle.
func openWithDirectoryHook(path string, afterDirectoryOpen func() error) (*DB, error) {
	if path == "" {
		return nil, ErrInvalid
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrInvalid
	}
	absolute = filepath.Clean(absolute)
	directory := filepath.Dir(absolute)
	name := filepath.Base(absolute)
	if name == "." || name == string(filepath.Separator) || name == "" || name == ".." {
		return nil, ErrInvalid
	}
	parent, err := openSecureDirectory(directory)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	if afterDirectoryOpen != nil {
		if err := afterDirectoryOpen(); err != nil {
			return nil, ErrUnavailable
		}
	}

	database, err := openBbolt(absolute, func(_ string, flags int, mode os.FileMode) (*os.File, error) {
		return parent.OpenFile(name, flags, mode)
	})
	if err != nil {
		if errors.Is(err, ErrInvalid) {
			return nil, ErrInvalid
		}
		if errors.Is(err, ErrCorrupt) || errors.Is(err, bbolt.ErrInvalid) || errors.Is(err, bbolt.ErrChecksum) || errors.Is(err, bbolt.ErrVersionMismatch) {
			return nil, ErrCorrupt
		}
		return nil, ErrUnavailable
	}
	if err := database.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(recordBucket)
		return err
	}); err != nil {
		_ = database.Close()
		return nil, ErrCorrupt
	}
	return &DB{db: database}, nil
}

func openBbolt(path string, openFile func(string, int, os.FileMode) (*os.File, error)) (database *bbolt.DB, err error) {
	defer func() {
		if recover() != nil {
			database = nil
			err = ErrCorrupt
		}
	}()
	database, err = bbolt.Open(path, 0o600, &bbolt.Options{
		Timeout:        openTimeout,
		NoSync:         false,
		NoFreelistSync: false,
		NoGrowSync:     false,
		OpenFile:       openFile,
	})
	return database, err
}

// View runs a bounded read-only transaction. Callback errors are reduced to a
// content-free sentinel so caller-provided text cannot enter backend errors.
func (db *DB) View(ctx context.Context, callback func(Reader) error) error {
	if ctx == nil || callback == nil {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if db == nil {
		return ErrClosed
	}
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed || db.db == nil {
		return ErrClosed
	}
	state := &txState{ctx: ctx, active: true}
	err := db.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(recordBucket)
		if bucket == nil {
			state.setError(ErrCorrupt)
			return ErrCorrupt
		}
		reader := &readTx{bucket: bucket, state: state}
		callbackErr := callback(reader)
		state.finish()
		if stateErr := state.result(); stateErr != nil {
			return stateErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if callbackErr != nil {
			return sanitizeCallbackError(callbackErr)
		}
		return nil
	})
	return sanitizeTransactionError(err)
}

// Update runs a bounded serializable write transaction. Failed operations or
// callback errors abort the transaction even when a callback ignores an error.
func (db *DB) Update(ctx context.Context, callback func(Writer) error) error {
	if ctx == nil || callback == nil {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if db == nil {
		return ErrClosed
	}
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed || db.db == nil {
		return ErrClosed
	}
	state := &txState{ctx: ctx, active: true}
	err := db.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(recordBucket)
		if bucket == nil {
			state.setError(ErrCorrupt)
			return ErrCorrupt
		}
		writer := &writeTx{readTx: readTx{bucket: bucket, state: state}}
		callbackErr := callback(writer)
		state.finish()
		if stateErr := state.result(); stateErr != nil {
			return stateErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if callbackErr != nil {
			return sanitizeCallbackError(callbackErr)
		}
		return nil
	})
	return sanitizeTransactionError(err)
}

func sanitizeCallbackError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrInvalidTx):
		return ErrInvalidTx
	default:
		return ErrTransaction
	}
}

func sanitizeTransactionError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrInvalidTx):
		return ErrInvalidTx
	case errors.Is(err, ErrCorrupt):
		return ErrCorrupt
	case errors.Is(err, ErrClosed):
		return ErrClosed
	default:
		return ErrTransaction
	}
}

// Close waits for active transactions and closes the database. It is safe to
// call repeatedly.
func (db *DB) Close() error {
	if db == nil {
		return ErrClosed
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return nil
	}
	db.closed = true
	if db.db == nil {
		return nil
	}
	database := db.db
	db.db = nil
	if err := database.Close(); err != nil {
		return ErrUnavailable
	}
	return nil
}

type txState struct {
	mu     sync.Mutex
	ctx    context.Context
	active bool
	ops    int
	bytes  int
	err    error
}

func (s *txState) fail(err error) error {
	if s.err == nil {
		s.err = err
	}
	return s.err
}

func (s *txState) setError(err error) {
	s.mu.Lock()
	s.fail(err)
	s.mu.Unlock()
}

func (s *txState) result() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *txState) begin(key []byte, valueBytes int) error {
	if !s.active {
		return s.fail(ErrClosed)
	}
	if err := s.ctx.Err(); err != nil {
		return s.fail(err)
	}
	if len(key) == 0 || len(key) > MaxKeyBytes || valueBytes < 0 || valueBytes > MaxValueBytes {
		return s.fail(ErrInvalidTx)
	}
	if s.ops >= MaxTransactionOps {
		return s.fail(ErrInvalidTx)
	}
	if len(key) > MaxTransactionBytes-valueBytes || s.bytes > MaxTransactionBytes-len(key)-valueBytes {
		return s.fail(ErrInvalidTx)
	}
	s.ops++
	s.bytes += len(key) + valueBytes
	return nil
}

func (s *txState) chargeReadValue(valueBytes int) error {
	if !s.active {
		return s.fail(ErrClosed)
	}
	if valueBytes < 0 || valueBytes > MaxValueBytes || valueBytes > MaxTransactionBytes-s.bytes {
		return s.fail(ErrInvalidTx)
	}
	s.bytes += valueBytes
	return nil
}

func (s *txState) finish() {
	s.mu.Lock()
	s.active = false
	s.mu.Unlock()
}

type readTx struct {
	bucket *bbolt.Bucket
	state  *txState
}

func (tx *readTx) Get(key []byte) ([]byte, bool, error) {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	if err := tx.state.begin(key, 0); err != nil {
		return nil, false, err
	}
	value := tx.bucket.Get(key)
	if value == nil {
		return nil, false, nil
	}
	if err := tx.state.chargeReadValue(len(value)); err != nil {
		return nil, false, err
	}
	return append([]byte(nil), value...), true, nil
}

type writeTx struct{ readTx }

func (tx *writeTx) Put(key, value []byte) error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	if err := tx.state.begin(key, len(value)); err != nil {
		return err
	}
	if err := tx.bucket.Put(append([]byte(nil), key...), append([]byte(nil), value...)); err != nil {
		return tx.state.fail(ErrTransaction)
	}
	return nil
}

func (tx *writeTx) Delete(key []byte) error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	if err := tx.state.begin(key, 0); err != nil {
		return err
	}
	if err := tx.bucket.Delete(append([]byte(nil), key...)); err != nil {
		return tx.state.fail(ErrTransaction)
	}
	return nil
}
