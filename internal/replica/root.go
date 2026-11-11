package replica

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const rootFileName = "blur-replica.json"

// ErrFolderNotReady means the sync provider has not delivered the folder root
// yet (for example an iCloud placeholder), so nothing should be created.
var ErrFolderNotReady = errors.New("replica: folder not ready")

// Root is the folder-level metadata: format version and the PBKDF2 salt.
type Root struct {
	Version int    `json:"version"`
	Salt    []byte `json:"salt"`
}

// LoadOrCreateRoot reads blur-replica.json, creating it with a fresh salt only
// when neither it nor a provider placeholder exists.
func LoadOrCreateRoot(dir string) (Root, error) {
	path := filepath.Join(dir, rootFileName)
	if root, err := readRoot(path); err == nil {
		return root, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Root{}, err
	}
	if _, err := os.Stat(filepath.Join(dir, "."+rootFileName+".icloud")); err == nil {
		return Root{}, ErrFolderNotReady
	}
	root := Root{Version: 1, Salt: make([]byte, SaltSize)}
	if _, err := io.ReadFull(rand.Reader, root.Salt); err != nil {
		return Root{}, err
	}
	encoded, _ := json.Marshal(root)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return readRoot(path)
	}
	if err != nil {
		return Root{}, err
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return Root{}, err
	}
	if err := file.Close(); err != nil {
		return Root{}, err
	}
	return root, nil
}

func readRoot(path string) (Root, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Root{}, err
	}
	if len(data) > 4096 {
		return Root{}, ErrFormat
	}
	var root Root
	if json.Unmarshal(data, &root) != nil || root.Version != 1 || len(root.Salt) < 8 || len(root.Salt) > 64 {
		return Root{}, ErrFormat
	}
	return root, nil
}

func encodeHex(b []byte) string { return hex.EncodeToString(b) }
func decodeHex(s string) ([]byte, error) {
	return hex.DecodeString(s)
}

// ParseDeviceID parses a canonical lowercase UUID string.
func ParseDeviceID(s string) ([16]byte, bool) {
	var id [16]byte
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' || s != strings.ToLower(s) {
		return id, false
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(raw) != 16 {
		return id, false
	}
	copy(id[:], raw)
	return id, true
}

// FormatDeviceID renders a device ID as a UUID string (the folder name).
func FormatDeviceID(id [16]byte) string {
	h := hex.EncodeToString(id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:])
}

// NewDeviceID returns a random UUID v4.
func NewDeviceID() (string, error) {
	var id [16]byte
	if _, err := io.ReadFull(rand.Reader, id[:]); err != nil {
		return "", err
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return FormatDeviceID(id), nil
}
