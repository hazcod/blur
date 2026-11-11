package client

import (
	"image"
	"image/color"
	"reflect"
	"testing"
)

func TestUISnapshotInertWhenEnvUnset(t *testing.T) {
	t.Setenv(uiSnapshotEnv, "")
	if dir := uiSnapshotDir(); dir != "" {
		t.Fatalf("snapshot dir = %q, want inert", dir)
	}
	(*desktop)(nil).startUISnapshotIfRequested()
	d := &desktop{}
	d.startUISnapshotIfRequested() // must not touch a nil app
}

func TestUISnapshotStoreIsIsolated(t *testing.T) {
	t.Setenv(uiSnapshotEnv, "")
	app, err := openUISnapshotApp(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUISnapshotQuickStatesAreSyntheticAndDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, state := range uiSnapshotQuickStates() {
		if seen[state.name] {
			t.Fatalf("duplicate state %q", state.name)
		}
		seen[state.name] = true
		d := &desktop{}
		d.resetQuickSnapshotState()
		state.apply(d)
		switch state.name {
		case "typing":
			if d.quickInput == "" {
				t.Fatal("typing state has no input")
			}
		case "pending":
			if !d.quickAskPending || !d.askLoading || d.askQuestion == "" {
				t.Fatal("pending state incomplete")
			}
		case "answer":
			if len(d.quickConversation) != 1 || d.quickConversation[0].sources != 3 || d.quickConversation[0].general || !d.quickPanelExpanded() {
				t.Fatal("answer state incomplete")
			}
		case "preview":
			if !d.quickPreview || len(quickPreviewGroups(d.today.Snapshot, d.hourlySummaries)) == 0 {
				t.Fatal("preview state incomplete")
			}
		}
	}
	for _, want := range []string{"empty", "typing", "pending", "answer", "note-saved", "preview"} {
		if !seen[want] {
			t.Fatalf("missing state %q", want)
		}
	}
}

func TestIsUniformImage(t *testing.T) {
	white := image.NewNRGBA(image.Rect(0, 0, 100, 80))
	for i := range white.Pix {
		white.Pix[i] = 0xff
	}
	if !isUniformImage(white) {
		t.Fatal("all-white image should be uniform")
	}
	painted := image.NewNRGBA(image.Rect(0, 0, 100, 80))
	for i := range painted.Pix {
		painted.Pix[i] = 0xff
	}
	painted.Set(99, 79, color.NRGBA{A: 0xff}) // a corner sample differs
	if isUniformImage(painted) {
		t.Fatal("image with a differing sample is not uniform")
	}
	if isUniformImage(nil) || isUniformImage(image.NewNRGBA(image.Rect(0, 0, 0, 0))) {
		t.Fatal("nil or empty image is not reported uniform")
	}
}

func TestUISnapshotSettingsGroupsCoverEveryGroupButCapture(t *testing.T) {
	var files []string
	seen := map[string]bool{settingsGroupCapture: true}
	for _, group := range uiSnapshotSettingsGroups() {
		files = append(files, group.file)
		seen[group.name] = true
	}
	want := []string{"Settings-ai", "Settings-quick-ask", "Settings-connections", "Settings-memory", "Settings-privacy"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("settings snapshots = %v, want %v", files, want)
	}
	for _, group := range settingsGroups() {
		if !seen[group] {
			t.Fatalf("settings group %q has no snapshot", group)
		}
	}
}
