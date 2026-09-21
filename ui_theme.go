package main

import (
	"os/exec"
	"runtime"
	"strings"
	"sync"

	. "go.hasen.dev/shirei"
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

	text    Vec4
	textDim Vec4

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

	text:    Vec4{0, 0, 15, 1},
	textDim: Vec4{220, 8, 40, 1},

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

	text:    Vec4{0, 0, 90, 1},
	textDim: Vec4{220, 8, 62, 1},

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
