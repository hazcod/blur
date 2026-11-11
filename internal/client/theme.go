package client

// Design notes
//
// Blur's identity is quiet graphite surfaces, one warm amber status light,
// high-contrast type and restrained motion (docs/superpowers/specs/
// 2026-11-04-blur-design.md, "User interface"). Keep future passes inside
// these rules instead of adding new looks.
//
// Tokens (every colour goes through a named token; views never inline NRGBA):
//
//	token       dark     light    use
//	window      #15161A  #ECEDEF  window and content background
//	rail        #1B1D22  #E2E4E8  left navigation rail
//	surface     #202329  #FFFFFF  interactive objects only (editors, answers)
//	hairline    #2E3139  #D5D8DE  1 px dividers and borders
//	ink         #F1F1EE  #17181B  primary text
//	ink-muted   #A2A6AF  #5B6069  secondary text (4.5:1 on window)
//	ink-faint   #6E737D  #7C818B  decoration and disabled text only
//	amber       #E8A33C  #E8A33C  the one accent
//	amber-hover #F2B658  #D6912A  accent hover
//	on-amber    #1B1408  #1B1408  text on amber fills
//
// One-accent rule: amber appears only as the capture light, the selected-page
// marker, primary-button fills and the focused-input border. Status is never
// colour alone: the capture light always has a text label beside it.
//
// Type: body uses the system face at the user's base size. Only page and day
// titles use the body sans, bold and larger. Sentence case everywhere; no ad-hoc
// ALL CAPS, no " · " joiners in meta lines.
//
// Structure: no nested cards. Sections are a section-title, content, and a
// 1 px hairline. Real surfaces (surface fill, 1 px hairline, radius 10, no
// shadow) are reserved for objects the user interacts with: the quick-note
// editor, the Ask answer, the Library entry and settings groups.

import (
	"image/color"

	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/platform/graphics"
	"github.com/golang-gui/goui/style"
	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
)

// Layout metrics shared by the window shell and the style sheet.
const (
	railWidth       = 208
	navRowHeight    = 34
	navMarkerWidth  = 3
	navTextInset    = 21
	navTextTrailing = 12
	contentPadding  = 32
	contentMaxText  = 760
	controlHeight   = 32
	controlRadius   = 8
	surfaceRadius   = 10
	captureLightPx  = 10
)

// Named style classes owned by this file.
const (
	styleHairline       = "hairline"
	styleDanger         = "danger"
	styleArrow          = "day-arrow"
	styleChoiceOn       = "choice-on"
	styleSecondary      = "secondary"
	stylePageTitle      = "page-title"
	styleDayTitle       = "day-title"
	styleSectionTitle   = "section-title"
	styleMeta           = "meta"
	styleBrand          = "sidebar-brand"
	styleNav            = "nav"
	styleNavSelected    = "nav-selected"
	styleNavText        = "nav-text"
	styleNavTextCurrent = "nav-text-selected"
	styleNavMarker      = "nav-marker"
	styleNavMarkerOn    = "nav-marker-on"
	styleRail           = modern.Sidebar
)

// palette holds the colour tokens for one colour scheme.
type palette struct {
	window, rail, surface, raised, hairline color.NRGBA
	ink, inkMuted, inkFaint                 color.NRGBA
	amber, amberHover, amberPressed         color.NRGBA
	onAmber                                 color.NRGBA
	rowHover, rowPressed                    color.NRGBA
	danger                                  color.NRGBA
}

func rgb(hex uint32) color.NRGBA {
	return color.NRGBA{R: uint8(hex >> 16), G: uint8(hex >> 8), B: uint8(hex), A: 0xff}
}

// mixColor blends base toward tint by amount (0..1) into an opaque colour.
func mixColor(base, tint color.NRGBA, amount float64) color.NRGBA {
	ch := func(a, b uint8) uint8 {
		return uint8(float64(a)*(1-amount) + float64(b)*amount + 0.5)
	}
	return color.NRGBA{R: ch(base.R, tint.R), G: ch(base.G, tint.G), B: ch(base.B, tint.B), A: 0xff}
}

func themePalette(dark bool) palette {
	if dark {
		return palette{
			window: rgb(0x15161A), rail: rgb(0x1B1D22), surface: rgb(0x202329), raised: rgb(0x2A2D35), hairline: rgb(0x2E3139),
			ink: rgb(0xF1F1EE), inkMuted: rgb(0xA2A6AF), inkFaint: rgb(0x6E737D),
			amber: rgb(0xE8A33C), amberHover: rgb(0xF2B658), amberPressed: rgb(0xCF8E2E), onAmber: rgb(0x1B1408),
			rowHover: rgb(0x23262C), rowPressed: rgb(0x2A2D35),
			danger: rgb(0xD9644A),
		}
	}
	return palette{
		window: rgb(0xECEDEF), rail: rgb(0xE2E4E8), surface: rgb(0xFFFFFF), raised: rgb(0xF4F5F7), hairline: rgb(0xD5D8DE),
		ink: rgb(0x17181B), inkMuted: rgb(0x5B6069), inkFaint: rgb(0x7C818B),
		amber: rgb(0xE8A33C), amberHover: rgb(0xD6912A), amberPressed: rgb(0xC1821F), onAmber: rgb(0x1B1408),
		rowHover: rgb(0xD9DCE1), rowPressed: rgb(0xCDD1D8),
		danger: rgb(0xC24A30),
	}
}

// quickPalette is the Interact panel's palette: the panel always sits on a
// black notch backdrop, so it uses the dark tokens in either colour scheme.
func quickPalette() palette { return themePalette(true) }

// titleFontFamily returns the family for page and day titles. Titles use the
// same straight system sans as the body, set bold and larger; the earlier
// serif (Charter) read as too decorative.
func titleFontFamily(fallback string) string { return fallback }

// themeRules returns the complete stylesheet: the stock Modern rules with
// Blur's tokens layered on top. Overrides restate every state they care about
// because the Modern rules for hover/pressed/focus are more specific than a
// plain normal-state rule.
func themeRules(options modern.Options) []style.Rule {
	p := themePalette(options.Dark)
	base := options.FontSize
	if base <= 0 {
		base = 14
	}
	family := options.FontFamily
	serif := titleFontFamily(family)
	rules := modern.Rules(options)
	text := func(name string, c color.NRGBA) style.Rule {
		return style.Name(name).ForegroundColor(c).FontFamily(family).FontSize(base)
	}
	selection := mixColor(p.window, p.amber, .28)
	rules = append(rules,
		style.Name("window").BackgroundColor(p.window),
		style.Name("header-bar").BackgroundColor(p.window),
		text("label", p.ink),
		style.Name("label").State(style.Disabled).ForegroundColor(p.inkFaint),
		style.Name("label").Part("selection").BackgroundColor(selection),
		style.Name("label").Part("selection").State(style.Focused).BackgroundColor(selection),
		style.Name("label").Part("caret").ForegroundColor(p.ink),
		style.Name(modern.MutedText).ForegroundColor(p.inkMuted),
		style.Name(modern.MutedText).State(style.Disabled).ForegroundColor(p.inkFaint),
		style.Name(modern.PrimaryContent).ForegroundColor(p.onAmber),
		style.Name(modern.PrimaryContent).State(style.Disabled).ForegroundColor(p.inkFaint),

		// Typography.
		style.Name(stylePageTitle).ForegroundColor(p.ink).FontFamily(serif).FontSize(26).FontWeight(style.FontWeightBold),
		style.Name(styleDayTitle).ForegroundColor(p.ink).FontFamily(serif).FontSize(30).FontWeight(style.FontWeightBold),
		style.Name(styleArrow).ForegroundColor(p.ink).FontFamily(family).FontSize(base+9),
		style.Name(styleArrow).State(style.Disabled).ForegroundColor(p.inkFaint),
		style.Name(styleBrand).ForegroundColor(p.ink).FontFamily(serif).FontSize(20).FontWeight(style.FontWeightBold),
		style.Name(styleSectionTitle).ForegroundColor(p.ink).FontFamily(family).FontSize(base+2).FontWeight(style.FontWeightBold),
		style.Name(styleMeta).ForegroundColor(p.inkMuted).FontFamily(family).FontSize(base-1),

		// Surfaces.
		style.Name(styleHairline).BackgroundColor(p.hairline).BorderWidth(0).Radius(0),
		style.Name(styleRail).BackgroundColor(p.rail).BorderWidth(0).Radius(0).Shadow(style.Shadow{}),
		style.Name(modern.Card).BackgroundColor(p.surface).BorderColor(p.hairline).BorderWidth(1).Radius(surfaceRadius).Shadow(style.Shadow{}),

		// Buttons.
		style.Name(modern.Flat).ForegroundColor(p.ink).BackgroundColor(color.Transparent).BorderColor(p.hairline).BorderWidth(0).Radius(controlRadius),
		style.Name(modern.Flat).State(style.Hovered).BackgroundColor(color.Transparent).BorderColor(p.hairline).BorderWidth(1),
		style.Name(modern.Flat).State(style.Pressed).BackgroundColor(p.rowHover).BorderColor(p.hairline).BorderWidth(1),
		style.Name(modern.Flat).State(style.Disabled).ForegroundColor(p.inkFaint).BackgroundColor(color.Transparent).BorderWidth(0),
		style.Name(modern.Flat).Part("focus").State(style.Focused).BackgroundColor(color.Transparent).BorderWidth(0),
		style.Name(modern.Flat).Part("focus").State(style.FocusVisible).BackgroundColor(color.Transparent).BorderColor(p.amber).BorderWidth(2).Radius(controlRadius),

		style.Name(modern.Primary).ForegroundColor(p.onAmber).BackgroundColor(p.amber).BorderColor(p.amber).BorderWidth(0).Radius(controlRadius),
		style.Name(modern.Primary).State(style.Hovered).BackgroundColor(p.amberHover).BorderWidth(0),
		style.Name(modern.Primary).State(style.Pressed).BackgroundColor(p.amberPressed).BorderWidth(0),
		style.Name(modern.Primary).State(style.Disabled).ForegroundColor(p.inkFaint).BackgroundColor(p.raised).BorderWidth(0),
		style.Name(modern.Primary).Part("focus").State(style.Focused).BackgroundColor(color.Transparent).BorderWidth(0),
		style.Name(modern.Primary).Part("focus").State(style.FocusVisible).BackgroundColor(color.Transparent).BorderColor(p.ink).BorderWidth(2).Radius(controlRadius),

		style.Name(styleDanger).ForegroundColor(p.onAmber).BackgroundColor(p.danger).BorderWidth(0).Radius(controlRadius),
		style.Name(styleDanger).State(style.Hovered).BackgroundColor(mixColor(p.danger, p.ink, .12)).BorderWidth(0),
		style.Name(styleDanger).State(style.Pressed).BackgroundColor(mixColor(p.danger, p.window, .15)).BorderWidth(0),
		style.Name(styleDanger).State(style.Disabled).ForegroundColor(p.inkFaint).BackgroundColor(p.raised).BorderWidth(0),
		style.Name(styleDanger).Part("focus").State(style.Focused).BackgroundColor(color.Transparent).BorderWidth(0),
		style.Name(styleDanger).Part("focus").State(style.FocusVisible).BackgroundColor(color.Transparent).BorderColor(p.ink).BorderWidth(2).Radius(controlRadius),

		style.Name(styleSecondary).ForegroundColor(p.ink).BackgroundColor(p.surface).BorderColor(p.hairline).BorderWidth(1).Radius(controlRadius),
		style.Name(styleSecondary).State(style.Hovered).BackgroundColor(mixColor(p.surface, p.ink, .06)).BorderColor(p.inkFaint).BorderWidth(1),
		style.Name(styleSecondary).State(style.Pressed).BackgroundColor(p.rowPressed).BorderWidth(1),
		style.Name(styleSecondary).State(style.Disabled).ForegroundColor(p.inkFaint).BackgroundColor(p.surface).BorderColor(p.hairline).BorderWidth(1),
		style.Name(styleSecondary).Part("focus").State(style.Focused).BackgroundColor(color.Transparent).BorderWidth(0),
		style.Name(styleSecondary).Part("focus").State(style.FocusVisible).BackgroundColor(color.Transparent).BorderColor(p.amber).BorderWidth(2).Radius(controlRadius),

		style.Name(styleChoiceOn).ForegroundColor(p.ink).BackgroundColor(p.raised).BorderColor(p.hairline).BorderWidth(1).Radius(controlRadius),
		style.Name(styleChoiceOn).State(style.Hovered).BackgroundColor(p.raised).BorderColor(p.inkFaint).BorderWidth(1),
		style.Name(styleChoiceOn).State(style.Pressed).BackgroundColor(p.rowPressed).BorderWidth(1),
		style.Name(styleChoiceOn).State(style.Disabled).ForegroundColor(p.inkFaint).BackgroundColor(p.raised).BorderWidth(1),
		style.Name(styleChoiceOn).Part("focus").State(style.Focused).BackgroundColor(color.Transparent).BorderWidth(0),
		style.Name(styleChoiceOn).Part("focus").State(style.FocusVisible).BackgroundColor(color.Transparent).BorderColor(p.amber).BorderWidth(2).Radius(controlRadius),

		// Navigation rail rows: full-width, square, tinted on hover only.
		style.Name(styleNav).BackgroundColor(color.Transparent).BorderColor(color.Transparent).BorderWidth(0).Radius(0),
		style.Name(styleNav).State(style.Hovered).BackgroundColor(p.rowHover).BorderWidth(0),
		style.Name(styleNav).State(style.Pressed).BackgroundColor(p.rowPressed).BorderWidth(0),
		style.Name(styleNav).Part("focus").State(style.Focused).BackgroundColor(color.Transparent).BorderWidth(0),
		style.Name(styleNav).Part("focus").State(style.FocusVisible).BackgroundColor(color.Transparent).BorderColor(p.amber).BorderWidth(2).Radius(0),
		style.Name(styleNavSelected).BackgroundColor(color.Transparent).BorderColor(color.Transparent).BorderWidth(0).Radius(0),
		style.Name(styleNavSelected).State(style.Hovered).BackgroundColor(p.rowHover).BorderWidth(0),
		style.Name(styleNavSelected).State(style.Pressed).BackgroundColor(p.rowPressed).BorderWidth(0),
		style.Name(styleNavSelected).Part("focus").State(style.Focused).BackgroundColor(color.Transparent).BorderWidth(0),
		style.Name(styleNavSelected).Part("focus").State(style.FocusVisible).BackgroundColor(color.Transparent).BorderColor(p.amber).BorderWidth(2).Radius(0),
		style.Name(styleNavText).ForegroundColor(p.inkMuted).FontFamily(family).FontSize(base+1),
		style.Name(styleNavTextCurrent).ForegroundColor(p.ink).FontFamily(family).FontSize(base+1).FontWeight(style.FontWeightBold),
		style.Name(styleNavMarker).BackgroundColor(color.Transparent).BorderWidth(0).Radius(0),
		style.Name(styleNavMarkerOn).BackgroundColor(p.amber).BorderWidth(0).Radius(0),

		// Text entry: surface fill, hairline border, amber when focused.
		style.Name("text-input").ForegroundColor(p.ink).BackgroundColor(p.window).BorderColor(p.hairline).BorderWidth(1).Radius(controlRadius),
		style.Name("text-input").State(style.Focused).BorderColor(p.amber).BorderWidth(1),
		style.Name("text-input").State(style.Disabled).ForegroundColor(p.inkFaint).BackgroundColor(p.window),
		style.Name("text-input").Part("placeholder").ForegroundColor(p.inkMuted),
		style.Name("text-input").Part("placeholder").State(style.Disabled).ForegroundColor(p.inkFaint),
		style.Name("text-input").Part("selection").BackgroundColor(selection),
		style.Name("text-input").Part("caret").ForegroundColor(p.ink),
		style.Name("text-view").ForegroundColor(p.ink).BackgroundColor(p.window).BorderWidth(0).Radius(0),
		style.Name("text-view").Part("frame").BorderColor(p.hairline).BorderWidth(1).Radius(controlRadius),
		style.Name("text-view").Part("frame").State(style.Focused).BorderColor(p.amber).BorderWidth(1),
		style.Name("text-view").State(style.Disabled).ForegroundColor(p.inkFaint).BackgroundColor(p.window),
		style.Name("text-view").Part("placeholder").ForegroundColor(p.inkMuted),
		style.Name("text-view").Part("placeholder").State(style.Disabled).ForegroundColor(p.inkFaint),
		style.Name("text-view").Part("selection").BackgroundColor(selection),
		style.Name("text-view").Part("caret").ForegroundColor(p.ink),

		// Scrolling stays out of the way.
		style.Name("scroll-view").Part("trough").BackgroundColor(color.Transparent).Radius(4),
		style.Name("scroll-view").Part("thumb").BackgroundColor(mixColor(p.window, p.ink, .22)).Radius(4),
		style.Name("scroll-view").Part("thumb").State(style.Hovered).BackgroundColor(mixColor(p.window, p.ink, .34)),
		style.Name("scroll-view").Part("thumb").State(style.Pressed).BackgroundColor(mixColor(p.window, p.ink, .44)),
	)
	return append(rules, quickPanelRules(options)...)
}

// Quick ask type scale, relative to the user's base size (13 pt by default):
// composer base+3, answer base+2, preview rows base+1, question line base,
// meta and mode label base-1.
const (
	styleQuickBackground = "interact-background"
	styleQuickInput      = "interact-input"
	styleQuickAnswer     = "interact-answer"
	styleQuickQuestion   = "interact-question"
	styleQuickMeta       = "interact-meta"
	styleQuickAccent     = "interact-accent"
	styleQuickRow        = "interact-row"
	styleQuickGroup      = "interact-group"
	styleQuickHairline   = "interact-hairline"
)

// interactBackdropColor is the quick panel's graphite surface (the rail token).
func interactBackdropColor() color.NRGBA { return quickPalette().rail }

// quickPanelRules styles the Interact panel from the dark tokens. The native
// host rounds only the lower edge, so GoUI paint stays square. Text is plain
// on the graphite surface: no bubbles and no card fills.
func quickPanelRules(options modern.Options) []style.Rule {
	q := quickPalette()
	base := options.FontSize
	if base <= 0 {
		base = 13
	}
	family := options.FontFamily
	text := func(name string, c color.NRGBA, size float32) style.Rule {
		return style.Name(name).ForegroundColor(c).FontFamily(family).FontSize(size)
	}
	surface := interactBackdropColor()
	return []style.Rule{
		style.Name(styleQuickBackground).BackgroundColor(surface).BorderColor(color.Transparent).BorderWidth(0).Radius(interactPaintCornerRadius),
		style.Name(styleQuickInput).ForegroundColor(q.ink).BackgroundColor(surface).BorderColor(color.Transparent).BorderWidth(0).Radius(0).FontFamily(family).FontSize(base + 3),
		style.Name(styleQuickInput).State(style.Focused).BackgroundColor(surface).BorderColor(color.Transparent).BorderWidth(0),
		style.Name(styleQuickInput).State(style.Hovered).BackgroundColor(surface).BorderColor(color.Transparent).BorderWidth(0),
		style.Name(styleQuickInput).Part("placeholder").ForegroundColor(q.inkFaint).FontFamily(family).FontSize(base + 3),
		style.Name(styleQuickInput).Part("selection").BackgroundColor(mixColor(surface, q.amber, .28)),
		style.Name(styleQuickInput).Part("caret").ForegroundColor(q.ink),
		text(styleQuickAnswer, q.ink, base+2),
		text(styleQuickQuestion, q.inkMuted, base),
		text(styleQuickMeta, q.inkMuted, base-1),
		text(styleQuickAccent, q.amber, base-1),
		text(styleQuickRow, q.ink, base+1),
		text(styleQuickGroup, q.inkMuted, base-1),
		style.Name(styleQuickHairline).BackgroundColor(q.hairline).BorderWidth(0).Radius(0),
	}
}

// ---------------------------------------------------------------------------
// View helpers. They carry no colour of their own; colour lives in the sheet.

// hairline is a 1 px divider that stretches across a stretched column.
func hairline() ui.View {
	return ui.VBox().Style(styleHairline).MinHeight(1).MaxHeight(1)
}

// primaryButton is an amber fill with on-amber ink. Its label must use the
// primary-content style: labels do not inherit from their button.
func primaryButton(label string) *ui.ButtonView {
	return ui.Button().Content(ui.Label(label).Style(modern.PrimaryContent)).Style(modern.Primary).
		Paddings(geometry.Insets{Left: 16, Right: 16, Top: 6, Bottom: 6}).MinHeight(controlHeight)
}

// dangerButton is the destructive confirmation: a danger-token fill. Use it
// only on the second, explicit step of a deletion.
func dangerButton(label string) *ui.ButtonView {
	return ui.Button().Content(ui.Label(label).Style(modern.PrimaryContent)).Style(styleDanger).
		Paddings(geometry.Insets{Left: 16, Right: 16, Top: 6, Bottom: 6}).MinHeight(controlHeight)
}

// secondaryButton is a data-changing action that is not the group's main one:
// ink on the surface with a hairline border. Never amber.
func secondaryButton(label string) *ui.ButtonView {
	return ui.Button(label).Style(styleSecondary).
		Paddings(geometry.Insets{Left: 16, Right: 16, Top: 6, Bottom: 6}).MinHeight(controlHeight)
}

// flatButton is ink text on nothing, with a hairline border on hover only.
func flatButton(label string) *ui.ButtonView {
	return ui.Button(label).Style(modern.Flat).
		Paddings(geometry.Insets{Left: 12, Right: 12, Top: 6, Bottom: 6}).MinHeight(controlHeight)
}

// choiceButton is one option of a small single-choice group: the chosen
// option is a raised, bordered chip; the others stay flat. Not amber: amber is
// reserved for the primary action.
func choiceButton(label string, selected bool) *ui.ButtonView {
	name := modern.Flat
	if selected {
		name = styleChoiceOn
	}
	return ui.Button(label).Style(name).
		Paddings(geometry.Insets{Left: 12, Right: 12, Top: 6, Bottom: 6}).MinHeight(controlHeight)
}

// hugButtons keeps buttons that stand alone in a stretched column at the size
// of their label instead of spanning the whole measure.
func hugButtons(views []ui.View) []ui.View {
	out := make([]ui.View, len(views))
	for i, view := range views {
		if _, ok := view.(*ui.ButtonView); ok {
			view = hug(view)
		}
		out[i] = view
	}
	return out
}

// pageScroll scrolls vertically only, so text wraps at the column width.
func pageScroll(content ui.View) *ui.ScrollViewView {
	return ui.ScrollView(content).Direction(ui.ScrollVertical)
}

// arrowButton is a flat square control with a large glyph, for day stepping.
func arrowButton(glyph string) *ui.ButtonView {
	return ui.Button().Content(ui.Label(glyph).Style(styleArrow)).Style(modern.Flat).
		Paddings(geometry.Insets{Left: 8, Right: 8, Top: 2, Bottom: 2}).MinSize(controlHeight, controlHeight)
}

// wrapLabel is a label that wraps at the column width instead of clipping.
func wrapLabel(text string) *ui.LabelView {
	return ui.Label(text).WrapMode(ui.WrapWordChar)
}

// hug keeps a control at the size of its label inside a stretched column.
func hug(view ui.View) ui.View {
	return ui.HBox(view).CrossAlign(layout.CrossCenter)
}

// section is a section-title followed by its content, with no surrounding box.
func section(title string, content ...ui.View) ui.View {
	rows := make([]ui.View, 0, len(content)+1)
	if title != "" {
		rows = append(rows, wrapLabel(title).Style(styleSectionTitle))
	}
	rows = append(rows, content...)
	return ui.VBox(hugButtons(rows)...).Spacing(8).CrossAlign(layout.CrossStretch)
}

// sections joins blocks with a hairline and 20 px of air on both sides.
func sections(blocks ...ui.View) ui.View {
	rows := make([]ui.View, 0, len(blocks)*2)
	for i, block := range blocks {
		if block == nil {
			continue
		}
		if i > 0 && len(rows) > 0 {
			rows = append(rows, hairline())
		}
		rows = append(rows, block)
	}
	return ui.VBox(rows...).Spacing(20).CrossAlign(layout.CrossStretch)
}

// surfaceBox is a real surface: an object the user interacts with.
func surfaceBox(padding float32, spacing float32, children ...ui.View) *ui.BoxView {
	return ui.VBox(hugButtons(children)...).Padding(padding).Spacing(spacing).CrossAlign(layout.CrossStretch).Style(modern.Card)
}

// captureLightState is what the rail's capture light shows.
type captureLightState int

const (
	captureLightPaused captureLightState = iota
	captureLightOn
	captureLightPending // starting, or waiting for system access
)

// captureLabel is the text beside the capture light.
func captureLightLabel(state captureLightState, starting bool) string {
	switch {
	case state == captureLightOn:
		return "Capturing"
	case starting:
		return "Starting"
	case state == captureLightPending:
		return "Needs access"
	}
	return "Paused"
}

// captureLight draws the 8 px status light: filled amber when capturing, a
// faint filled dot when paused, an amber outline while starting or waiting.
func captureLight(p palette, state captureLightState, redraw *captureLightState) ui.View {
	return ui.DrawingArea(func(painter gui.Painter, size geometry.Size) {
		center := geometry.Point{X: size.Width / 2, Y: size.Height / 2}
		switch state {
		case captureLightOn:
			painter.FillEllipse(center, 4, 4, graphics.ColorOf(p.amber))
		case captureLightPending:
			painter.DrawEllipse(center, 3.25, 3.25, 1.5, graphics.ColorOf(p.amber))
		default:
			painter.FillEllipse(center, 4, 4, graphics.ColorOf(p.inkFaint))
		}
		if redraw != nil {
			*redraw = state
		}
	}).OnShouldDraw(func(need *bool) {
		if redraw != nil && *redraw != state {
			*need = true
		}
	}).MinSize(captureLightPx, captureLightPx).MaxSize(captureLightPx, captureLightPx)
}
