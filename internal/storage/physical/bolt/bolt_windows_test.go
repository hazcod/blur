//go:build windows

package bolt

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsCreatePrivateDirectoryAndDatabase(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	if err := CreatePrivateDirectory(directory); err != nil {
		t.Fatalf("CreatePrivateDirectory() error = %v", err)
	}
	db, err := Open(filepath.Join(directory, "store.db"))
	if err != nil {
		t.Fatalf("Open() in private ACL directory = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	parent, err := openSecureDirectory(directory)
	if err != nil {
		t.Fatalf("openSecureDirectory() did not verify protected directory ACL: %v", err)
	}
	defer parent.Close()
	file, err := parent.OpenFile("store.db", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("handle-relative open did not verify protected database ACL: %v", err)
	}
	_ = file.Close()
}

func TestWindowsOpenRejectsBroadDirectoryACL(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "broad")
	if err := CreatePrivateDirectory(directory); err != nil {
		t.Fatalf("CreatePrivateDirectory() error = %v", err)
	}
	setEveryoneDACL(t, directory, true, true)
	if _, err := Open(filepath.Join(directory, "store.db")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Open(broad directory ACL) error = %v, want ErrInvalid", err)
	}
}

func TestWindowsOpenRejectsBroadDatabaseACL(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	if err := CreatePrivateDirectory(directory); err != nil {
		t.Fatalf("CreatePrivateDirectory() error = %v", err)
	}
	databasePath := filepath.Join(directory, "store.db")
	db, err := Open(databasePath)
	if err != nil {
		t.Fatalf("Open() before broad database ACL = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	setEveryoneDACL(t, databasePath, false, false)
	if _, err := Open(databasePath); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Open(broad database ACL) error = %v, want ErrInvalid", err)
	}
}

func TestWindowsOpenRejectsInheritedBroadDirectoryACL(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	if err := CreatePrivateDirectory(parent); err != nil {
		t.Fatalf("CreatePrivateDirectory(parent) error = %v", err)
	}
	setEveryoneDACL(t, parent, true, true)
	inherited := filepath.Join(parent, "inherited")
	if err := os.Mkdir(inherited, 0o700); err != nil {
		t.Fatalf("Mkdir(inherited) error = %v", err)
	}
	if _, err := Open(filepath.Join(inherited, "store.db")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Open(inherited broad directory ACL) error = %v, want ErrInvalid", err)
	}
}

func setEveryoneDACL(t *testing.T, path string, isDirectory, inherit bool) {
	t.Helper()
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	attributes := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if isDirectory {
		attributes |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(path16, windows.READ_CONTROL|windows.WRITE_DAC, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, attributes, 0)
	if err != nil {
		t.Fatalf("CreateFile(directory handle) error = %v", err)
	}
	defer windows.CloseHandle(handle)
	everyone, err := windows.StringToSid("S-1-1-0")
	if err != nil {
		t.Fatal(err)
	}
	inheritance := uint32(windows.NO_INHERITANCE)
	if inherit {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(everyone),
		},
	}}, nil)
	if err != nil {
		t.Fatalf("ACLFromEntries(Everyone) error = %v", err)
	}
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatalf("SetSecurityInfo(broad DACL) error = %v", err)
	}
}

func TestWindowsOpenRejectsDirectoryJunction(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(root, "junction")
	output, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput()
	if err != nil {
		t.Skipf("directory junction creation is unavailable on this Windows runner: %v (%s)", err, output)
	}
	if _, err := Open(filepath.Join(junction, "store.db")); err == nil {
		t.Fatal("Open(path through directory junction) succeeded")
	}
	if _, err := os.Lstat(filepath.Join(target, "store.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory junction target was modified: Lstat error = %v", err)
	}
}
