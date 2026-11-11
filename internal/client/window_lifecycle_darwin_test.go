//go:build darwin

package client

import (
	"errors"
	"testing"
)

func TestClosingMainWindowHidesItWithoutAllowingApplicationClose(t *testing.T) {
	allow := true
	hidden := false
	err := keepDesktopRunningOnWindowClose(&allow, func() error {
		hidden = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if allow {
		t.Fatal("window close was not vetoed")
	}
	if !hidden {
		t.Fatal("window was not hidden")
	}
}

func TestClosingMainWindowRemainsOpenWhenHideFails(t *testing.T) {
	allow := true
	wantErr := errors.New("hide failed")
	err := keepDesktopRunningOnWindowClose(&allow, func() error { return wantErr })
	if err != wantErr {
		t.Fatalf("hide error = %v, want %v", err, wantErr)
	}
	if allow {
		t.Fatal("window was allowed to close after hide failed")
	}
}
