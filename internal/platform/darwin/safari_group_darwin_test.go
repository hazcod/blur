package darwin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafariGroupIDValidationAndPrivateSocketDirectory(t *testing.T) {
	for _, group := range []string{"", "blur", "group..blur", "group.blur/other", "group.blur."} {
		if validSafariAppGroupID(group) {
			t.Fatalf("accepted invalid App Group ID %q", group)
		}
	}
	if !validSafariAppGroupID("group.sh.blur.shared") {
		t.Fatal("rejected valid App Group ID")
	}
	root, err := os.MkdirTemp("/tmp", "b-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !ownedByCurrentUID(info) {
		t.Fatalf("temp directory not owned: %#v", info.Sys())
	}
	path, err := SafariSocketPath(root)
	if err != nil {
		created, _ := os.Lstat(filepath.Join(root, "Blur"))
		t.Fatalf("SafariSocketPath: %v root=%v child=%v owned=%v", err, info.Mode(), created.Mode(), ownedByCurrentUID(created))
	}
	if path != filepath.Join(root, "b", "s") {
		t.Fatalf("socket path=%q", path)
	}
	info, err = os.Stat(filepath.Dir(path))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("socket directory mode=%v err=%v", info, err)
	}
	if err := os.Remove(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "b")); err != nil {
		t.Fatal(err)
	}
	if _, err := SafariSocketPath(root); err == nil {
		t.Fatal("accepted symlinked socket directory")
	}
}
