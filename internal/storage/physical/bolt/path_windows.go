//go:build windows

package bolt

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsSecureDirectory struct {
	handle windows.Handle
}

func openSecureDirectory(path string) (secureDirectory, error) {
	return openDirectoryPath(path, true)
}

func openDirectoryPath(path string, requirePrivate bool) (*windowsSecureDirectory, error) {
	_, parts, root, err := windowsPathParts(path)
	if err != nil {
		return nil, err
	}
	root16, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return nil, ErrInvalid
	}
	current, err := windows.CreateFile(root16,
		windows.FILE_LIST_DIRECTORY|windows.FILE_TRAVERSE|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	if err := verifyWindowsObject(current, true, false); err != nil {
		_ = windows.CloseHandle(current)
		return nil, err
	}
	for _, part := range parts {
		next, openErr := openRelativeWindows(current, part,
			windows.FILE_LIST_DIRECTORY|windows.FILE_TRAVERSE|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE,
			windows.FILE_OPEN,
			windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
			nil)
		_ = windows.CloseHandle(current)
		if openErr != nil {
			if isWindowsUnsafePathError(openErr) {
				return nil, ErrInvalid
			}
			return nil, ErrUnavailable
		}
		if verifyErr := verifyWindowsObject(next, true, false); verifyErr != nil {
			_ = windows.CloseHandle(next)
			return nil, verifyErr
		}
		current = next
	}
	if requirePrivate {
		if err := verifyWindowsACL(current); err != nil {
			_ = windows.CloseHandle(current)
			return nil, err
		}
	}
	return &windowsSecureDirectory{handle: current}, nil
}

func (directory *windowsSecureDirectory) OpenFile(name string, flag int, mode os.FileMode) (*os.File, error) {
	if directory == nil || directory.handle == 0 || !validWindowsBaseName(name) {
		return nil, ErrInvalid
	}
	access := uint32(windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL)
	if flag&os.O_WRONLY == 0 {
		access |= windows.FILE_READ_DATA
	}
	if flag&os.O_WRONLY != 0 || flag&os.O_RDWR != 0 {
		access |= windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_ATTRIBUTES
	}
	disposition := uint32(windows.FILE_OPEN)
	var descriptor *windows.SECURITY_DESCRIPTOR
	if flag&os.O_CREATE != 0 {
		disposition = windows.FILE_OPEN_IF
		var err error
		descriptor, err = currentUserSecurityDescriptor()
		if err != nil {
			return nil, ErrUnavailable
		}
	}
	options := uint32(windows.FILE_NON_DIRECTORY_FILE | windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	handle, err := openRelativeWindows(directory.handle, name, access|windows.SYNCHRONIZE, disposition, options, descriptor)
	if err != nil {
		if isWindowsUnsafePathError(err) {
			return nil, ErrInvalid
		}
		return nil, ErrUnavailable
	}
	if err := verifyWindowsObject(handle, false, true); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return os.NewFile(uintptr(handle), name), nil
}

func (directory *windowsSecureDirectory) Close() error {
	if directory == nil || directory.handle == 0 {
		return nil
	}
	return windows.CloseHandle(directory.handle)
}

func createPrivateDirectory(path string) error {
	_, parts, _, err := windowsPathParts(path)
	if err != nil || len(parts) == 0 {
		return ErrInvalid
	}
	name := parts[len(parts)-1]
	parentPath := filepath.Dir(path)
	parent, err := openDirectoryPath(parentPath, false)
	if err != nil {
		return err
	}
	defer parent.Close()
	descriptor, err := currentUserSecurityDescriptor()
	if err != nil {
		return ErrUnavailable
	}
	created, err := openRelativeWindows(parent.handle, name,
		windows.FILE_LIST_DIRECTORY|windows.FILE_TRAVERSE|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.WRITE_DAC|windows.SYNCHRONIZE,
		windows.FILE_OPEN_IF,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		descriptor)
	if err != nil {
		if isWindowsUnsafePathError(err) {
			return ErrInvalid
		}
		return ErrUnavailable
	}
	defer windows.CloseHandle(created)
	if err := verifyWindowsObject(created, true, true); err != nil {
		return err
	}
	if err := verifyWindowsACL(created); err != nil {
		return err
	}
	return nil
}

func windowsPathParts(path string) (absolute string, parts []string, root string, err error) {
	if path == "" {
		return "", nil, "", ErrInvalid
	}
	absolute, err = filepath.Abs(path)
	if err != nil {
		return "", nil, "", ErrInvalid
	}
	absolute = filepath.Clean(absolute)
	volume := filepath.VolumeName(absolute)
	if len(volume) != 2 || volume[1] != ':' || !filepath.IsAbs(absolute) || strings.HasPrefix(absolute, `\\`) {
		return "", nil, "", ErrInvalid
	}
	root = volume + string(filepath.Separator)
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", nil, "", ErrInvalid
	}
	if relative == "." {
		return absolute, nil, root, nil
	}
	for _, part := range strings.FieldsFunc(relative, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == "" || part == "." || part == ".." {
			return "", nil, "", ErrInvalid
		}
		parts = append(parts, part)
	}
	return absolute, parts, root, nil
}

func validWindowsBaseName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\\:`)
}

func openRelativeWindows(parent windows.Handle, name string, access, disposition, options uint32, descriptor *windows.SECURITY_DESCRIPTOR) (windows.Handle, error) {
	if parent == 0 || !validWindowsBaseName(name) {
		return 0, ErrInvalid
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, ErrInvalid
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		Length:             uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory:      parent,
		ObjectName:         objectName,
		Attributes:         windows.OBJ_CASE_INSENSITIVE,
		SecurityDescriptor: descriptor,
	}
	var ioStatus windows.IO_STATUS_BLOCK
	var handle windows.Handle
	fileAttributes := uint32(windows.FILE_ATTRIBUTE_NORMAL)
	if options&windows.FILE_DIRECTORY_FILE != 0 {
		fileAttributes = windows.FILE_ATTRIBUTE_DIRECTORY
	}
	err = windows.NtCreateFile(&handle, access, &attributes, &ioStatus, nil,
		fileAttributes,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		disposition, options, 0, 0)
	if err != nil {
		return 0, err
	}
	return handle, nil
}

func isWindowsUnsafePathError(err error) bool {
	switch err {
	case windows.STATUS_FILE_IS_A_DIRECTORY,
		windows.STATUS_NOT_A_DIRECTORY,
		windows.STATUS_IO_REPARSE_TAG_INVALID,
		windows.STATUS_IO_REPARSE_TAG_MISMATCH,
		windows.STATUS_IO_REPARSE_DATA_INVALID,
		windows.STATUS_IO_REPARSE_TAG_NOT_HANDLED,
		windows.STATUS_REPARSE_POINT_NOT_RESOLVED,
		windows.STATUS_DIRECTORY_IS_A_REPARSE_POINT,
		windows.STATUS_REPARSE_POINT_ENCOUNTERED,
		windows.STATUS_STOPPED_ON_SYMLINK:
		return true
	default:
		return false
	}
}

func verifyWindowsObject(handle windows.Handle, wantDirectory, checkACL bool) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return ErrUnavailable
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != wantDirectory {
		return ErrInvalid
	}
	fileType, err := windows.GetFileType(handle)
	if err != nil || fileType != windows.FILE_TYPE_DISK {
		return ErrInvalid
	}
	if checkACL {
		return verifyWindowsACL(handle)
	}
	return nil
}

func currentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return nil, ErrUnavailable
	}
	return user.User.Sid, nil
}

func currentUserSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	sddl := "O:" + sid.String() + "D:P(A;;GA;;;" + sid.String() + ")"
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil || descriptor == nil {
		return nil, ErrUnavailable
	}
	return descriptor, nil
}

func verifyWindowsACL(handle windows.Handle) error {
	currentSID, err := currentUserSID()
	if err != nil {
		return ErrUnavailable
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || descriptor == nil {
		return ErrUnavailable
	}
	owner, ownerDefaulted, err := descriptor.Owner()
	if err != nil || owner == nil || ownerDefaulted || !owner.Equals(currentSID) {
		return ErrInvalid
	}
	dacl, daclDefaulted, err := descriptor.DACL()
	if err != nil || dacl == nil || daclDefaulted || dacl.AceCount != 1 {
		return ErrInvalid
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 || control&windows.SE_DACL_PROTECTED == 0 || control&windows.SE_DACL_DEFAULTED != 0 || control&(windows.SE_DACL_AUTO_INHERIT_REQ|windows.SE_DACL_AUTO_INHERITED) != 0 {
		return ErrInvalid
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 || ace.Mask != windows.GENERIC_ALL {
		return ErrInvalid
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !aceSID.IsValid() || !aceSID.Equals(currentSID) {
		return ErrInvalid
	}
	return nil
}
