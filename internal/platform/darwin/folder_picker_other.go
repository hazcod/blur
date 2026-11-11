//go:build !darwin

package darwin

// ChooseFolder is unavailable outside macOS.
func ChooseFolder(string) (string, []byte, error) {
	return "", nil, ErrFolderPickerUnavailable
}

// ResolveBookmark is unavailable outside macOS.
func ResolveBookmark([]byte) (string, func(), bool, error) {
	return "", nil, false, ErrFolderPickerUnavailable
}

// CreateBookmark is unavailable outside macOS.
func CreateBookmark(string) ([]byte, error) {
	return nil, ErrFolderPickerUnavailable
}
