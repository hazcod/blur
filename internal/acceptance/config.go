// Package acceptance configures separately built native acceptance fixtures.
// Production builds cannot activate this mode through environment variables.
package acceptance

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Both values are set only by the fixture build's linker flags. A fixture
// binary without valid configuration fails closed instead of using user data.
var compiledDirectory string
var compiledNonce string

var ErrConfiguration = errors.New("acceptance: fixture configuration unavailable")

type Config struct{ Root, Domain, AppID, SecretService string }

func Load() (Config, error) {
	if !Enabled {
		return Config{}, ErrConfiguration
	}
	return loadAt(compiledDirectory, compiledNonce)
}

func (c Config) DataDirectory() string { return filepath.Join(c.Root, "Data") }
func (c Config) LogDirectory() string  { return filepath.Join(c.Root, "Logs") }

func loadAt(directory, nonce string) (Config, error) {
	decoded, err := hex.DecodeString(nonce)
	if err != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != nonce {
		return Config{}, ErrConfiguration
	}
	clear(decoded)
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || !strings.HasPrefix(filepath.Base(directory), "blur-acceptance-") {
		return Config{}, ErrConfiguration
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return Config{}, ErrConfiguration
	}
	root, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return Config{}, ErrConfiguration
	}
	for _, name := range []string{"Data", "Logs"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return Config{}, ErrConfiguration
		}
	}
	path := filepath.Join(root, "acceptance.json")
	marker, err := os.Lstat(path)
	if err != nil || !marker.Mode().IsRegular() || marker.Mode().Perm()&0o077 != 0 || marker.Size() > 4096 {
		return Config{}, ErrConfiguration
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, ErrConfiguration
	}
	defer func(close func() error) { _ = close() }(file.Close)
	actual, err := file.Stat()
	if err != nil || !os.SameFile(marker, actual) {
		return Config{}, ErrConfiguration
	}
	var value struct {
		Version int    `json:"version"`
		Nonce   string `json:"nonce"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil || value.Version != 1 || value.Nonce != nonce {
		return Config{}, ErrConfiguration
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Config{}, ErrConfiguration
	}
	return Config{Root: root, Domain: "blur-" + nonce + ".localhost", AppID: "com.hazcod.blur.acceptance.fixture." + nonce, SecretService: "sh.blur.acceptance." + nonce}, nil
}
