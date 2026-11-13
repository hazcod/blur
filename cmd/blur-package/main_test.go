package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackagePortableWindowsArchiveContainsAppAndNotices(t *testing.T) {
	root, binary := packageFixture(t)
	mcpBinary := filepath.Join(t.TempDir(), "blur-mcp.exe")
	if err := os.WriteFile(mcpBinary, []byte("mcp fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "blur-windows-amd64.zip")
	if err := packagePortable(root, binary, mcpBinary, output, "windows", "amd64"); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	want := map[string]string{
		"blur-windows-amd64/Blur.exe":     "binary fixture",
		"blur-windows-amd64/blur-mcp.exe": "mcp fixture",
		"blur-windows-amd64/README.md":    "readme fixture",
		"blur-windows-amd64/LICENSE":      "license fixture",
		"blur-windows-amd64/SECURITY.md":  "security fixture",
	}
	for _, entry := range archive.File {
		if expected, ok := want[entry.Name]; ok {
			reader, err := entry.Open()
			if err != nil {
				t.Fatal(err)
			}
			contents, readErr := io.ReadAll(reader)
			closeErr := reader.Close()
			if readErr != nil || closeErr != nil || string(contents) != expected {
				t.Fatalf("archive entry %q = %q, read=%v close=%v", entry.Name, contents, readErr, closeErr)
			}
			delete(want, entry.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing archive entries: %v", want)
	}
}

func TestPackagePortableLinuxArchiveIncludesLauncherAndArtwork(t *testing.T) {
	root, binary := packageFixture(t)
	output := filepath.Join(t.TempDir(), "blur-linux-amd64.tar.gz")
	if err := packagePortable(root, binary, "", output, "linux", "amd64"); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	archive := tar.NewReader(gz)
	entries := map[string]string{}
	modes := map[string]int64{}
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		contents, err := io.ReadAll(archive)
		if err != nil {
			t.Fatal(err)
		}
		entries[header.Name] = string(contents)
		modes[header.Name] = header.Mode
	}
	prefix := "blur-linux-amd64/"
	if entries[prefix+"blur"] != "binary fixture" || entries[prefix+"blur.svg"] != "icon fixture" {
		t.Fatalf("Linux archive is missing binary or icon: %v", entries)
	}
	if modes[prefix+"blur"] != 0o755 {
		t.Fatalf("Linux executable mode = %#o, want 0755", modes[prefix+"blur"])
	}
	if !strings.Contains(entries[prefix+"blur.desktop"], "Exec=blur\n") || !strings.Contains(entries[prefix+"blur.desktop"], "Icon=blur\n") {
		t.Fatalf("desktop entry is not launchable: %q", entries[prefix+"blur.desktop"])
	}
	for _, name := range []string{"README.md", "LICENSE", "SECURITY.md"} {
		if entries[prefix+name] == "" {
			t.Errorf("Linux archive missing %s", name)
		}
	}
}

func TestPackagePortableRejectsUnsupportedTargetsAndSymlinkBinary(t *testing.T) {
	root, binary := packageFixture(t)
	output := filepath.Join(t.TempDir(), "blur.zip")
	for _, target := range [][2]string{{"windows", "arm64"}, {"linux", "arm64"}, {"windows", "386"}, {"linux", "386"}} {
		suffix := ".zip"
		if target[0] == "linux" {
			suffix = ".tar.gz"
		}
		unsupportedOutput := filepath.Join(t.TempDir(), "blur"+suffix)
		if err := packagePortable(root, binary, "", unsupportedOutput, target[0], target[1]); err == nil {
			t.Fatalf("unsupported %s/%s package succeeded", target[0], target[1])
		}
	}
	link := filepath.Join(t.TempDir(), "blur")
	if err := os.Symlink(binary, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := packagePortable(root, link, "", output, "linux", "amd64"); err == nil {
		t.Fatal("symlink binary package succeeded")
	}
}

func TestPackagePortableNeverReplacesItsInputBinary(t *testing.T) {
	root := t.TempDir()
	for name, contents := range map[string]string{"README.md": "readme", "LICENSE": "license", "SECURITY.md": "security"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "blur.svg"), []byte("icon"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "blur.tar.gz")
	if err := os.WriteFile(binary, []byte("binary fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := packagePortable(root, binary, "", binary, "linux", "amd64"); err == nil {
		t.Fatal("packager replaced its own input binary")
	}
	contents, err := os.ReadFile(binary)
	if err != nil || string(contents) != "binary fixture" {
		t.Fatalf("input binary changed to %q, err=%v", contents, err)
	}
}

func TestPackagePortableRequiresDistinctWindowsGUIAndMCPBinaries(t *testing.T) {
	root, binary := packageFixture(t)
	output := filepath.Join(t.TempDir(), "blur-windows-amd64.zip")
	if err := packagePortable(root, binary, binary, output, "windows", "amd64"); err == nil {
		t.Fatal("packager accepted one console/gui executable for both Windows roles")
	}
}

func TestPackagePortableWritesDeterministicArchives(t *testing.T) {
	root, binary := packageFixture(t)
	first := filepath.Join(t.TempDir(), "first.tar.gz")
	second := filepath.Join(t.TempDir(), "second.tar.gz")
	if err := packagePortable(root, binary, "", first, "linux", "amd64"); err != nil {
		t.Fatal(err)
	}
	if err := packagePortable(root, binary, "", second, "linux", "amd64"); err != nil {
		t.Fatal(err)
	}
	a, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("identical inputs produced different portable archives")
	}
	info, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("portable archive permissions = %#o, want 0644 for distribution", got)
	}
}

func packageFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	for name, contents := range map[string]string{
		"README.md": "readme fixture", "LICENSE": "license fixture", "SECURITY.md": "security fixture",
		filepath.Join("assets", "blur.svg"): "icon fixture",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(t.TempDir(), "blur")
	if err := os.WriteFile(binary, []byte("binary fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root, binary
}
