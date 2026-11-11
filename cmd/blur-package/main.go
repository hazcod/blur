package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxPackagedBinaryBytes = 512 << 20

var errInvalidPackage = errors.New("blur-package: invalid package input")

type archiveEntry struct {
	name   string
	data   []byte
	source string
	size   int64
	mode   os.FileMode
}

func main() {
	var repoRoot, binaryPath, mcpBinaryPath, outputPath, targetOS, targetArch string
	flag.StringVar(&repoRoot, "root", ".", "repository root containing README.md, LICENSE, and SECURITY.md")
	flag.StringVar(&binaryPath, "binary", "", "prebuilt target binary")
	flag.StringVar(&mcpBinaryPath, "mcp-binary", "", "console-subsystem MCP companion binary (Windows only)")
	flag.StringVar(&outputPath, "output", "", "output .zip or .tar.gz archive")
	flag.StringVar(&targetOS, "os", "", "target operating system (windows or linux)")
	flag.StringVar(&targetArch, "arch", "", "target architecture (amd64 or arm64)")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal(errInvalidPackage)
	}
	if err := packagePortable(repoRoot, binaryPath, mcpBinaryPath, outputPath, targetOS, targetArch); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	slog.Error("portable package failed", "error", err)
	os.Exit(2)
}

func packagePortable(repoRoot, binaryPath, mcpBinaryPath, outputPath, targetOS, targetArch string) error {
	archiveFormat, executable, err := portableTarget(targetOS, targetArch)
	if err != nil || repoRoot == "" || binaryPath == "" || outputPath == "" ||
		(targetOS == "windows" && mcpBinaryPath == "") || (targetOS != "windows" && mcpBinaryPath != "") {
		return errInvalidPackage
	}
	if (archiveFormat == "zip" && !strings.HasSuffix(outputPath, ".zip")) || (archiveFormat == "tar.gz" && !strings.HasSuffix(outputPath, ".tar.gz")) {
		return errInvalidPackage
	}
	binary, err := inspectPackagedBinary(binaryPath)
	if err != nil {
		return err
	}
	if packageOutputIsBinary(binaryPath, outputPath) {
		return errInvalidPackage
	}
	var mcpBinary archiveEntry
	if targetOS == "windows" {
		mcpBinary, err = inspectPackagedBinary(mcpBinaryPath)
		if err != nil || packageOutputIsBinary(mcpBinaryPath, outputPath) || packageOutputIsBinary(binaryPath, mcpBinaryPath) {
			return errInvalidPackage
		}
		mcpBinary.name, mcpBinary.mode = "blur-mcp.exe", 0o755
	}
	binary.name, binary.mode = executable, 0o755
	entries := []archiveEntry{binary}
	if targetOS == "windows" {
		binary.name = "Blur.exe"
		entries[0] = binary
		entries = append(entries, mcpBinary)
	}
	for _, name := range []string{"README.md", "LICENSE", "SECURITY.md"} {
		data, err := readRepositoryFile(repoRoot, name, 16<<20)
		if err != nil {
			return err
		}
		entries = append(entries, archiveEntry{name: name, data: data, size: int64(len(data)), mode: 0o644})
	}
	if targetOS == "linux" {
		icon, err := readRepositoryFile(repoRoot, filepath.Join("assets", "blur.svg"), 2<<20)
		if err != nil {
			return err
		}
		entries = append(entries,
			archiveEntry{name: "blur.desktop", data: []byte(linuxDesktopEntry), size: int64(len(linuxDesktopEntry)), mode: 0o644},
			archiveEntry{name: "blur.svg", data: icon, size: int64(len(icon)), mode: 0o644},
		)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("blur-package: create output directory: %w", err)
	}
	temporary, err := os.CreateTemp(outputDir, ".blur-package-*")
	if err != nil {
		return fmt.Errorf("blur-package: create temporary archive: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if archiveFormat == "zip" {
		err = writeZip(temporary, entries, targetOS, targetArch)
	} else {
		err = writeTarGzip(temporary, entries, targetOS, targetArch)
	}
	if err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("blur-package: set archive permissions: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("blur-package: sync archive: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("blur-package: close archive: %w", err)
	}
	if err := os.Rename(temporaryPath, outputPath); err != nil {
		return fmt.Errorf("blur-package: publish archive: %w", err)
	}
	return nil
}

const linuxDesktopEntry = `[Desktop Entry]
Name=Blur
Comment=Private, on-device work memory
Exec=blur
Icon=blur
Terminal=false
Type=Application
Categories=Utility;Office;
`

func portableTarget(targetOS, targetArch string) (format, executable string, err error) {
	switch {
	case targetOS == "windows" && targetArch == "amd64":
		return "zip", "Blur.exe", nil
	case targetOS == "linux" && (targetArch == "amd64" || targetArch == "arm64"):
		return "tar.gz", "blur", nil
	default:
		return "", "", errInvalidPackage
	}
}

func inspectPackagedBinary(path string) (archiveEntry, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxPackagedBinaryBytes {
		return archiveEntry{}, errInvalidPackage
	}
	return archiveEntry{source: path, size: info.Size()}, nil
}

func readRepositoryFile(root, name string, limit int64) ([]byte, error) {
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		return nil, errInvalidPackage
	}
	data, err := os.ReadFile(path)
	if err != nil || int64(len(data)) != info.Size() {
		return nil, errInvalidPackage
	}
	return data, nil
}

func packageOutputIsBinary(binaryPath, outputPath string) bool {
	binaryAbsolute, binaryErr := filepath.Abs(binaryPath)
	outputAbsolute, outputErr := filepath.Abs(outputPath)
	if binaryErr != nil || outputErr != nil {
		return true
	}
	if filepath.Clean(binaryAbsolute) == filepath.Clean(outputAbsolute) {
		return true
	}
	binaryInfo, binaryErr := os.Stat(binaryPath)
	outputInfo, outputErr := os.Stat(outputPath)
	return binaryErr == nil && outputErr == nil && os.SameFile(binaryInfo, outputInfo)
}

func writeEntry(output io.Writer, entry archiveEntry) error {
	if entry.source == "" {
		_, err := output.Write(entry.data)
		return err
	}
	file, err := os.Open(entry.source)
	if err != nil {
		return errInvalidPackage
	}
	defer file.Close()
	info, err := file.Stat()
	pathInfo, pathErr := os.Lstat(entry.source)
	if err != nil || pathErr != nil || !info.Mode().IsRegular() || !pathInfo.Mode().IsRegular() || !os.SameFile(info, pathInfo) || info.Size() != entry.size {
		return errInvalidPackage
	}
	written, err := io.CopyN(output, file, entry.size)
	if err != nil || written != entry.size {
		return errInvalidPackage
	}
	return nil
}

func writeZip(output io.Writer, entries []archiveEntry, targetOS, targetArch string) error {
	writer := zip.NewWriter(output)
	prefix := "blur-" + targetOS + "-" + targetArch + "/"
	for _, entry := range entries {
		header := &zip.FileHeader{Name: prefix + filepath.ToSlash(entry.name), Method: zip.Deflate}
		header.SetModTime(time.Unix(0, 0).UTC())
		header.SetMode(entry.mode)
		file, err := writer.CreateHeader(header)
		if err != nil {
			_ = writer.Close()
			return fmt.Errorf("blur-package: create zip entry: %w", err)
		}
		if err := writeEntry(file, entry); err != nil {
			_ = writer.Close()
			return fmt.Errorf("blur-package: write zip entry: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("blur-package: close zip: %w", err)
	}
	return nil
}

func writeTarGzip(output io.Writer, entries []archiveEntry, targetOS, targetArch string) error {
	gz := gzip.NewWriter(output)
	gz.Header.ModTime = time.Unix(0, 0).UTC()
	gz.Header.OS = 255
	writer := tar.NewWriter(gz)
	prefix := "blur-" + targetOS + "-" + targetArch + "/"
	for _, entry := range entries {
		header := &tar.Header{
			Name: prefix + filepath.ToSlash(entry.name), Mode: int64(entry.mode.Perm()), Size: entry.size,
			ModTime: time.Unix(0, 0).UTC(), Typeflag: tar.TypeReg, Uid: 0, Gid: 0, Format: tar.FormatUSTAR,
		}
		if err := writer.WriteHeader(header); err != nil {
			_ = writer.Close()
			_ = gz.Close()
			return fmt.Errorf("blur-package: create tar entry: %w", err)
		}
		if err := writeEntry(writer, entry); err != nil {
			_ = writer.Close()
			_ = gz.Close()
			return fmt.Errorf("blur-package: write tar entry: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		_ = gz.Close()
		return fmt.Errorf("blur-package: close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("blur-package: close gzip: %w", err)
	}
	return nil
}
