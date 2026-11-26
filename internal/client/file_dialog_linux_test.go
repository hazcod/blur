//go:build linux

package client

import (
	"errors"
	"testing"

	"github.com/golang-gui/goui/ui"
)

func TestLinuxPickerResultMapsCancellation(t *testing.T) {
	if _, err := linuxPickerResult(nil, nil); !errors.Is(err, errLinuxPickerCancelled) {
		t.Fatalf("empty selection = %v, want cancellation", err)
	}
	if _, err := linuxPickerResult(nil, ui.ErrDialogBusy); !errors.Is(err, errLinuxPickerCancelled) {
		t.Fatalf("busy picker = %v, want cancellation", err)
	}
	failure := errors.New("portal failed")
	if _, err := linuxPickerResult(nil, failure); !errors.Is(err, failure) {
		t.Fatalf("picker failure = %v, want %v", err, failure)
	}
}

func TestLinuxPickerWithoutDesktopReportsUnavailable(t *testing.T) {
	var got error
	requestLinuxSavePath(nil, ui.DialogOptions{}, func(_ string, err error) { got = err })
	if !errors.Is(got, errDesktopAppUnavailable) {
		t.Fatalf("missing app = %v, want unavailable", got)
	}
}
