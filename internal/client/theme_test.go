package client

import (
	"image/color"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/golang-gui/goui/style"
	"github.com/golang-gui/goui/theme/modern"
)

func relativeLuminance(c color.NRGBA) float64 {
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= .04045 {
			return s / 12.92
		}
		return math.Pow((s+.055)/1.055, 2.4)
	}
	return .2126*lin(c.R) + .7152*lin(c.G) + .0722*lin(c.B)
}

func contrastRatio(a, b color.NRGBA) float64 {
	x, y := relativeLuminance(a), relativeLuminance(b)
	return (math.Max(x, y) + .05) / (math.Min(x, y) + .05)
}

func TestThemeTokensMatchDesignAndMeetTextContrast(t *testing.T) {
	dark, light := themePalette(true), themePalette(false)
	if dark.window != rgb(0x15161A) || dark.rail != rgb(0x1B1D22) || dark.surface != rgb(0x202329) || dark.hairline != rgb(0x2E3139) {
		t.Fatalf("dark surfaces drifted from the design tokens: %#v", dark)
	}
	if light.window != rgb(0xECEDEF) || light.rail != rgb(0xE2E4E8) || light.surface != rgb(0xFFFFFF) || light.hairline != rgb(0xD5D8DE) {
		t.Fatalf("light surfaces drifted from the design tokens: %#v", light)
	}
	if dark.amber != light.amber {
		t.Fatal("amber is the single accent and must be the same in both schemes")
	}
	for name, p := range map[string]palette{"dark": dark, "light": light} {
		for _, bg := range []struct {
			name  string
			color color.NRGBA
		}{{"window", p.window}, {"rail", p.rail}, {"surface", p.surface}} {
			if got := contrastRatio(p.ink, bg.color); got < 7 {
				t.Errorf("%s ink on %s contrast = %.2f, want at least 7", name, bg.name, got)
			}
			if got := contrastRatio(p.inkMuted, bg.color); got < 4.5 {
				t.Errorf("%s ink-muted on %s contrast = %.2f, want at least 4.5", name, bg.name, got)
			}
		}
		if got := contrastRatio(p.onAmber, p.amber); got < 7 {
			t.Errorf("%s on-amber contrast = %.2f, want at least 7", name, got)
		}
		if got := contrastRatio(p.onAmber, p.amberHover); got < 4.5 {
			t.Errorf("%s on-amber over hover fill contrast = %.2f, want at least 4.5", name, got)
		}
	}
}

func TestThemeSheetStylesNavigationAndButtons(t *testing.T) {
	for _, dark := range []bool{true, false} {
		p := themePalette(dark)
		sheet := style.Sheet(themeRules(modern.Options{Dark: dark, FontFamily: "Test Sans", FontSize: 13})...)
		bg := func(name string) color.Color {
			c, _ := sheet.Resolve(style.Sel{Name: name}).BackgroundColor()
			return c
		}
		if got := bg(styleNavMarkerOn); got != p.amber {
			t.Errorf("dark=%v selected marker = %v, want amber", dark, got)
		}
		if got := bg(styleNavMarker); got != color.Color(color.Transparent) {
			t.Errorf("dark=%v unselected marker = %v, want transparent spacer", dark, got)
		}
		if got := bg(modern.Primary); got != p.amber {
			t.Errorf("dark=%v primary fill = %v, want amber", dark, got)
		}
		if got := bg(styleRail); got != p.rail {
			t.Errorf("dark=%v rail = %v, want rail token", dark, got)
		}
		if got := bg(styleHairline); got != p.hairline {
			t.Errorf("dark=%v hairline = %v", dark, got)
		}
		hover := sheet.Resolve(style.Sel{Name: modern.Flat, State: style.Hovered})
		if width, _ := hover.BorderWidth(); width != 1 {
			t.Errorf("dark=%v flat hover border = %v, want 1", dark, width)
		}
		rest := sheet.Resolve(style.Sel{Name: modern.Flat})
		if width, _ := rest.BorderWidth(); width != 0 {
			t.Errorf("dark=%v flat rest border = %v, want none", dark, width)
		}
		card := sheet.Resolve(style.Sel{Name: modern.Card})
		if radius, _ := card.Radius(); radius != surfaceRadius {
			t.Errorf("dark=%v card radius = %v", dark, radius)
		}
		if shadow, _ := card.Shadow(); shadow != (style.Shadow{}) {
			t.Errorf("dark=%v card must not cast a shadow", dark)
		}
		if family, _ := sheet.Resolve(style.Sel{Name: styleSectionTitle}).FontFamily(); family != "Test Sans" {
			t.Errorf("dark=%v section titles must use the body face, got %q", dark, family)
		}
		if size, _ := sheet.Resolve(style.Sel{Name: styleDayTitle}).FontSize(); size != 30 {
			t.Errorf("dark=%v day title size = %v", dark, size)
		}
		focus := sheet.Resolve(style.Sel{Name: "text-input", State: style.Focused})
		if c, _ := focus.BorderColor(); c != p.amber {
			t.Errorf("dark=%v focused input border = %v, want amber", dark, c)
		}
	}
}

func TestDesktopViewsUseNamedTokensOnly(t *testing.T) {
	for _, file := range []string{"desktop.go", "quick_panel.go"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"color.NRGBA{", "color.RGBA{", " · "} {
			if file == "quick_panel.go" && banned == "color.NRGBA{" {
				continue // the notch backdrop and composer are fixed absolute black
			}
			if strings.Contains(string(data), banned) {
				t.Errorf("%s contains %q; colour goes through theme tokens and copy avoids joiner dots", file, banned)
			}
		}
	}
}

func TestShiftedDayStepsByCalendarDay(t *testing.T) {
	for _, test := range []struct {
		in    string
		delta int
		want  string
		ok    bool
	}{
		{"2026-11-11", -1, "2026-11-10", true},
		{"2026-11-30", 1, "2026-12-01", true},
		{"2026-03-01", -1, "2026-02-28", true},
		{"nope", 1, "", false},
	} {
		got, ok := shiftedDay(test.in, test.delta)
		if got != test.want || ok != test.ok {
			t.Errorf("shiftedDay(%q, %d) = %q, %v; want %q, %v", test.in, test.delta, got, ok, test.want, test.ok)
		}
	}
}

func TestCaptureLightLabelNamesEveryState(t *testing.T) {
	for _, test := range []struct {
		state    captureLightState
		starting bool
		want     string
	}{
		{captureLightOn, false, "Capturing"},
		{captureLightPaused, false, "Paused"},
		{captureLightPending, false, "Needs access"},
		{captureLightPending, true, "Starting"},
	} {
		if got := captureLightLabel(test.state, test.starting); got != test.want {
			t.Errorf("captureLightLabel(%v, %v) = %q, want %q", test.state, test.starting, got, test.want)
		}
	}
}
