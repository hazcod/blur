// Package replica exports sealed, append-only change segments to a folder that
// a file-sync service carries, and imports other devices' segments. Segment
// names are content-free sequence numbers; plaintext never leaves the store
// without AES-256-GCM sealing under a passphrase-derived key.
package replica

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"time"
)

const (
	// SegmentVersion is the header and plaintext format version.
	SegmentVersion = 1
	// PBKDF2Iterations is the work factor for the passphrase-derived key.
	PBKDF2Iterations = 600000
	KeySize          = 32
	SaltSize         = 16
	// PadClass is the plaintext size class; plaintext is padded to a multiple.
	PadClass = 64 << 10
	// MaxSegmentPlaintext caps one segment's plaintext.
	MaxSegmentPlaintext = 16 << 20
	// maxRecordBytes leaves room for padding and framing inside the cap.
	maxRecordBytes = MaxSegmentPlaintext - PadClass - 64

	headerSize = 1 + 16 + 8 + 12

	// Record kinds. 0 is padding. Kind 3 (user notes with their own record
	// type) is reserved; notes currently travel as ordinary observations.
	KindPadding     = 0
	KindObservation = 1
	KindTombstone   = 2
)

var (
	// ErrAuth means the segment failed authentication: wrong key or tampering.
	ErrAuth = errors.New("replica: segment authentication failed")
	// ErrFormat means the segment is structurally invalid.
	ErrFormat = errors.New("replica: invalid segment")
	// ErrNoKey means no sync key is available on this device.
	ErrNoKey = errors.New("replica: sync passphrase not set")
)

// Header is the authenticated, unencrypted segment header.
type Header struct {
	Version  uint8
	DeviceID [16]byte
	Seq      uint64
}

// Record is one plaintext entry. Observation holds the store's authenticated
// observation record bytes exactly; SourceID is a 64-hex source ID, or "*" for
// an "everything captured before At" tombstone.
type Record struct {
	Kind        byte
	At          time.Time
	Observation []byte
	SourceID    string
}

// DeriveKey derives the 32-byte sync key from a passphrase and the folder salt.
func DeriveKey(passphrase string, salt []byte) ([]byte, error) {
	if passphrase == "" || len(salt) < 8 {
		return nil, ErrNoKey
	}
	return pbkdf2.Key(sha256.New, passphrase, salt, PBKDF2Iterations, KeySize)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, ErrNoKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func encodeRecords(records []Record) ([]byte, error) {
	out := make([]byte, 1, 1024)
	out[0] = SegmentVersion
	put := func(kind byte, payload []byte) {
		var head [5]byte
		head[0] = kind
		binary.BigEndian.PutUint32(head[1:], uint32(len(payload)))
		out = append(out, head[:]...)
		out = append(out, payload...)
	}
	for _, r := range records {
		switch r.Kind {
		case KindObservation:
			if len(r.Observation) == 0 || r.At.IsZero() {
				return nil, ErrFormat
			}
			payload := make([]byte, 8, 8+len(r.Observation))
			binary.BigEndian.PutUint64(payload, uint64(r.At.UTC().UnixNano()))
			payload = append(payload, r.Observation...)
			put(KindObservation, payload)
		case KindTombstone:
			idLen := 0
			if r.SourceID != "*" {
				idLen = 32
			}
			if r.At.IsZero() || r.SourceID != "*" && len(r.SourceID) != 64 {
				return nil, ErrFormat
			}
			payload := make([]byte, 9, 9+idLen)
			binary.BigEndian.PutUint64(payload, uint64(r.At.UTC().UnixNano()))
			payload[8] = byte(idLen)
			if idLen > 0 {
				id, err := decodeHex(r.SourceID)
				if err != nil {
					return nil, ErrFormat
				}
				payload = append(payload, id...)
			}
			put(KindTombstone, payload)
		default:
			return nil, ErrFormat
		}
		if len(out) > maxRecordBytes {
			return nil, ErrFormat
		}
	}
	// Pad to the next size class with one padding record.
	pad := PadClass - len(out)%PadClass
	if pad < 5 {
		pad += PadClass
	}
	var head [5]byte
	binary.BigEndian.PutUint32(head[1:], uint32(pad-5))
	out = append(out, head[:]...)
	out = append(out, make([]byte, pad-5)...)
	if len(out) > MaxSegmentPlaintext {
		return nil, ErrFormat
	}
	return out, nil
}

func decodeRecords(plain []byte) ([]Record, error) {
	if len(plain) < 1 || plain[0] != SegmentVersion || len(plain) > MaxSegmentPlaintext {
		return nil, ErrFormat
	}
	rest := plain[1:]
	var records []Record
	for len(rest) > 0 {
		if len(rest) < 5 {
			return nil, ErrFormat
		}
		kind := rest[0]
		n := int(binary.BigEndian.Uint32(rest[1:5]))
		rest = rest[5:]
		if n > len(rest) {
			return nil, ErrFormat
		}
		payload := rest[:n]
		rest = rest[n:]
		switch kind {
		case KindPadding:
		case KindObservation:
			if len(payload) <= 8 {
				return nil, ErrFormat
			}
			at := time.Unix(0, int64(binary.BigEndian.Uint64(payload))).UTC()
			records = append(records, Record{Kind: kind, At: at, Observation: append([]byte(nil), payload[8:]...)})
		case KindTombstone:
			if len(payload) < 9 {
				return nil, ErrFormat
			}
			at := time.Unix(0, int64(binary.BigEndian.Uint64(payload))).UTC()
			r := Record{Kind: kind, At: at, SourceID: "*"}
			switch int(payload[8]) {
			case 0:
				if len(payload) != 9 {
					return nil, ErrFormat
				}
			case 32:
				if len(payload) != 9+32 {
					return nil, ErrFormat
				}
				r.SourceID = encodeHex(payload[9:])
			default:
				return nil, ErrFormat
			}
			records = append(records, r)
		default:
			return nil, ErrFormat
		}
	}
	return records, nil
}

// SealSegment encodes, pads and seals records. The header (version, device ID,
// sequence, nonce) is authenticated as additional data.
func SealSegment(key []byte, deviceID [16]byte, seq uint64, records []Record, random io.Reader) ([]byte, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if random == nil {
		random = rand.Reader
	}
	plain, err := encodeRecords(records)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	header := make([]byte, headerSize, headerSize+len(plain)+aead.Overhead())
	header[0] = SegmentVersion
	copy(header[1:17], deviceID[:])
	binary.BigEndian.PutUint64(header[17:25], seq)
	if _, err := io.ReadFull(random, header[25:]); err != nil {
		return nil, err
	}
	return aead.Seal(header, header[25:], plain, header[:headerSize]), nil
}

// OpenSegment authenticates and decrypts a segment. Wrong key, any tampering
// of header or body, and truncation all return ErrAuth.
func OpenSegment(key, data []byte) (Header, []Record, error) {
	aead, err := newGCM(key)
	if err != nil {
		return Header{}, nil, err
	}
	if len(data) < headerSize+aead.Overhead() || len(data) > headerSize+MaxSegmentPlaintext+aead.Overhead() || data[0] != SegmentVersion {
		return Header{}, nil, ErrFormat
	}
	plain, err := aead.Open(nil, data[25:headerSize], data[headerSize:], data[:headerSize])
	if err != nil {
		return Header{}, nil, ErrAuth
	}
	defer clear(plain)
	h := Header{Version: data[0], Seq: binary.BigEndian.Uint64(data[17:25])}
	copy(h.DeviceID[:], data[1:17])
	records, err := decodeRecords(plain)
	if err != nil {
		return Header{}, nil, err
	}
	return h, records, nil
}
