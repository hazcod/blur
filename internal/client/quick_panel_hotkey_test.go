package client

import (
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/hotkey"
)

func TestQuickAskShortcutRowsKeepEveryOptionInOrder(t *testing.T) {
	options := hotkey.Options()
	rows := quickAskShortcutRows(options, quickAskShortcutsPerRow)
	var flat []hotkey.Option
	for _, row := range rows {
		if len(row) == 0 || len(row) > quickAskShortcutsPerRow {
			t.Fatalf("row size %d out of range", len(row))
		}
		flat = append(flat, row...)
	}
	if len(flat) != len(options) {
		t.Fatalf("got %d options, want %d", len(flat), len(options))
	}
	for i := range flat {
		if flat[i] != options[i] {
			t.Fatalf("option %d = %v, want %v", i, flat[i], options[i])
		}
	}
	if got := quickAskShortcutRows(nil, 3); len(got) != 0 {
		t.Fatalf("empty input gave %v", got)
	}
	if got := quickAskShortcutRows(options, 0); len(got) != len(options) {
		t.Fatalf("size 0 should clamp to 1, got %d rows", len(got))
	}
}

func TestQuickAskShortcutNote(t *testing.T) {
	if got := quickAskShortcutNote(""); !strings.Contains(got, "No shortcut") {
		t.Fatalf("off note = %q", got)
	}
	if got := quickAskShortcutNote("alt+space"); !strings.Contains(got, "⌥Space") {
		t.Fatalf("on note = %q", got)
	}
}
