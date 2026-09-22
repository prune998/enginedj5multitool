package main

import (
	"fmt"
	"strconv"
	"strings"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// ConfigTool edits config.yaml explicitly. Values changed elsewhere in the
// main UI (theme selector, splitter, library path) are session-only — the
// config file is only written by this tool's Save button.
type ConfigTool struct {
	loaded         bool
	path           string
	loadErr        string
	draft          Config
	widthText      string // browser width as editable text
	fontFamilyText string // font family as editable text
	fontSizeText   string // font size as editable text
	saved          bool
}

func (t *ConfigTool) Name() string    { return "Settings" }
func (t *ConfigTool) Icon() IconGlyph { return SymCog }

func (t *ConfigTool) View(a *App) {
	a.L("Settings", FontSize(a.fs(18)), FontWeight(WeightBold))
	t.ensureLoaded()

	a.L(fmt.Sprintf("config file: %s", t.path), FontSize(a.fs(11)), TextColorVec(a.pal().textDim))

	Container(Attrs(Expand, Gap(8), Pad2(6, 0)), func() {
		// Library
		Container(Attrs(Gap(2)), func() {
			a.L("Default Engine DJ database", FontSize(a.fs(11)), TextColorVec(a.pal().textDim))
			a.input(&t.draft.Library, DefaultTextInputAttrs())
		})
		// Music root
		Container(Attrs(Gap(2)), func() {
			a.L("Music root (optional)", FontSize(a.fs(11)), TextColorVec(a.pal().textDim))
			a.input(&t.draft.MusicRoot, DefaultTextInputAttrs())
			a.wrappedText("Extra folder to search when a track's audio file cannot be found. Track paths in the Engine DJ database are relative to the Engine Library folder; leave this empty for the standard layouts (audio next to the library or in the Music.app media tree). Set it only when your audio lives somewhere else entirely — for example an external drive like /Volumes/DJ Music — and every tool (Cues & Loops, MP3 Tags, Dedup) will look there too. Changes apply after pressing Load.",
				640, FontSize(a.fs(11)), TextColorVec(a.pal().textDim))
		})
		// Engine Library folder
		Container(Attrs(Gap(2)), func() {
			a.L("Engine Library folder (e.g. /Users/me/Music/Engine Library)", FontSize(a.fs(11)), TextColorVec(a.pal().textDim))
			a.input(&t.draft.EngineLibrary, DefaultTextInputAttrs())
		})
		// Discogs token
		Container(Attrs(Gap(2)), func() {
			a.L("Discogs personal access token (artwork search) — discogs.com → Settings → Developers",
				FontSize(a.fs(11)), TextColorVec(a.pal().textDim))
			a.input(&t.draft.DiscogsToken, DefaultTextInputAttrs())
		})
		// Theme
		Container(Attrs(Gap(2)), func() {
			a.L("Theme", FontSize(a.fs(11)), TextColorVec(a.pal().textDim))
			theme := &t.draft.Theme
			SegmentedControl(theme, func() {
				SegmentedCell("Auto", "auto")
				SegmentedCell("Light", "light")
				SegmentedCell("Dark", "dark")
			})
		})
		// Browser width
		Container(Attrs(Gap(2)), func() {
			a.L("Track list width at startup", FontSize(a.fs(11)), TextColorVec(a.pal().textDim))
			a.input(&t.widthText, DefaultTextInputAttrs())
		})
		// Font
		Container(Attrs(Row, Expand, Gap(8)), func() {
			Container(Attrs(Grow(1), Gap(2)), func() {
				a.L("Font family (empty = default)", FontSize(a.fs(11)), TextColorVec(a.pal().textDim))
				a.input(&t.fontFamilyText, DefaultTextInputAttrs())
			})
			Container(Attrs(Gap(2)), func() {
				a.L("Font size in px (0 = default 12)", FontSize(a.fs(11)), TextColorVec(a.pal().textDim))
				a.input(&t.fontSizeText, DefaultTextInputAttrs())
			})
		})
	})

	Container(Attrs(Row, CrossMid, Gap(10), Pad2(6, 0)), func() {
		if CtrlButton(SymITick, "Save to config.yaml", t.loadErr == "") {
			t.Save(a)
		}
		if CtrlButton(SymRefresh, "Reload from file", true) {
			t.loaded = false
			t.saved = false
			t.ensureLoaded()
		}
		if t.saved {
			a.L("Saved ✓", TextColor(140, 45, 34, 1), FontSize(a.fs(12)))
		}
	})
	a.L("Saving applies the values to this session. Changes made elsewhere in the UI (theme, splitter, library path) are session-only and never written to the config file.",
		FontSize(a.fs(11)), TextColorVec(a.pal().textDim))
}

// browserWidthDraft exposes the browser width as an editable string, kept in
// sync with the numeric draft value.
func (t *ConfigTool) browserWidthDraft() *string {
	return &t.widthText
}

// ensureLoaded reads the config file once (or after an explicit reload).
func (t *ConfigTool) ensureLoaded() {
	if t.loaded {
		return
	}
	t.loaded = true
	t.saved = false
	lc := LoadOrCreateConfig()
	t.path = lc.Path
	t.loadErr = errString(lc.Err)
	t.draft = lc.Config
	t.syncWidthText()
	t.fontFamilyText = t.draft.FontFamily
	t.fontSizeText = intText(t.draft.FontSize)
}

// intText renders an int as text (0 stays "0").
func intText(v int) string {
	return strconv.Itoa(v)
}

// syncWidthText refreshes the browser-width text field from the draft value.
func (t *ConfigTool) syncWidthText() {
	t.widthText = strconv.FormatFloat(float64(t.draft.BrowserWidth), 'f', -1, 32)
}

func (t *ConfigTool) Save(a *App) {
	w, err := strconv.ParseFloat(strings.TrimSpace(t.widthText), 32)
	if err != nil || w < 200 || w > 4000 {
		Toast(SymFail, "Invalid browser width", "Enter a number between 200 and 4000.")
		return
	}
	t.draft.BrowserWidth = float32(w)
	size, err := strconv.Atoi(strings.TrimSpace(t.fontSizeText))
	if err != nil || size < 0 || size > 200 {
		Toast(SymFail, "Invalid font size", "Enter a number between 0 and 200 (0 = default).")
		return
	}
	t.draft.FontSize = size
	t.draft.FontFamily = strings.TrimSpace(t.fontFamilyText)
	t.draft.Theme = strings.ToLower(strings.TrimSpace(t.draft.Theme))
	switch t.draft.Theme {
	case "auto", "light", "dark":
	default:
		Toast(SymFail, "Invalid theme", "Use auto, light or dark.")
		return
	}
	if err := SaveConfigFile(t.path, t.draft); err != nil {
		Toast(SymFail, "Config save failed", err.Error())
		return
	}
	t.saved = true

	// Apply the saved values to the running session.
	a.DBPath = t.draft.Library
	a.MusicRoot = t.draft.MusicRoot
	a.Theme = t.draft.Theme
	if t.draft.BrowserWidth > 0 {
		a.splitW = t.draft.BrowserWidth
	}
	a.FontFamily = t.draft.FontFamily
	a.FontSize = t.draft.FontSize
	if a.FontSize > 0 {
		GetHost().ComfortScale = float32(a.FontSize) / 12
	} else {
		GetHost().ComfortScale = 1
	}
	if tags, ok := a.Tools[1].(*TagsTool); ok {
		tags.discogsToken = t.draft.DiscogsToken
	}
	a.Refresh()
	Toast(SymITick, "Config saved", "Settings applied to this session and written to config.yaml.")
}
