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
}

// NewApp builds the app state and opens the library.
func NewApp(dbPath string) *App {
	a := &App{DBPath: dbPath}
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
	Container(Attrs(Viewport), func() {
		a.TopBar()
		Container(Attrs(Row, Grow(1), Expand), func() {
			a.Sidebar()
			Container(Attrs(Grow(1), Expand, Viewport, Pad(10), Gap(8)), func() {
				if a.ActiveTool < 0 || a.ActiveTool >= len(a.Tools) {
					return
				}
				a.Tools[a.ActiveTool].View(a)
			})
		})
	})
}

// TopBar shows the library path, music root and load status.
func (a *App) TopBar() {
	Container(Attrs(Row, CrossMid, Pad2(8, 10), Gap(10), Background(220, 14, 94, 1)), func() {
		Label("Engine DJ Multi Tool", FontWeight(WeightBold), FontSize(16))
		Spacer(8)

		Label("Library", TextColor(220, 8, 40, 1))
		Container(Attrs(FixWidth(320)), func() {
			TextInput(&a.DBPath)
		})
		if Button(SymRefresh, "Load") {
			a.Filter = a.FilterDraft
			a.Refresh()
		}

		Label("Music root", TextColor(220, 8, 40, 1))
		Container(Attrs(FixWidth(260)), func() {
			TextInput(&a.MusicRoot)
		})

		Spacer(8)
		if a.LibErrStr != "" {
			Label(a.LibErrStr, TextColor(0, 70, 40, 1))
		} else {
			Label(fmt.Sprintf("%d tracks", a.TrackCount), TextColor(220, 8, 40, 1))
		}
	})
}

// Sidebar lists the registered tools.
func (a *App) Sidebar() {
	Container(Attrs(FixWidth(190), Expand, Pad(6), Gap(4), Background(220, 10, 90, 1)), func() {
		for i, t := range a.Tools {
			tool, idx := t, i
			active := idx == a.ActiveTool
			Container(Attrs(Pad2(8, 10), Corners(6), Row, CrossMid, Gap(8)), func() {
				if IsHovered() {
					ModAttrs(Background(220, 14, 84, 1))
				}
				if active {
					ModAttrs(BackgroundVec(AccentBlue))
				}
				if PressAction() {
					a.ActiveTool = idx
				}
				Icon(tool.Icon(), TextColor(0, 0, 20, 1))
				labelAttrs := []TextStyleFn{TextColor(0, 0, 20, 1)}
				if active {
					labelAttrs = []TextStyleFn{TextColor(0, 0, 100, 1), FontWeight(WeightBold)}
				}
				Label(tool.Name(), labelAttrs...)
			})
		}
		Spacer(6)
		Label("Tools are pluggable —\nsee RegisterTool().", FontSize(11), TextColor(220, 6, 55, 1))
	})
}

// BrowserPanel is the shared track list used by the tools: a search row plus
// a sortable, virtualized table. Clicking a row selects the track.
func (a *App) BrowserPanel(extra *TableColumn[TrackRecord]) {
	Container(Attrs(Row, CrossMid, Gap(8), Pad4(0, 0, 8, 0)), func() {
		Icon(SymSearch, TextColor(220, 8, 40, 1))
		Container(Attrs(Expand), func() {
			TextInput(&a.FilterDraft)
		})
		if Button(SymSearch, "Search") {
			a.Filter = a.FilterDraft
			a.Refresh()
		}
	})

	columns := []TableColumn[TrackRecord]{
		{
			Label: "ID", Width: 60,
			Cell: func(r TrackRecord) { Label(fmt.Sprintf("%d", r.ID)) },
			Less: func(a, b TrackRecord) bool { return a.ID < b.ID },
		},
		{
			Label: "Artist", Width: 190,
			Cell: func(r TrackRecord) { Label(r.Artist) },
			Less: func(a, b TrackRecord) bool { return a.Artist < b.Artist },
		},
		{
			Label: "Title",
			Cell:  func(r TrackRecord) { Label(r.Title) },
			Less:  func(a, b TrackRecord) bool { return a.Title < b.Title },
		},
		{
			Label: "Length", Width: 70,
			Cell: func(r TrackRecord) { Label(formatDuration(r.Length)) },
			Less: func(a, b TrackRecord) bool { return a.Length < b.Length },
		},
	}
	if extra != nil {
		columns = append(columns, *extra)
	}

	Container(Attrs(Grow(1), Expand, Clip), func() {
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
	NextAccessName(fmt.Sprintf("track-%d", r.ID))
	NextAccessDescription(r.Artist + " — " + r.Title)
	NextAccessValue(formatDuration(r.Length))
	AssignAccess()
	if PressAction() {
		a.Selected = r.ID
	}
	selected := a.Selected == r.ID
	hue, sat, light := float32(220), float32(8), float32(100)
	if r.ID%2 == 1 {
		light = 96
	}
	if selected {
		hue, sat, light = 204, 70, 85
		if IsHovered() {
			light = 78
		}
	} else if IsHovered() {
		sat, light = 14, 90
	}
	ModAttrs(Background(hue, sat, light, 1))
}

func runUI(dbPath, snapshotPath string, toolIdx int, filter string) {
	a := NewApp(dbPath)
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
