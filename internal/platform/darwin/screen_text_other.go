//go:build !darwin

package darwin

func OpenScreenTextBridge(string) (*ScreenTextBridge, error) { return nil, ErrScreenTextUnavailable }
