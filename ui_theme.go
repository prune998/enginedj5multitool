package main

import (
	"os/exec"
	"runtime"
	"strings"
	"sync"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// palette holds the HSLA colors used across the UI. HSL ranges follow shirei:
// hue in degrees, saturation/lightness in percent, alpha 0..1.
type palette struct {
	bgRoot  Vec4
	bgTop   Vec4
	bgSide  Vec4
	bgPanel Vec4

	rowAlt      Vec4
	rowHover    Vec4
	rowSel      Vec4
	rowSelHover Vec4

	text      Vec4
	textDim   Vec4
	textError Vec4
	textOk    Vec4

	inputFace        Vec4
	inputBorder      Vec4
	inputBorderFocus Vec4
	inputInk         Vec4
	inputSel         Vec4

	swatchEmpty   Vec4
	grabber       Vec4
	grabberHover  Vec4
	dividerHover  Vec4
	dividerActive Vec4

	bubbleInk Vec4
}

var lightPalette = palette{
	bgRoot:  Vec4{220, 14, 97, 1},
	bgTop:   Vec4{220, 14, 94, 1},
	bgSide:  Vec4{220, 10, 90, 1},
	bgPanel: Vec4{220, 10, 97, 1},

	rowAlt:      Vec4{220, 8, 96, 1},
	rowHover:    Vec4{220, 14, 90, 1},
	rowSel:      Vec4{204, 70, 85, 1},
	rowSelHover: Vec4{204, 70, 76, 1},

	text:      Vec4{0, 0, 15, 1},
	textDim:   Vec4{220, 8, 40, 1},
	textError: Vec4{0, 70, 36, 1},
	textOk:    Vec4{140, 55, 30, 1},

	inputFace:        Vec4{0, 0, 100, 1},
	inputBorder:      Vec4{0, 0, 0, 0.16},
	inputBorderFocus: Vec4{204, 70, 40, 0.55},
	inputInk:         Vec4{0, 0, 12, 1},
	inputSel:         Vec4{204, 60, 50, 0.35},

	swatchEmpty:   Vec4{0, 0, 88, 1},
	grabber:       Vec4{220, 6, 70, 1},
	grabberHover:  Vec4{204, 60, 55, 1},
	dividerHover:  Vec4{204, 40, 90, 1},
	dividerActive: Vec4{204, 40, 84, 1},

	bubbleInk: Vec4{0, 0, 20, 1},
}

var darkPalette = palette{
	bgRoot:  Vec4{220, 10, 13, 1},
	bgTop:   Vec4{220, 10, 16, 1},
	bgSide:  Vec4{220, 10, 18, 1},
	bgPanel: Vec4{220, 10, 13, 1},

	rowAlt:      Vec4{220, 8, 17, 1},
	rowHover:    Vec4{220, 10, 24, 1},
	rowSel:      Vec4{204, 45, 30, 1},
	rowSelHover: Vec4{204, 45, 38, 1},

	text:      Vec4{0, 0, 90, 1},
	textDim:   Vec4{220, 8, 62, 1},
	textError: Vec4{4, 78, 66, 1},
	textOk:    Vec4{140, 55, 62, 1},

	inputFace:        Vec4{220, 10, 24, 1},
	inputBorder:      Vec4{220, 8, 55, 0.45},
	inputBorderFocus: Vec4{204, 70, 55, 0.9},
	inputInk:         Vec4{0, 0, 92, 1},
	inputSel:         Vec4{204, 60, 45, 0.45},

	swatchEmpty:   Vec4{0, 0, 30, 1},
	grabber:       Vec4{220, 6, 40, 1},
	grabberHover:  Vec4{204, 60, 60, 1},
	dividerHover:  Vec4{220, 10, 24, 1},
	dividerActive: Vec4{204, 30, 28, 1},

	bubbleInk: Vec4{0, 0, 95, 1},
}

// dark reports whether the UI should use the dark palette, honouring the
// user's theme selection ("auto" follows the OS).
func (a *App) dark() bool {
	switch a.Theme {
	case "dark":
		return true
	case "light":
		return false
	default:
		return osPrefersDark()
	}
}

func (a *App) pal() palette {
	if a.dark() {
		return darkPalette
	}
	return lightPalette
}

// L renders a themed label: the palette text color is applied first so
// explicit TextColor mods passed by the caller override it.
func (a *App) L(text string, mods ...TextStyleFn) {
	Label(text, append([]TextStyleFn{TextColorVec(a.pal().text)}, mods...)...)
}

// reportLine is one line of a tool run report; style picks the ink: "" is
// the normal text color, "err" the error color, "ok" the success color.
type reportLine struct {
	text  string
	style string
}

// paneTextWidth returns the usable text width of the tool's right pane.
func (a *App) paneTextWidth() float32 {
	w := a.splitRowRect.Size[0] - a.splitW - 10 - 20 // splitter + padding
	if w < 200 {
		w = 600
	}
	return w
}

// wrappedText renders a themed label soft-wrapped to the given max width
// (shirei labels only wrap when their container carries a MaxWidth).
func (a *App) wrappedText(text string, maxWidth float32, mods ...TextStyleFn) {
	Container(Attrs(Expand, MaxWidth(maxWidth)), func() {
		a.L(text, mods...)
	})
}

// errorText renders an error message in the theme's error color, soft-wrapped
// to the given max width.
func (a *App) errorText(text string, maxWidth float32) {
	a.wrappedText(text, maxWidth, FontSize(12), TextColorVec(a.pal().textError))
}

// reportLines renders run-report lines, wrapping each to the pane width and
// coloring them by style.
func (a *App) reportLines(lines []reportLine, maxWidth float32) {
	for _, ln := range lines {
		switch ln.style {
		case "err":
			a.errorText(ln.text, maxWidth)
		case "ok":
			a.wrappedText(ln.text, maxWidth, FontSize(12), TextColorVec(a.pal().textOk))
		case "dim":
			a.wrappedText(ln.text, maxWidth, FontSize(12), TextColorVec(a.pal().textDim))
		default:
			a.wrappedText(ln.text, maxWidth, FontSize(12))
		}
	}
}

// input is a themed replacement for widgets.TextInput / widgets.TextInputExt:
// same layout and editing behaviour, but the face, border and ink come from
// the palette so dark mode gets dark fields with light text.
func (a *App) input(buf *string, attrs TextInputAttrs) {
	p := a.pal()
	raw := TextInputConfigFromAttrs(attrs)
	raw.TextColor = p.inputInk
	raw.CaretColor = p.inputInk
	raw.SelectionColor = p.inputSel
	// Placeholder ink derives from TextColor at reduced alpha.

	// Replicate widgets' withDefaults + withComfort for the chrome sizing and
	// the draw config (ProcessInputText comforts its own copy internally from
	// the unscaled raw config).
	cfg := raw
	if cfg.FontSize == 0 {
		cfg.FontSize = DefaultTextSize
	}
	if cfg.Padding == (Vec4{}) {
		cfg.Padding = N4(cfg.FontSize / 2)
	}
	s := ComfortScale()
	cfg.FontSize *= s
	cfg.Padding[0] *= s
	cfg.Padding[1] *= s
	cfg.Padding[2] *= s
	cfg.Padding[3] *= s

	padSize := PadSize(cfg.Padding)
	lineHeight := cfg.FontSize
	rows := 1
	switch {
	case attrs.Rows > 0:
		rows = attrs.Rows
	case attrs.MaxLines == 0:
		rows = 4
	default:
		rows = attrs.MaxLines
		if rows > 4 {
			rows = 4
		}
		if rows < 1 {
			rows = 1
		}
	}
	minW := padSize[0] + cfg.FontSize*10
	if cfg.MinWidth > 0 {
		minW = cfg.MinWidth
	}
	boxH := float32(rows)*lineHeight + padSize[1]
	maxW := cfg.MaxWidth
	if cfg.FixedWidth {
		maxW = minW
	}
	minSize := Vec2{minW, boxH}
	parent := GetAttrs()

	Container(Attrs(
		Focusable,
		Corners(4),
		BackgroundVec(p.inputFace),
		PadVec(cfg.Padding),
		MinSizeVec(minSize),
		MaxSizeVec(Vec2{maxW, boxH}),
		Clip,
		BorderWidth(1),
		BorderColorVec(p.inputBorder),
	), func() {
		if !cfg.FixedWidth && cfg.MaxWidth == 0 {
			ModAttrs(Expand)
			if parent.Row {
				ModAttrs(Grow(1))
			}
		}
		st := ProcessTextInput(buf, raw)
		NextAccessRole("text")
		NextAccessEditable(true, cfg.Wrap || cfg.MaxLines != 1)
		NextAccessProtected(attrs.Masked)
		if !attrs.Masked {
			NextAccessValue(*buf)
		} else {
			NextAccessValue("")
		}
		AssignAccess()
		if st.HasFocus {
			ModAttrs(BorderColorVec(p.inputBorderFocus))
		} else {
			ModAttrs(BorderColorVec(p.inputBorder))
		}
		size := st.FieldSize
		if size == (Vec2{}) {
			size = minSize
		}
		if attrs.Depth > 0 {
			topH := float32(6) * attrs.Depth
			topA := float32(0.04) * attrs.Depth
			if topH > 16 {
				topH = 16
			}
			if topA > 0.14 {
				topA = 0.14
			}
			Element(Attrs(NoAnimate, ClickThrough, Float(0, 0), FixSize(size[0], topH),
				Background(0, 0, 0, topA), Grad(0, 0, 0, -topA)))
		}
		DrawTextInputPlain(st, cfg)
	})
}

// textArea is the multi-line themed input (themed TextArea).
func (a *App) textArea(buf *string) {
	a.input(buf, DefaultMultilineTextInputAttrs())
}

// smallInput is a TextInputAttrs with a reduced minimum width while keeping
// the default single-line behaviour.
func smallInput(minWidth float32) TextInputAttrs {
	at := DefaultTextInputAttrs()
	at.MinWidth = minWidth
	return at
}

var osDarkOnce sync.Once
var osDarkValue bool

// osPrefersDark reports the OS appearance preference (cached).
func osPrefersDark() bool {
	osDarkOnce.Do(func() { osDarkValue = detectOSDark() })
	return osDarkValue
}

// detectOSDark is a best-effort OS appearance probe: `defaults` on macOS,
// gsettings on GNOME, the registry on Windows; light elsewhere.
func detectOSDark() bool {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("defaults", "read", "-g", "AppleInterfaceStyle").Output()
		return err == nil && strings.Contains(strings.ToLower(string(out)), "dark")
	case "linux":
		out, err := exec.Command("gsettings", "get", "org.gnome.desktop.interface", "color-scheme").Output()
		return err == nil && strings.Contains(strings.ToLower(string(out)), "dark")
	case "windows":
		out, err := exec.Command("reg", "query",
			`HKCU\Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`,
			"/v", "AppsUseLightTheme").Output()
		// 0x1 = light apps; anything else counts as dark.
		return err == nil && !strings.Contains(strings.ToLower(string(out)), "0x1")
	default:
		return false
	}
}

// resolveDark is the pure core of a.dark(), kept testable.
func resolveDark(theme string, osDark bool) bool {
	switch theme {
	case "dark":
		return true
	case "light":
		return false
	default:
		return osDark
	}
}

// tagHue derives a stable hue for a tag name so bubbles keep their color.
func tagHue(tag string) float32 {
	h := 0
	for _, r := range tag {
		h = (h*31 + int(r)) % 360
	}
	return float32(h)
}
