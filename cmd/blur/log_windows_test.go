//go:build windows

package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsPersistentLoggingWritesUserLogAndCrashFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	closeLog, err := configurePersistentLogging()
	if err != nil {
		t.Fatal(err)
	}
	slog.Info("windows log regression marker")
	closeLog()

	logDirectory := filepath.Join(root, "Blur", "Logs")
	logData, err := os.ReadFile(filepath.Join(logDirectory, "blur.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "windows log regression marker") {
		t.Fatalf("persistent log omitted marker: %s", logData)
	}
	if info, err := os.Stat(filepath.Join(logDirectory, "crash.log")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("runtime crash log info=%v err=%v", info, err)
	}
	for _, path := range []string{filepath.Dir(logDirectory), logDirectory, filepath.Join(logDirectory, "blur.log"), filepath.Join(logDirectory, "crash.log")} {
		assertWindowsUserOnlyACL(t, path)
	}
}

func assertWindowsUserOnlyACL(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil {
		t.Fatalf("read ACL for %q: descriptor=%v err=%v", path, sd, err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		t.Fatalf("read current user SID: %v", err)
	}
	owner, defaulted, err := sd.Owner()
	if err != nil || defaulted || owner == nil || !owner.Equals(user.User.Sid) {
		t.Fatalf("ACL for %q has an unexpected owner: %v", path, err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("ACL for %q is not protected: %v", path, err)
	}
	dacl, defaulted, err := sd.DACL()
	if err != nil || defaulted || dacl == nil || dacl.AceCount != 2 {
		t.Fatalf("ACL for %q must grant only the current user and SYSTEM: %v", path, err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	seenUser, seenSystem := false, false
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask != windows.ACCESS_MASK(0x001f01ff) || ace.Header.AceFlags & ^byte(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) != 0 {
			t.Fatalf("ACL for %q contains an unexpected access grant: %v", path, err)
		}
		trustee := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case trustee.Equals(user.User.Sid) && !seenUser:
			seenUser = true
		case trustee.Equals(system) && !seenSystem:
			seenSystem = true
		default:
			t.Fatalf("ACL for %q has an unexpected or repeated trustee", path)
		}
	}
	if !seenUser || !seenSystem {
		t.Fatalf("ACL for %q omitted the current user or SYSTEM", path)
	}
}

func TestWindowsPersistentLogReopenAppendsWithoutOverwriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blur.log")
	if err := os.WriteFile(path, []byte("previous\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writer, err := openWindowsPersistentLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("current\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "previous\ncurrent\n" {
		t.Fatalf("reopened log = %q, err=%v", got, err)
	}
}

func TestWindowsPersistentLogRotatesWithoutGrowingPastLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blur.log")
	writer, err := openWindowsPersistentLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(make([]byte, maxPersistentLogBytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("next-record\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	assertWindowsUserOnlyACL(t, path)
	assertWindowsUserOnlyACL(t, path+".1")
	rotated, err := os.Stat(path + ".1")
	if err != nil || rotated.Size() != maxPersistentLogBytes {
		t.Fatalf("rotated log size=%v err=%v, want %d", rotated, err, maxPersistentLogBytes)
	}
	current, err := os.ReadFile(path)
	if err != nil || string(current) != "next-record\n" {
		t.Fatalf("current log=%q err=%v", current, err)
	}
}

func TestWindowsCrashLogPreservesPreviousRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crash.log")
	previous := []byte("previous runtime crash\n")
	if err := os.WriteFile(path, previous, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := openWindowsCrashLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("current runtime crash\n")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	assertWindowsUserOnlyACL(t, path)
	assertWindowsUserOnlyACL(t, path+".1")
	if got, err := os.ReadFile(path + ".1"); err != nil || string(got) != string(previous) {
		t.Fatalf("previous crash backup = %q, err=%v", got, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "current runtime crash\n" {
		t.Fatalf("current crash log = %q, err=%v", got, err)
	}
}
