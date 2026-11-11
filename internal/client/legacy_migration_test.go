package client

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fakeMod(files map[string]time.Time) func(string) (time.Time, bool) {
	return func(p string) (time.Time, bool) { t, ok := files[p]; return t, ok }
}

func TestShouldMigrateLegacy(t *testing.T) {
	const c, l = "/home/Containers/Data/Library/Application Support/Blur", "/home/Library/Application Support/Blur"
	old := time.Unix(1000, 0)
	newer := old.Add(time.Hour)
	cases := []struct {
		name  string
		in    legacyMigrationInput
		files map[string]time.Time
		want  bool
	}{
		{"container store missing", legacyMigrationInput{true, c, l}, map[string]time.Time{l + "/memory.db": old}, true},
		{"legacy newer wins", legacyMigrationInput{true, c, l}, map[string]time.Time{l + "/memory.db": newer, c + "/memory.db": old}, true},
		{"container newer wins", legacyMigrationInput{true, c, l}, map[string]time.Time{l + "/memory.db": old, c + "/memory.db": newer}, false},
		{"equal mtime keeps container", legacyMigrationInput{true, c, l}, map[string]time.Time{l + "/memory.db": old, c + "/memory.db": old}, false},
		{"marker alone does not block", legacyMigrationInput{true, c, l}, map[string]time.Time{l + "/memory.db": old, c + "/migrated-from-legacy": old}, true},
		{"not sandboxed", legacyMigrationInput{false, c, l}, map[string]time.Time{l + "/memory.db": newer}, false},
		{"no legacy store", legacyMigrationInput{true, c, l}, map[string]time.Time{c + "/memory.db": old}, false},
		{"same directory", legacyMigrationInput{true, l, l}, map[string]time.Time{l + "/memory.db": old}, false},
		{"unknown legacy dir", legacyMigrationInput{true, c, ""}, map[string]time.Time{l + "/memory.db": old}, false},
	}
	for _, tc := range cases {
		if got := shouldMigrateLegacy(tc.in, fakeMod(tc.files)); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestLegacyReplacesOlderContainerStore(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "legacy")
	container := filepath.Join(root, "container", "Blur")
	for _, d := range []string{legacy, container} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(dir, name, body string, mod time.Time) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().Add(-time.Hour)
	write(container, "memory.db", "old", base)
	write(container, "preferences.json", `{"old":1}`, base)
	write(container, ".bootstrap.lock", "", base)
	write(legacy, "memory.db", "new", base.Add(time.Minute))
	write(legacy, "preferences.json", `{"new":1}`, base.Add(time.Minute))
	in := legacyMigrationInput{true, container, legacy}
	if !shouldMigrateLegacy(in, fileModTime) {
		t.Fatal("newer legacy store must trigger migration")
	}
	if _, err := copyLegacyStore(legacy, container, bytes.NewReader(make([]byte, 6))); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"memory.db": "new", "preferences.json": `{"new":1}`} {
		if b, _ := os.ReadFile(filepath.Join(container, name)); string(b) != want {
			t.Errorf("%s = %q want %q", name, b, want)
		}
	}
	if !pathExists(filepath.Join(container, ".bootstrap.lock")) {
		t.Error("container-only lock file must survive")
	}
	if shouldMigrateLegacy(in, fileModTime) {
		t.Error("migration must not repeat once the container is newer")
	}
	entries, _ := os.ReadDir(filepath.Dir(container))
	if len(entries) != 1 {
		t.Errorf("staging directory left behind: %v", entries)
	}
}

func TestCopyLegacyStore(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "legacy")
	container := filepath.Join(root, "container", "Blur")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"memory.db": "db", "preferences.json": "{}", ".bootstrap.lock": ""} {
		if err := os.WriteFile(filepath.Join(legacy, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := copyLegacyStore(legacy, container, bytes.NewReader(make([]byte, 6))); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"memory.db", "preferences.json", legacyMigrationMarker} {
		if !pathExists(filepath.Join(container, name)) {
			t.Errorf("%s missing in container", name)
		}
	}
	if pathExists(filepath.Join(container, ".bootstrap.lock")) {
		t.Error("lock file must not be copied")
	}
	if b, _ := os.ReadFile(filepath.Join(legacy, "memory.db")); string(b) != "db" {
		t.Error("legacy data changed")
	}
	entries, _ := os.ReadDir(filepath.Dir(container))
	if len(entries) != 1 {
		t.Errorf("staging directory left behind: %v", entries)
	}
	if shouldMigrateLegacy(legacyMigrationInput{true, container, legacy}, fileModTime) {
		t.Error("migration must not repeat")
	}
}

func TestLegacyHomeFromContainer(t *testing.T) {
	if got := legacyHomeFromContainer("/Users/a/Library/Containers/com.hazcod.blur/Data"); got != "/Users/a" {
		t.Errorf("got %q", got)
	}
	if got := legacyHomeFromContainer("/Users/a"); got != "" {
		t.Errorf("got %q", got)
	}
}
