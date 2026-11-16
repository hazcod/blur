package bolt

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateTempFileRejectsLinkedDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(directory, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	file, _, err := CreatePrivateTempFile(link, ".settings-")
	if file != nil {
		_ = file.Close()
		t.Fatal("created a private file through a linked directory")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("linked settings directory was not rejected", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatal("linked directory target was modified", err)
	}
}

func TestPrivateTempFileValidatesAndSurvivesAtomicPublication(t *testing.T) {
	directory := t.TempDir()
	file, name, err := CreatePrivateTempFile(directory, ".settings-")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePrivateFile(file); err != nil {
		t.Fatal("temporary settings file was not private before write", err)
	}
	if _, err := file.Write([]byte("private fixture")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "settings.json")
	if err := os.Rename(name, path); err != nil {
		t.Fatal(err)
	}
	opened, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Close() }()
	if err := ValidatePrivateFile(opened); err != nil {
		t.Fatal("published settings file lost its private permissions", err)
	}
}
