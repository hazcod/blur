//go:build windows

package windows

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"github.com/hazcod/blur/internal/platform"
	"golang.org/x/sys/windows"
)

const (
	secretStoreEvidence = "dpapi_user"
	secretStoreMagic    = "BLURKEY1"
	maxSecretBytes      = 1 << 20
	maxSecretFileBytes  = maxSecretBytes + 64
)

var _ platform.SecretStore = (*SecretStore)(nil)

// SecretStore protects each value with Windows DPAPI in the current user's
// scope. Only the encrypted DPAPI blob is written to disk.
type SecretStore struct{ dir string }

func NewSecretStore() *SecretStore { return &SecretStore{} }

func (s *SecretStore) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	report := platform.CapabilityReport{
		ID: "secret_store", State: platform.Available,
		Reason: "dpapi_user_scope", Evidence: secretStoreEvidence,
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	return report, nil
}

func (s *SecretStore) Get(ctx context.Context, name string, maxBytes int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validSecretName(name) || maxBytes < 1 || maxBytes > maxSecretBytes {
		return nil, secretError(platform.SecretUnavailable)
	}
	if err := s.ensureDir(); err != nil {
		return nil, mapSecretError(err)
	}
	path := s.path(name)
	if err := secureFileACL(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, mapSecretError(err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, mapSecretError(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, mapSecretError(err)
	}
	if !info.Mode().IsRegular() || info.Size() < int64(len(secretStoreMagic)+1) || info.Size() > maxSecretFileBytes {
		return nil, secretError(platform.SecretCorrupt)
	}
	blob := make([]byte, int(info.Size()))
	if _, err := io.ReadFull(file, blob); err != nil {
		clear(blob)
		return nil, secretError(platform.SecretCorrupt)
	}
	if err := ctx.Err(); err != nil {
		clear(blob)
		return nil, err
	}
	if string(blob[:len(secretStoreMagic)]) != secretStoreMagic {
		clear(blob)
		return nil, secretError(platform.SecretCorrupt)
	}
	plain, err := unprotectDPAPI(blob[len(secretStoreMagic):])
	clear(blob)
	if err != nil {
		return nil, secretError(platform.SecretUnavailable)
	}
	if len(plain) < 1 || len(plain) > maxSecretBytes || len(plain) > maxBytes {
		clear(plain)
		return nil, secretError(platform.SecretCorrupt)
	}
	if err := ctx.Err(); err != nil {
		clear(plain)
		return nil, err
	}
	return plain, nil
}

func (s *SecretStore) Put(ctx context.Context, name string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validSecretName(name) || len(value) < 1 || len(value) > maxSecretBytes {
		return secretError(platform.SecretUnavailable)
	}
	if err := s.ensureDir(); err != nil {
		return mapSecretError(err)
	}
	protected, err := protectDPAPI(value)
	if err != nil {
		return secretError(platform.SecretUnavailable)
	}
	defer clear(protected)
	content := make([]byte, len(secretStoreMagic)+len(protected))
	copy(content, secretStoreMagic)
	copy(content[len(secretStoreMagic):], protected)
	defer clear(content)
	if err := ctx.Err(); err != nil {
		return err
	}
	target := s.path(name)
	if err := secureFileACL(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return mapSecretError(err)
	}
	tmp, err := os.CreateTemp(s.dir, ".key-*.tmp")
	if err != nil {
		return mapSecretError(err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := secureFileACL(tmpName); err != nil {
		_ = tmp.Close()
		return mapSecretError(err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return mapSecretError(err)
	}
	if err := ctx.Err(); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return mapSecretError(err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return mapSecretError(err)
	}
	if err := tmp.Close(); err != nil {
		return mapSecretError(err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(tmpName)
	if err != nil {
		return secretError(platform.SecretUnavailable)
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return secretError(platform.SecretUnavailable)
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return mapSecretError(err)
	}
	return nil
}

func (s *SecretStore) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validSecretName(name) {
		return secretError(platform.SecretUnavailable)
	}
	path := s.path(name)
	if err := secureFileACL(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return mapSecretError(err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return mapSecretError(err)
}

func (s *SecretStore) ensureDir() error {
	if s.dir == "" {
		base, err := os.UserConfigDir()
		if err != nil || base == "" {
			return fmt.Errorf("config path unavailable")
		}
		s.dir = filepath.Join(base, "Blur", "keys")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(s.dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("invalid secret store directory")
	}
	return secureDirectoryACL(s.dir)
}

func (s *SecretStore) path(name string) string {
	dir := s.dir
	if dir == "" {
		base, _ := os.UserConfigDir()
		dir = filepath.Join(base, "Blur", "keys")
	}
	digest := sha256.Sum256([]byte(name))
	return filepath.Join(dir, fmt.Sprintf("%x.key", digest[:]))
}

func validSecretName(name string) bool {
	return name != "" && len(name) <= 1024 && strings.TrimSpace(name) != ""
}

func protectDPAPI(value []byte) ([]byte, error) {
	input := windows.DataBlob{Size: uint32(len(value)), Data: &value[0]}
	var output windows.DataBlob
	err := windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	runtime.KeepAlive(value)
	if err != nil || output.Data == nil || output.Size == 0 || output.Size > maxSecretFileBytes {
		if output.Data != nil {
			zeroDPAPIBlob(&output)
		}
		if err == nil {
			err = errors.New("invalid DPAPI output")
		}
		return nil, err
	}
	defer zeroDPAPIBlob(&output)
	result := make([]byte, int(output.Size))
	copy(result, unsafe.Slice(output.Data, int(output.Size)))
	return result, nil
}

func unprotectDPAPI(value []byte) ([]byte, error) {
	if len(value) == 0 {
		return nil, errors.New("empty DPAPI input")
	}
	input := windows.DataBlob{Size: uint32(len(value)), Data: &value[0]}
	var output windows.DataBlob
	err := windows.CryptUnprotectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	runtime.KeepAlive(value)
	if err != nil || output.Data == nil || output.Size == 0 || output.Size > maxSecretBytes {
		if output.Data != nil {
			zeroDPAPIBlob(&output)
		}
		if err == nil {
			err = errors.New("invalid DPAPI output")
		}
		return nil, err
	}
	defer zeroDPAPIBlob(&output)
	result := make([]byte, int(output.Size))
	copy(result, unsafe.Slice(output.Data, int(output.Size)))
	return result, nil
}

func zeroDPAPIBlob(blob *windows.DataBlob) {
	if blob.Data != nil {
		clear(unsafe.Slice(blob.Data, int(blob.Size)))
		_, _ = windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(blob.Data))))
		blob.Data = nil
		blob.Size = 0
	}
}

func secureDirectoryACL(path string) error { return setPrivateACL(path, windows.SE_FILE_OBJECT) }
func secureFileACL(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("invalid secret file")
	}
	return setPrivateACL(path, windows.SE_FILE_OBJECT)
}

// RestrictPathToCurrentUser replaces a regular file or directory's ACL with a
// protected ACL granting full control only to the current user and SYSTEM.
func RestrictPathToCurrentUser(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return errors.New("invalid protected path")
	}
	return setPrivateACL(path, windows.SE_FILE_OBJECT)
}

func setPrivateACL(path string, objectType windows.SE_OBJECT_TYPE) error {
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || tokenUser == nil || tokenUser.User.Sid == nil {
		return errors.New("current user unavailable")
	}
	sidText := tokenUser.User.Sid.String()
	if sidText == "" {
		return errors.New("current user unavailable")
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + sidText + ")(A;OICI;FA;;;SY)")
	if err != nil {
		return errors.New("private ACL unavailable")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return errors.New("private ACL unavailable")
	}
	owner, err := windows.StringToSid(sidText)
	if err != nil {
		return errors.New("current user unavailable")
	}
	return windows.SetNamedSecurityInfo(path, objectType,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil)
}

func mapSecretError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return secretError(platform.SecretNotFound)
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
		return secretError(platform.SecretDenied)
	}
	return secretError(platform.SecretUnavailable)
}

func secretError(state platform.SecretState) error { return &platform.SecretStoreError{State: state} }
