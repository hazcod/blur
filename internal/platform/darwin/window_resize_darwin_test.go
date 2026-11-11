package darwin

import "testing"

func TestQuickPanelFrameOriginAnchorsTopEdge(t *testing.T) {
	screen := nsRect{X: 0, Y: 0, Width: 1512, Height: 982}
	for _, height := range []float64{80, 136, 520} {
		x, y := quickPanelFrameOrigin(screen, 520, height, 0)
		if x != 496 {
			t.Fatalf("height %v: x = %v, want 496 (centred)", height, x)
		}
		if top := y + height; top != 982 {
			t.Fatalf("height %v: top edge = %v, want flush with 982", height, top)
		}
	}
}

func TestQuickPanelFrameOriginOffsetDisplay(t *testing.T) {
	// A main display positioned away from the global origin keeps its own edge.
	screen := nsRect{X: -1440, Y: 120, Width: 1440, Height: 900}
	x, y := quickPanelFrameOrigin(screen, 820, 520, 0)
	if x != -1440+310 {
		t.Fatalf("x = %v", x)
	}
	if y+520 != 1020 {
		t.Fatalf("top = %v, want 1020", y+520)
	}
	// A gap lowers the panel by exactly that many points.
	_, y = quickPanelFrameOrigin(screen, 820, 520, 8)
	if y+520 != 1012 {
		t.Fatalf("gap top = %v, want 1012", y+520)
	}
}

func TestShouldAnimateWindowFrame(t *testing.T) {
	a := nsRect{X: 496, Y: 846, Width: 520, Height: 136}
	b := nsRect{X: 346, Y: 462, Width: 820, Height: 520}
	cases := []struct {
		name            string
		reduce, visible bool
		cur, target     nsRect
		want            bool
	}{
		{"changed visible", false, true, a, b, true},
		{"reduce motion", true, true, a, b, false},
		{"hidden", false, false, a, b, false},
		{"unchanged", false, true, a, a, false},
		{"sub-point jitter", false, true, a, nsRect{X: a.X + 0.2, Y: a.Y, Width: a.Width, Height: a.Height - 0.3}, false},
	}
	for _, tc := range cases {
		if got := shouldAnimateWindowFrame(tc.reduce, tc.visible, tc.cur, tc.target); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestQuickPanelPaletteMatchesHex(t *testing.T) {
	if quickPanelSurfaceRGB != (nsRGB{27.0 / 255, 29.0 / 255, 34.0 / 255}) || quickPanelSurfaceHex != "#1B1D22" {
		t.Fatal("surface colour drifted from #1B1D22")
	}
	if quickPanelHairlineRGB != (nsRGB{46.0 / 255, 49.0 / 255, 57.0 / 255}) || quickPanelHairlineHex != "#2E3139" {
		t.Fatal("hairline colour drifted from #2E3139")
	}
}

func TestValidWindowContentSizeBounds(t *testing.T) {
	if !validWindowContentSize(520, 80) || validWindowContentSize(0, 80) || validWindowContentSize(520, 2000) {
		t.Fatal("unexpected size validation")
	}
}
