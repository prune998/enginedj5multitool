package main

import (
	"fmt"
	"os"

	. "go.hasen.dev/shirei"
	app "go.hasen.dev/shirei/app"
	. "go.hasen.dev/shirei/widgets"
)

// AppTool is one panel of the application. To add a new tool, implement this
// interface and register a factory with RegisterTool (see init() below).
type AppTool interface {
	// Name is shown in the sidebar.
	Name() string
	// Icon is the sidebar glyph.
	Icon() IconGlyph
	// View renders the tool panel. Tools read shared library state from the
	// App and keep their own local state on their struct.
	View(a *App)
}

var toolFactories []func() AppTool

// RegisterTool adds a tool to the sidebar. Call from init() of the package
// that implements the tool.
func RegisterTool(f func() AppTool) {
	toolFactories = append(toolFactories, f)
}

func init() {
	RegisterTool(func() AppTool { return &CuesTool{} })
	RegisterTool(func() AppTool { return &TagsTool{} })
}

// App is the shared application state: database handle, track list, and the
// currently selected track. Tools receive it in View.
type App struct {
	DBPath    string
	MusicRoot string

	lib       *Library
	LibErrStr string

	Tracks      []TrackRecord
	FilterDraft string // search input buffer
	Filter      string // applied filter
	TrackCount  int

	Selected   int64 // selected Track.id
	ActiveTool int
	Tools      []AppTool

	Theme string // "auto" (OS default), "light" or "dark"

	// Splitter between the track browser and the tool's detail panel.
	splitW       float32 // browser width; 0 = default
	splitRowRect Rect

	onQuit func() // test hook; defaults to app.Quit
}

const (
	splitterWidth   = float32(10)
	minBrowserWidth = float32(300)
	minDetailWidth  = float32(360)
)

// NewApp builds the app state and opens the library.
func NewApp(dbPath string) *App {
	a := &App{DBPath: dbPath, Theme: "auto", splitW: 560}
	for _, f := range toolFactories {
		a.Tools = append(a.Tools, f())
	}
	a.Refresh()
	return a
}

// SelectedTrack returns the record of the selected track, if any.
func (a *App) SelectedTrack() (TrackRecord, bool) {
	for _, r := range a.Tracks {
		if r.ID == a.Selected {
			return r, true
		}
	}
	return TrackRecord{}, false
}

// Refresh (re)opens the library and reloads the track list with the applied
// filter.
func (a *App) Refresh() {
	if a.lib != nil {
		a.lib.Close()
		a.lib = nil
	}
	lib, err := OpenLibrary(a.DBPath, false)
	if err != nil {
		a.lib = nil
		a.LibErrStr = err.Error()
		return
	}
	a.lib = lib
	a.LibErrStr = ""
	tracks, err := lib.Tracks(a.Filter)
	if err != nil {
		a.LibErrStr = err.Error()
		return
	}
	a.Tracks = tracks
	a.TrackCount = len(tracks)
}

// RootView is the whole UI: a top bar with library controls, a tool sidebar
// and the active tool's panel.
func (a *App) RootView() {
	if handleShortcuts() {
		a.quit()
	}
	p := a.pal()
	// The root text ink cascades to every shirei-internal label (checkbox
	// labels, table headers...) so dark mode never shows dark text.
	Container(Attrs(Viewport, BackgroundVec(p.bgRoot), AmendTextStyle(TextColorVec(p.text))), func() {
		a.TopBar()
		Container(Attrs(Row, Grow(1), Expand), func() {
			a.Sidebar()
			Container(Attrs(Grow(1), Expand, Viewport, Pad(10)), func() {
				if a.ActiveTool < 0 || a.ActiveTool >= len(a.Tools) {
					return
				}
				a.Tools[a.ActiveTool].View(a)
			})
		})
	})
}

// handleShortcuts handles global keyboard shortcuts. It reports true when the
// user requested to quit (Cmd-Q on macOS, Ctrl-Q on Windows/Linux). It runs at
// the very top of the frame, before any widget, so a focused text input cannot
// swallow the combo.
func handleShortcuts() bool {
	fi := GetFrameInput()
	if fi.Key == KeyQ && GetInputState().Modifiers == PrimaryMod() {
		fi.Key = 0   // consume: no widget may react to the key...
		fi.Text = "" // ...and no text input may insert a stray "q"
		return true
	}
	return false
}

func (a *App) quit() {
	if a.onQuit != nil {
		a.onQuit()
		return
	}
	app.Quit()
}

// TopBar shows the library path, music root, theme selector and load status.
func (a *App) TopBar() {
	p := a.pal()
	Container(Attrs(Row, CrossMid, Pad2(8, 10), Gap(10), BackgroundVec(p.bgTop)), func() {
		Label("Engine DJ Multi Tool", FontWeight(WeightBold), FontSize(16), TextColorVec(p.text))
		Spacer(8)

		Label("Library", TextColorVec(p.textDim))
		Container(Attrs(Grow(1), MinWidth(180), MaxWidth(320)), func() {
			a.input(&a.DBPath, smallInput(180))
		})
		if Button(SymRefresh, "Load") {
			a.Filter = a.FilterDraft
			a.Refresh()
		}

		Label("Music root", TextColorVec(p.textDim))
		Container(Attrs(Grow(1), MinWidth(120), MaxWidth(260)), func() {
			a.input(&a.MusicRoot, smallInput(120))
		})

		Container(Attrs(Row, CrossMid), func() {
			theme := &a.Theme
			SegmentedControl(theme, func() {
				SegmentedCell("Auto", "auto")
				SegmentedCell("Light", "light")
				SegmentedCell("Dark", "dark")
			})
		})

		Spacer(8)
		if a.LibErrStr != "" {
			Label(a.LibErrStr, TextColor(0, 70, 40, 1))
		} else {
			Label(fmt.Sprintf("%d tracks", a.TrackCount), TextColorVec(p.textDim))
		}
	})
}

// Sidebar lists the registered tools.
func (a *App) Sidebar() {
	p := a.pal()
	Container(Attrs(FixWidth(190), Expand, Pad(6), Gap(4), BackgroundVec(p.bgSide)), func() {
		for i, t := range a.Tools {
			tool, idx, active := t, i, i == a.ActiveTool
			Container(Attrs(Pad2(8, 10), Corners(6), Row, CrossMid, Gap(8)), func() {
				if IsHovered() {
					ModAttrs(BackgroundVec(p.rowHover))
				}
				if active {
					ModAttrs(BackgroundVec(AccentBlue))
				}
				if PressAction() {
					a.ActiveTool = idx
				}
				if active {
					Icon(tool.Icon(), TextColor(0, 0, 100, 1))
					Label(tool.Name(), TextColor(0, 0, 100, 1), FontWeight(WeightBold))
				} else {
					Icon(tool.Icon(), TextColorVec(p.text))
					Label(tool.Name(), TextColorVec(p.text))
				}
			})
		}
		Spacer(6)
		a.L("Tools are pluggable —\nsee RegisterTool().", FontSize(11), TextColorVec(p.textDim))
	})
}

// captureSplitRow records the screen rect of the tool's main row; the splitter
// uses it to convert mouse X into the browser panel width.
func (a *App) captureSplitRow() {
	a.splitRowRect = GetScreenRect()
}

func (a *App) splitWidth() float32 {
	if a.splitW <= 0 {
		return 560
	}
	return a.splitW
}

func (a *App) setSplitFromMouse() {
	mouse := GetInputState().MousePoint
	w := mouse[0] - a.splitRowRect.Origin[0]
	maxW := a.splitRowRect.Size[0] - minDetailWidth - splitterWidth
	if maxW < minBrowserWidth {
		maxW = minBrowserWidth
	}
	if w < minBrowserWidth {
		w = minBrowserWidth
	}
	if w > maxW {
		w = maxW
	}
	a.splitW = w
}

// Splitter is a draggable divider that resizes the track browser. Place it
// between the browser container (FixWidth(a.splitWidth())) and the detail
// container (Grow(1)) inside a row that called a.captureSplitRow().
func (a *App) Splitter() {
	p := a.pal()
	Container(Attrs(FixWidth(splitterWidth), Expand, Pad2(0, 3)), func() {
		NextAccessName("splitter")
		AssignAccess()
		if IsHovered() {
			ModAttrs(BackgroundVec(p.dividerHover))
		}
		if IsActive() {
			ModAttrs(BackgroundVec(p.dividerActive))
		}
		PressAction() // capture the pointer on mouse-down; release ends the drag
		if IsActive() {
			a.setSplitFromMouse()
		}
		Container(Attrs(Grow(1), Expand, Corners(3), BackgroundVec(p.grabber)), func() {
			if IsHovered() || IsActive() {
				ModAttrs(BackgroundVec(p.grabberHover))
			}
		})
	})
}

// BrowserPanel is the shared track list used by the tools: a search row plus
// a sortable, virtualized table. Clicking a row selects the track.
func (a *App) BrowserPanel(extra *TableColumn[TrackRecord]) {
	p := a.pal()
	Container(Attrs(Row, CrossMid, Gap(8), Pad4(0, 0, 8, 0)), func() {
		Icon(SymSearch, TextColorVec(p.textDim))
		Container(Attrs(Expand), func() {
			at := DefaultTextInputAttrs()
			at.Placeholder = "Filter tracks..."
			a.input(&a.FilterDraft, at)
		})
		if Button(SymSearch, "Search") {
			a.Filter = a.FilterDraft
			a.Refresh()
		}
	})

	columns := []TableColumn[TrackRecord]{
		{
			Label: "ID", Width: 60,
			Cell: func(r TrackRecord) { a.L(fmt.Sprintf("%d", r.ID)) },
			Less: func(a, b TrackRecord) bool { return a.ID < b.ID },
		},
		{
			Label: "Artist", Width: 190,
			Cell: func(r TrackRecord) { a.L(r.Artist) },
			Less: func(a, b TrackRecord) bool { return a.Artist < b.Artist },
		},
		{
			Label: "Title",
			Cell:  func(r TrackRecord) { a.L(r.Title) },
			Less:  func(a, b TrackRecord) bool { return a.Title < b.Title },
		},
		{
			Label: "Length", Width: 70,
			Cell: func(r TrackRecord) { a.L(formatDuration(r.Length)) },
			Less: func(a, b TrackRecord) bool { return a.Length < b.Length },
		},
	}
	if extra != nil {
		columns = append(columns, *extra)
	}

	Container(Attrs(Grow(1), Expand, Clip, AmendTextStyle(TextColor(0, 0, 12, 1))), func() {
		attrs := TableAttrs[TrackRecord]{
			RowHeight:         26,
			DefaultSortColumn: 1,
			OnRow:             a.browserRowHighlight,
		}
		TableExt("tracks", attrs, columns, a.Tracks, func(r TrackRecord) any { return r.ID })
	})
}

// browserRowHighlight paints zebra stripes and the selection, handles row
// clicks, and stamps rows with access names (used by screen readers and the
// drive test harness). Wired via TableAttrs.OnRow.
func (a *App) browserRowHighlight(index int, r TrackRecord) {
	p := a.pal()
	NextAccessName(fmt.Sprintf("track-%d", r.ID))
	NextAccessDescription(r.Artist + " — " + r.Title)
	NextAccessValue(formatDuration(r.Length))
	AssignAccess()
	if PressAction() {
		a.Selected = r.ID
	}
	selected := a.Selected == r.ID
	bg := p.rowAlt
	if index%2 == 0 {
		bg = Vec4{0, 0, 0, 0}
	}
	if selected {
		bg = p.rowSel
		if IsHovered() {
			bg = p.rowSelHover
		}
	} else if IsHovered() {
		bg = p.rowHover
	}
	if bg[3] > 0 {
		ModAttrs(BackgroundVec(bg))
	}
}

func runUI(dbPath, snapshotPath string, toolIdx int, filter, theme string) {
	a := NewApp(dbPath)
	if theme == "light" || theme == "dark" {
		a.Theme = theme
	}
	if filter != "" {
		a.Filter, a.FilterDraft = filter, filter
		a.Refresh()
	}
	if toolIdx >= 0 && toolIdx < len(a.Tools) {
		a.ActiveTool = toolIdx
	}
	if snapshotPath == "" {
		app.SetupWindow("Engine DJ Multi Tool", 1180, 740)
		app.Run(a.RootView)
		return
	}
	// Headless snapshot (for tests / docs).
	if len(a.Tracks) > 0 {
		a.Selected = a.Tracks[0].ID
	}
	if err := RenderToPNG(snapshotPath, 1180, 740, a.RootView); err != nil {
		fmt.Fprintf(os.Stderr, "error: snapshot: %v\n", err)
		os.Exit(1)
	}
}
