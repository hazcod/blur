// Package recordcodec authenticates bounded opaque records before plaintext is
// released to callers.
package recordcodec

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	KeySize           = 32
	NonceSize         = 12
	AuthenticationTag = 16
	MaxPlaintextBytes = 4 << 20
	MaxEnvelopeBytes  = 1 + NonceSize + AuthenticationTag + MaxPlaintextBytes
	MaxNamespaceBytes = 64
	MaxRecordKeyBytes = 1024

	envelopeVersion = 1
	nonceOffset     = 1
	headerSize      = nonceOffset + NonceSize
)

var (
	ErrInvalidKey         = errors.New("recordcodec: invalid key")
	ErrInvalidMetadata    = errors.New("recordcodec: invalid metadata")
	ErrInvalidEnvelope    = errors.New("recordcodec: invalid envelope")
	ErrUnsupportedVersion = errors.New("recordcodec: unsupported envelope version")
	ErrAuthentication     = errors.New("recordcodec: authentication failed")
	ErrOversize           = errors.New("recordcodec: size limit exceeded")
	ErrClosed             = errors.New("recordcodec: closed")
	ErrRandomSource       = errors.New("recordcodec: random source unavailable")
	ErrSerialization      = errors.New("recordcodec: serialization disabled")
)

// RecordMetadata binds a ciphertext to its logical location and schema
// revision. These strings are metadata only; record contents remain opaque.
type RecordMetadata struct {
	Namespace string
	Key       string
	Revision  uint64
}

// Codec owns a derived record key. Close clears its best-effort key buffer.
type Codec struct {
	state *codecState
}

type codecState struct {
	mu     sync.RWMutex
	key    []byte
	closed bool
}

// NewCodec derives an AES-256-GCM key from a 32-byte random data key. It keeps
// only the derived key and never formats key material.
func NewCodec(dataKey []byte) (*Codec, error) {
	if len(dataKey) != KeySize {
		return nil, ErrInvalidKey
	}
	master := append([]byte(nil), dataKey...)
	defer clear(master)
	key, err := deriveKey(master, []byte("blur.recordcodec.hkdf.v1"), "aes-256-gcm record key", KeySize)
	if err != nil {
		return nil, ErrInvalidKey
	}
	return &Codec{state: &codecState{key: key}}, nil
}

// deriveKey implements HKDF-SHA-256 and is kept small so its RFC 5869
// behavior can be pinned independently of the record envelope.
func deriveKey(secret, salt []byte, info string, length int) ([]byte, error) {
	key, err := hkdf.Key(sha256.New, secret, salt, info, length)
	if err != nil {
		return nil, ErrInvalidKey
	}
	return key, nil
}

// Seal encrypts plaintext into a versioned envelope with a fresh random GCM
// nonce. The namespace, key, and revision are authenticated as associated data.
func (c *Codec) Seal(metadata RecordMetadata, plaintext []byte) ([]byte, error) {
	if err := validateMetadata(metadata); err != nil {
		return nil, err
	}
	if len(plaintext) > MaxPlaintextBytes {
		return nil, ErrOversize
	}
	key, err := c.keyCopy()
	if err != nil {
		return nil, err
	}
	defer clear(key)
	aead, err := newAEAD(key)
	if err != nil {
		return nil, ErrInvalidKey
	}

	var nonce [NonceSize]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nil, ErrRandomSource
	}
	envelope := make([]byte, headerSize, headerSize+len(plaintext)+aead.Overhead())
	envelope[0] = envelopeVersion
	copy(envelope[nonceOffset:], nonce[:])
	envelope = aead.Seal(envelope, nonce[:], plaintext, associatedData(metadata))
	return envelope, nil
}

// Open authenticates the envelope and its metadata before returning plaintext.
func (c *Codec) Open(metadata RecordMetadata, envelope []byte) ([]byte, error) {
	if len(envelope) > MaxEnvelopeBytes {
		return nil, ErrOversize
	}
	if err := validateMetadata(metadata); err != nil {
		return nil, err
	}
	if len(envelope) < headerSize+AuthenticationTag {
		return nil, ErrInvalidEnvelope
	}
	if envelope[0] != envelopeVersion {
		return nil, ErrUnsupportedVersion
	}
	key, err := c.keyCopy()
	if err != nil {
		return nil, err
	}
	defer clear(key)
	aead, err := newAEAD(key)
	if err != nil {
		return nil, ErrInvalidKey
	}
	nonce := envelope[nonceOffset:headerSize]
	plaintext, err := aead.Open(nil, nonce, envelope[headerSize:], associatedData(metadata))
	if err != nil {
		return nil, ErrAuthentication
	}
	if len(plaintext) > MaxPlaintextBytes {
		clear(plaintext)
		return nil, ErrOversize
	}
	return plaintext, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (c *Codec) keyCopy() ([]byte, error) {
	if c == nil || c.state == nil {
		return nil, ErrClosed
	}
	state := c.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if state.closed {
		return nil, ErrClosed
	}
	return append([]byte(nil), state.key...), nil
}

func validateMetadata(metadata RecordMetadata) error {
	if metadata.Namespace == "" || metadata.Key == "" || metadata.Revision == 0 ||
		len(metadata.Namespace) > MaxNamespaceBytes || len(metadata.Key) > MaxRecordKeyBytes ||
		!utf8.ValidString(metadata.Namespace) || !utf8.ValidString(metadata.Key) ||
		strings.ContainsRune(metadata.Namespace, 0) || strings.ContainsRune(metadata.Key, 0) {
		return ErrInvalidMetadata
	}
	return nil
}

func associatedData(metadata RecordMetadata) []byte {
	out := make([]byte, 1+2+len(metadata.Namespace)+2+len(metadata.Key)+8)
	index := 0
	out[index] = envelopeVersion
	index++
	binary.BigEndian.PutUint16(out[index:index+2], uint16(len(metadata.Namespace)))
	index += 2
	copy(out[index:], metadata.Namespace)
	index += len(metadata.Namespace)
	binary.BigEndian.PutUint16(out[index:index+2], uint16(len(metadata.Key)))
	index += 2
	copy(out[index:], metadata.Key)
	index += len(metadata.Key)
	binary.BigEndian.PutUint64(out[index:], metadata.Revision)
	return out
}

// Close clears the retained key buffer and prevents further operations.
func (c *Codec) Close() error {
	if c == nil || c.state == nil {
		return ErrClosed
	}
	state := c.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.closed {
		clear(state.key)
		state.key = nil
		state.closed = true
	}
	return nil
}

const redactedCodec = "recordcodec.Codec([redacted])"

// Format redacts the codec regardless of formatting verb, including %#v on a
// dereferenced value or shallow copy. Secret state is kept behind a private
// pointer so formatting a value cannot traverse key bytes.
func (Codec) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, redactedCodec) }

func (Codec) String() string   { return redactedCodec }
func (Codec) GoString() string { return redactedCodec }

// MarshalText and MarshalJSON refuse serialization instead of disclosing the
// key or implying that the codec is a serializable value.
func (Codec) MarshalText() ([]byte, error) { return nil, ErrSerialization }
func (Codec) MarshalJSON() ([]byte, error) { return nil, ErrSerialization }
