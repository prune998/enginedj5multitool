package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	generic "go.hasen.dev/generic"
	. "go.hasen.dev/shirei"
	app "go.hasen.dev/shirei/app"
	"go.hasen.dev/shirei/audio"
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
	RegisterTool(func() AppTool { return &GlobalTool{} })
	RegisterTool(func() AppTool { return &RelinkTool{} })
	RegisterTool(func() AppTool { return &DedupTool{} })
	RegisterTool(func() AppTool { return &PlaylistCreatorTool{} })
	RegisterTool(func() AppTool { return &ConfigTool{} })
}

// App is the shared application state: database handle, track list, and the
// currently selected track. Tools receive it in View.
type App struct {
	DBPath        string
	MusicRoot     string
	EngineLibrary string

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

	FontFamily string // UI font family (empty = shirei default)
	FontSize   int    // UI font size in px (0 = shirei default 12)

	// Track browser: threaded sort state (header clicks) and scroll offset
	// (wheel writes it back; arrow navigation writes it to follow the
	// selection). tableH caches the list viewport height.
	SortState  TableSortState
	listScroll float32
	tableH     float32

	// Splitter between the track browser and the tool's detail panel.
	splitW       float32 // browser width; 0 = default
	splitRowRect Rect

	// Small-field row of the tags form: captured row width drives the
	// Genre field width so the row always fits exactly.
	formRowRect Rect

	onQuit func() // test hook; defaults to app.Quit

	cfgPath string // config.yaml path (shown/edited by the Settings tool)

	// macOS Full Disk Access: set at startup when TCC blocks the Music
	// folder; the banner offers a shortcut to the settings pane.
	fdaNotice bool

	// Stems: which tracks have stem files, the player state (the player
	// lives in the MP3 Tags pane), and the audio mixer (platform audio
	// starts on first stems playback).
	stemsSet         map[int64]bool
	stemsScanDone    chan struct{}
	stemsPlayer      *AudioPlayer
	stemsPlayerTrack int64
	stemsMute        [4]bool // stem enabled state (unchecked = muted)
	stemsMutePrev    [4]bool
	mixer            *audio.Mixer
	audioStarted     bool
}

const (
	splitterWidth   = float32(10)
	minBrowserWidth = float32(300)
	minDetailWidth  = float32(360)
)

// NewApp builds the app state and opens the library.
func NewApp(dbPath string) *App {
	a := &App{DBPath: dbPath, Theme: "auto", splitW: 560, SortState: TableSortState{Column: 1}, mixer: audio.NewMixer()}
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

// Close releases the database handle (no-op when the library never opened).
// Tests must call it so temp directories can be cleaned up on Windows,
// which refuses to delete files that are still open. It also waits for the
// background stems scan so nothing outlives the test.
func (a *App) Close() {
	if a.stemsScanDone != nil {
		<-a.stemsScanDone
		a.stemsScanDone = nil
	}
	if a.lib != nil {
		a.lib.Close()
		a.lib = nil
	}
}

// Refresh (re)opens the library and reloads the track list with the applied
// filter.
func (a *App) Refresh() {
	if a.lib != nil {
		a.lib.Close()
		a.lib = nil
	}
	lib, err := OpenLibrary(a.DBPath, false)
	if err == nil {
		lib.MusicRoot = a.MusicRoot
		lib.EngineLibrary = a.EngineLibrary
	}
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
	a.buildStemsSet()
}

// RootView is the whole UI: a top bar with library controls, a tool sidebar
// and the active tool's panel.
func (a *App) RootView() {
	a.handleGlobalKeys()
	// Re-apply per frame: a fresh UI (headless snapshots) resets the host.
	if a.FontSize > 0 {
		GetHost().ComfortScale = float32(a.FontSize) / 12
	}
	p := a.pal()
	// The root text style cascades to every shirei-internal label (checkbox
	// labels, table headers...) so dark mode never shows dark text and the
	// configured font family/size apply everywhere.
	rootMods := []TextStyleFn{TextColorVec(p.text)}
	if a.FontFamily != "" {
		rootMods = append(rootMods, Fonts(a.FontFamily))
	}
	if a.FontSize > 0 {
		rootMods = append(rootMods, FontSize(float32(a.FontSize)))
	}
	Container(Attrs(Viewport, BackgroundVec(p.bgRoot), AmendTextStyle(rootMods...)), func() {
		a.TopBar()
		if a.fdaNotice {
			a.FullDiskAccessBanner()
		}
		Container(Attrs(Row, Grow(1), Expand), func() {
			a.Sidebar()
			Container(Attrs(Grow(1), Expand, Viewport, Pad(10)), func() {
				if a.ActiveTool < 0 || a.ActiveTool >= len(a.Tools) {
					return
				}
				a.Tools[a.ActiveTool].View(a)
				ScrollBars()
			})
		})
	})
}

// handleGlobalKeys handles global keyboard shortcuts at the very top of the
// frame, before any widget: Cmd-Q/Ctrl-Q quits, and plain Up/Down arrows move
// the track selection (the key is consumed so focused text inputs don't see
// it).
func (a *App) handleGlobalKeys() {
	fi := GetFrameInput()
	// Accept Cmd-Q and Ctrl-Q on every platform (Linux window managers and
	// the drive test harness report either modifier).
	if fi.Key == KeyQ && GetInputState().Modifiers&(ModCmd|ModCtrl) != 0 {
		fi.Key = 0   // consume: no widget may react to the key...
		fi.Text = "" // ...and no text input may insert a stray "q"
		a.quit()
		return
	}
	switch fi.Key {
	case KeyUp:
		if a.moveSelection(-1) {
			fi.Key = 0
		}
	case KeyDown:
		if a.moveSelection(1) {
			fi.Key = 0
		}
	}
}

// moveSelection moves the selected track one row up/down in the displayed
// (sorted) order and scrolls the browser so the row stays visible. Returns
// false when there is nothing to select.
func (a *App) moveSelection(dir int) bool {
	ordered := a.orderedTracks()
	if len(ordered) == 0 {
		return false
	}
	idx := -1
	for i, r := range ordered {
		if r.ID == a.Selected {
			idx = i
			break
		}
	}
	if idx < 0 {
		if dir > 0 {
			idx = 0
		} else {
			idx = len(ordered) - 1
		}
	} else {
		idx += dir
		if idx < 0 {
			idx = 0
		}
		if idx >= len(ordered) {
			idx = len(ordered) - 1
		}
	}
	a.Selected = ordered[idx].ID

	// Follow the selection: place the row about a third from the top.
	const rowHeight = 26
	vh := a.tableH
	if vh <= 0 {
		vh = 480
	}
	target := float32(idx)*rowHeight - vh/3
	if target < 0 {
		target = 0
	}
	a.listScroll = target
	RequestNextFrame()
	return true
}

// orderedTracks returns the tracks in the browser's current display order
// (mirrors the table's sort state).
func (a *App) orderedTracks() []TrackRecord {
	cols := a.trackColumns(nil)
	rows := append([]TrackRecord{}, a.Tracks...)
	col := a.SortState.Column
	if col < 0 || col >= len(cols) || cols[col].Less == nil {
		return rows
	}
	less := cols[col].Less
	if a.SortState.Desc {
		sort.SliceStable(rows, func(i, j int) bool { return less(rows[j], rows[i]) })
	} else {
		sort.SliceStable(rows, func(i, j int) bool { return less(rows[i], rows[j]) })
	}
	return rows
}

func (a *App) quit() {
	if a.onQuit != nil {
		a.onQuit()
		return
	}
	app.Quit()
}

// FullDiskAccessBanner tells the user macOS is blocking the Music folder
// and offers a shortcut to the Full Disk Access settings pane. Shown under
// the top bar while the restriction is detected; it can be dismissed (the
// app may still work when the library lives outside the protected folder).
func (a *App) FullDiskAccessBanner() {
	p := a.pal()
	Container(Attrs(Row, CrossMid, Pad2(8, 6), Gap(10), BackgroundVec(p.bgPanel)), func() {
		Icon(SymWarn, TextColorVec(p.textError), FontSize(a.fs(14)))
		a.wrappedText("macOS is blocking access to the Music folder: grant this app Full Disk Access (System Settings → Privacy & Security → Full Disk Access), then press Load.",
			900, FontSize(a.fs(12)), TextColorVec(p.text))
		if CtrlButton(SymCog, "Open Full Disk Access settings", true) {
			_ = openFullDiskAccessPane()
		}
		if CtrlButton(SymICross, "Dismiss", true) {
			WithFrameLock(func() { a.fdaNotice = false })
		}
	})
}

// TopBar shows the library path, theme selector and load status. The music
// root override lives in the Settings tool (see its help text there).
func (a *App) TopBar() {
	p := a.pal()
	Container(Attrs(Row, CrossMid, Pad2(8, 10), Gap(10), BackgroundVec(p.bgTop)), func() {
		Label("Engine DJ Multi Tool", FontWeight(WeightBold), FontSize(a.fs(16)), TextColorVec(p.text))
		Spacer(8)

		Label("Library", TextColorVec(p.textDim))
		Container(Attrs(Grow(1), MinWidth(180), MaxWidth(320)), func() {
			a.input(&a.DBPath, smallInput(180))
		})
		if Button(SymRefresh, "Load") {
			a.Filter = a.FilterDraft
			a.Refresh()
		}

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
		a.L("Tools are pluggable —\nsee RegisterTool().", FontSize(a.fs(11)), TextColorVec(p.textDim))
	})
}

// captureSplitRow records the screen rect of the tool's main row; the splitter
// uses it to convert mouse X into the browser panel width.
func (a *App) captureSplitRow() {
	a.splitRowRect = GetScreenRect()
}

// captureFormRow records the screen rect of the tags form's small-field row.
func (a *App) captureFormRow() {
	a.formRowRect = GetScreenRect()
}

// genreFieldWidth computes the Genre input width as the row width left over
// after the data-sized Year/Track/Disc/BPM fields and the gaps.
func (a *App) genreFieldWidth(others float32) float32 {
	w := a.formRowRect.Size[0] - others - 4*8 - 2 // gaps + rounding buffer
	if w < 120 {
		w = 120
	}
	return w
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

// trackColumns builds the browser's columns; extra (optional) is appended by
// tools that want an additional column (e.g. file type).
func (a *App) trackColumns(extra *TableColumn[TrackRecord]) []TableColumn[TrackRecord] {
	p := a.pal()
	columns := []TableColumn[TrackRecord]{
		{
			Label: "ID", Width: 60,
			Cell: func(r TrackRecord) { a.L(fmt.Sprintf("%d", r.ID)) },
			Less: func(a, b TrackRecord) bool { return a.ID < b.ID },
		},
		stemsColumn(a),
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
			Label: "Key", Width: 64,
			Cell: func(r TrackRecord) { keyBadge(a, r.Key) },
			Less: func(a, b TrackRecord) bool { return a.Key < b.Key },
		},
		{
			Label: "Rating", Width: 92,
			Cell: func(r TrackRecord) { a.stars(r.Rating, 12, nil) },
			Less: func(a, b TrackRecord) bool { return a.Rating < b.Rating },
		},
		{
			Label: "Length", Width: 70,
			Cell: func(r TrackRecord) { a.L(formatDuration(r.Length)) },
			Less: func(a, b TrackRecord) bool { return a.Length < b.Length },
		},
		{
			Label: "File", Width: 260,
			Cell: func(r TrackRecord) { a.L(filepath.Base(r.Path), FontSize(a.fs(11)), TextColorVec(p.textDim)) },
			Less: func(a, b TrackRecord) bool { return strings.ToLower(a.Path) < strings.ToLower(b.Path) },
		},
	}
	if extra != nil {
		columns = append(columns, *extra)
	}
	return columns
}

// stars renders a 0-5 star rating (rating in 0..100, steps of 20): gold
// filled stars for the rating, dim outlines for the rest. When onClick is
// non-nil the stars are clickable (star number is passed).
func (a *App) stars(rating int64, size float32, onClick func(star int)) {
	filled := int(rating / 20)
	if filled > 5 {
		filled = 5
	}
	if filled < 0 {
		filled = 0
	}
	p := a.pal()
	Container(Attrs(Row, CrossMid), func() {
		for i := 1; i <= 5; i++ {
			i, on := i, i <= filled
			Container(Attrs(Pad2(0, 0)), func() {
				if onClick != nil {
					NextAccessName(fmt.Sprintf("rating-star-%d", i))
					AssignAccess()
					if IsHovered() {
						ModAttrs(BackgroundVec(p.rowHover))
					}
					if PressAction() {
						onClick(i)
					}
				}
				if on {
					Icon(TypStar, FontSize(a.fs(size)), TextColor(45, 85, 52, 1))
				} else {
					Icon(TypStarOutline, FontSize(a.fs(size)), TextColorVec(p.textDim))
				}
			})
		}
	})
}

// BrowserPanel is the shared track list used by the tools: a search row plus
// a sortable, virtualized table. Clicking a row selects the track; Up/Down
// arrows move the selection (see handleGlobalKeys).
func (a *App) BrowserPanel(extra *TableColumn[TrackRecord]) {
	p := a.pal()
	Container(Attrs(Row, CrossMid, Gap(8), Pad4(0, 0, 8, 0)), func() {
		Icon(SymSearch, TextColorVec(p.textDim))
		Container(Attrs(Expand), func() {
			NextAccessName("filter-input")
			AssignAccess()
			at := DefaultTextInputAttrs()
			at.Placeholder = "Filter tracks..."
			a.input(&a.FilterDraft, at)
			// Return in the filter box runs the search.
			if fi := GetFrameInput(); fi.Key == KeyEnter && HasFocusWithin() {
				fi.Key = 0
				a.Filter = a.FilterDraft
				a.Refresh()
			}
		})
		if CtrlButton(SymICross, "Clear", strings.TrimSpace(a.FilterDraft) != "" || strings.TrimSpace(a.Filter) != "") {
			a.FilterDraft, a.Filter = "", ""
			a.Refresh()
		}
		if Button(SymSearch, "Search") {
			a.Filter = a.FilterDraft
			a.Refresh()
		}
	})

	columns := a.trackColumns(extra)

	// The table header keeps its own light chrome, so pin dark ink for it;
	// body cells use a.L with the themed ink.
	Container(Attrs(Grow(1), Expand, Clip, AmendTextStyle(TextColor(0, 0, 12, 1))), func() {
		a.tableH = GetResolvedHeight()
		attrs := TableAttrs[TrackRecord]{
			RowHeight:    a.fs(26),
			SortState:    &a.SortState,
			ScrollOffset: &a.listScroll,
			OnRow:        a.browserRowHighlight,
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

func runUI(lc LoadedConfig, snapshotPath string, filter string) {
	a := NewApp(lc.Library)
	a.MusicRoot = lc.MusicRoot
	a.EngineLibrary = lc.EngineLibrary
	// NewApp's Refresh ran before the library paths were set — push them
	// into the open library and rescan for stems.
	if a.lib != nil {
		a.lib.MusicRoot = a.MusicRoot
		a.lib.EngineLibrary = a.EngineLibrary
	}
	a.buildStemsSet()
	a.Theme = lc.Theme
	a.FontFamily = lc.FontFamily
	a.FontSize = lc.FontSize
	if lc.BrowserWidth > 0 {
		a.splitW = lc.BrowserWidth
	}
	if lc.Tool >= 0 && lc.Tool < len(a.Tools) {
		a.ActiveTool = lc.Tool
	}
	if lc.DiscogsToken != "" {
		if tags, ok := a.Tools[1].(*TagsTool); ok {
			tags.discogsToken = lc.DiscogsToken
		}
	}
	if filter != "" {
		a.Filter, a.FilterDraft = filter, filter
		a.Refresh()
	}
	if snapshotPath == "" {
		a.cfgPath = lc.Path
		// macOS privacy (TCC) silently blocks Finder-launched apps from
		// reading the Music folder; detect it once and open the Full Disk
		// Access pane so the user can grant access (a system prompt cannot
		// be shown programmatically for this permission).
		if a.fdaNotice = fullDiskAccessRestricted(); a.fdaNotice {
			_ = openFullDiskAccessPane()
		}
		// Save the window size and the splitter position on exit — the only
		// main-UI values written back to the config file (and never when the
		// file was malformed).
		generic.AddExitCleanup(func() { a.saveWindowDims() })
		app.SetupWindow("Engine DJ Multi Tool", a.startupWindowWidth(), a.startupWindowHeight())
		app.Run(a.RootView)
		return
	}
	// Headless snapshot (for tests / docs).
	if len(a.Tracks) > 0 {
		a.Selected = a.Tracks[0].ID
	}
	seedSnapshotTools(a)
	if err := RenderToPNG(snapshotPath, 1800, 1000, a.RootView); err != nil {
		fmt.Fprintf(os.Stderr, "error: snapshot: %v\n", err)
		os.Exit(1)
	}
}

// seedSnapshotTools prepares tool state for documentation snapshots: the
// Relink scan is pre-run (so the capture shows proposals) and the Settings
// draft is replaced with neutral demo values — real paths and API tokens
// must never leak into committed screenshots.
func seedSnapshotTools(a *App) {
	if rt, ok := a.Tools[a.ActiveTool].(*RelinkTool); ok && a.MusicRoot != "" {
		rt.root = a.MusicRoot
		rep, missingList := rt.scanSync(a)
		rt.scanned = true
		rt.missingList = missingList
		rt.report = rep
	}
	if dt, ok := a.Tools[a.ActiveTool].(*DedupTool); ok {
		groups, total := dt.scanSync(a)
		dt.scanDone = true
		dt.groups = groups
		dt.total = total
	}
	if ct, ok := a.Tools[a.ActiveTool].(*ConfigTool); ok {
		ct.loaded = true
		ct.path = "/Users/you/Library/Application Support/enginedj5multitool/config.yaml"
		ct.draft = Config{
			Library:       "/Users/you/Music/Engine Library/Database2/m.db",
			MusicRoot:     "/Users/you/Music",
			EngineLibrary: "/Users/you/Music/Engine Library",
			Theme:         "auto",
			BrowserWidth:  660,
		}
		ct.syncWidthText()
	}
}

// startupWindowWidth / startupWindowHeight return the window size from the
// config (falling back to the built-in default when unset or implausible).
func (a *App) startupWindowWidth() int {
	cfg := LoadOrCreateConfig()
	if cfg.Err == nil && cfg.WindowWidth >= 200 && cfg.WindowWidth <= 8000 {
		return int(cfg.WindowWidth)
	}
	return 1180
}

func (a *App) startupWindowHeight() int {
	cfg := LoadOrCreateConfig()
	if cfg.Err == nil && cfg.WindowHeight >= 150 && cfg.WindowHeight <= 6000 {
		return int(cfg.WindowHeight)
	}
	return 740
}

// saveWindowDims persists the window size and the browser splitter position
// on quit — the only main-UI values written back to the config file. The
// file is re-read first so manual edits survive, and a malformed file is
// never clobbered.
func (a *App) saveWindowDims() {
	lc := LoadOrCreateConfig()
	if lc.Err != nil {
		return
	}
	ws := GetHost().WindowSize
	lc.WindowWidth = float64(ws[0])
	lc.WindowHeight = float64(ws[1])
	if w := a.splitWidth(); w >= minBrowserWidth {
		lc.BrowserWidth = w
	}
	if err := SaveConfigFile(lc.Path, lc.Config); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not save window size and splitter position: %v\n", err)
	}
}
