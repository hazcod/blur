//go:build !darwin

package darwin

// RunningApplications is unavailable outside macOS.
func RunningApplications() []Application { return nil }

// ChooseApplication is unavailable outside macOS.
func ChooseApplication() (Application, error) {
	return Application{}, ErrApplicationPickerUnavailable
}

// ApplicationName is unavailable outside macOS.
func ApplicationName(string) (string, bool) { return "", false }
