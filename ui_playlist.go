package main

import (
	"fmt"
	"strings"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// currentApp points at the running App while the Playlist Creator renders —
// the tool needs it in selectSmartlist, which shirei callbacks reach without
// arguments.
var currentApp *App

// ui_playlist.go: the Playlist Creator tool. Converts a dynamic playlist
// (Smartlist) into a real (static) playlist: pick the smartlist, choose the
// sort criteria and direction, pick the destination folder in the playlist
// tree, and create — the tool writes the Playlist row, the song entities and
// all the helper tables Engine DJ maintains.

// PlaylistCreatorTool converts smartlists into static playlists.
type PlaylistCreatorTool struct {
	loaded    bool
	loadErr   string
	smartlist []SmartlistInfo
	tree      []*PlaylistNode
	flat      []*PlaylistNode // tree in display order with depth

	selSmartlist int    // index into smartlist
	name         string // name of the playlist to create
	lastSuggest  string // last auto-filled name (so switching smartlists updates an unedited name)

	selCrit     string // sort criteria (SortCriteria entries); default "title"
	desc        bool   // sort direction (SegmentedControl target)
	selParent   int64  // destination folder (Playlist.id)
	parentLabel string

	tracks     []SmartlistTrack // evaluated + sorted tracks
	evalErr    string
	creating   bool
	status     string
	statusIsOk bool
}

func (t *PlaylistCreatorTool) Name() string    { return "Playlist Creator" }
func (t *PlaylistCreatorTool) Icon() IconGlyph { return SymList }

// ensureLoaded reads the smartlists and the playlist tree once.
func (t *PlaylistCreatorTool) ensureLoaded(a *App) {
	if t.loaded || a.lib == nil {
		return
	}
	t.loaded = true
	sls, err := a.lib.Smartlists()
	if err != nil {
		t.loadErr = "reading smartlists: " + err.Error()
		return
	}
	t.smartlist = sls
	if t.selCrit == "" {
		t.selCrit = "title"
	}
	tree, err := a.lib.PlaylistTree()
	if err != nil {
		t.loadErr = "reading playlists: " + err.Error()
		return
	}
	t.tree = tree
	t.flat = nil
	var flatten func(nodes []*PlaylistNode, depth int)
	flatten = func(nodes []*PlaylistNode, depth int) {
		for _, n := range nodes {
			t.flat = append(t.flat, n)
			_ = depth // depth is recomputed at render time from the prefix
			flatten(n.Children, depth+1)
		}
	}
	flatten(tree, 0)
	if len(sls) > 0 {
		t.selectSmartlist(0)
	}
}

// selectSmartlist evaluates the chosen smartlist with the current sort.
func (t *PlaylistCreatorTool) selectSmartlist(idx int) {
	t.selSmartlist = idx
	t.evalErr = ""
	t.tracks = nil
	if idx < 0 || idx >= len(t.smartlist) {
		return
	}
	a := currentApp
	if a == nil || a.lib == nil {
		return
	}
	sl := t.smartlist[idx]
	tracks, err := a.lib.EvaluateSmartlist(sl.Rules)
	if err != nil {
		t.evalErr = err.Error()
		return
	}
	SortSmartlistTracks(tracks, t.selCrit, !t.desc)
	t.tracks = tracks
	if t.name == "" || t.name == t.lastSuggest {
		t.name = sl.Title
	}
	t.lastSuggest = sl.Title
}

// reSort re-applies the current sort criteria to the evaluated tracks.
func (t *PlaylistCreatorTool) reSort() {
	SortSmartlistTracks(t.tracks, t.selCrit, !t.desc)
}

// View renders the tool: a smartlist picker, sort controls, an ordered
// preview and the destination picker with the create button.
func (t *PlaylistCreatorTool) View(a *App) {
	p := a.pal()
	currentApp = a
	t.ensureLoaded(a)

	Container(Attrs(Row, Grow(1), Expand), func() {
		a.captureSplitRow()

		Container(Attrs(FixWidth(a.splitWidth()), Expand, Clip, Gap(8)), func() {
			a.BrowserPanel(nil)
		})

		a.Splitter()

		Container(Attrs(Grow(1), Expand, Viewport, Pad2(0, 10), Gap(8)), func() {
			a.L("Playlist Creator", FontSize(a.fs(18)), FontWeight(WeightBold))
			a.wrappedText("Converts a dynamic playlist (Smartlist) into a real playlist: its tracks are evaluated once, sorted by the criteria below, and stored as a static playlist — future smartlist rule changes will not change the new playlist.",
				a.paneTextWidth(), FontSize(a.fs(11)), TextColorVec(p.textDim))

			if t.loadErr != "" {
				a.errorText("Error: "+t.loadErr, a.paneTextWidth())
				return
			}
			if a.lib == nil {
				a.L("Load a library first.", TextColorVec(p.textDim))
				return
			}
			if len(t.smartlist) == 0 {
				a.L("This library has no dynamic playlists (Smartlist table is empty).", TextColorVec(p.textDim))
				return
			}

			// Smartlist picker: a menu showing the smartlists in their
			// folder tree, with typeahead filtering.
			a.L("Dynamic playlist (Smartlist)", FontSize(a.fs(11)), TextColorVec(p.textDim))
			sl := t.smartlist[t.selSmartlist]
			Container(Attrs(Row, CrossMid, Gap(8)), func() {
				NextAccessName("plc-smartlist-menu")
				AssignAccess()
				CtrlMenuButton(SymList, sl.Title, func() { t.smartlistMenu() })
				a.L(strings.Trim(sl.Path, ";"), FontSize(a.fs(11)), TextColorVec(p.textDim))
			})

			// New playlist name (pre-filled with the smartlist title until
			// the user edits it).
			a.L("New playlist name", FontSize(a.fs(11)), TextColorVec(p.textDim))
			a.input(&t.name, DefaultTextInputAttrs())

			// Sort criteria + direction.
			a.L("Sort by", FontSize(a.fs(11)), TextColorVec(p.textDim))
			Container(Attrs(Row, CrossMid, Gap(8)), func() {
				if SegmentedControl(&t.selCrit, func() {
					for _, c := range SortCriteria {
						SegmentedCell(c, c)
					}
				}) {
					t.reSort()
				}
				if SegmentedControl(&t.desc, func() {
					SegmentedCell("Asc", false)
					SegmentedCell("Desc", true)
				}) {
					t.reSort()
				}
			})
			if t.evalErr != "" {
				a.errorText("Smartlist rules could not be evaluated: "+t.evalErr, a.paneTextWidth())
			}

			// Preview of the resulting order.
			if len(t.tracks) > 0 {
				a.L(fmt.Sprintf("Resulting order — %d track(s):", len(t.tracks)), FontSize(a.fs(11)), TextColorVec(p.textDim))
				n := len(t.tracks)
				if n > 8 {
					n = 8
				}
				for i := 0; i < n; i++ {
					tr := t.tracks[i]
					a.L(fmt.Sprintf("%2d.  %s — %s  (%.1f BPM, key %d, rating %d)",
						i+1, tr.Artist, tr.Title, tr.BPM, tr.Key, tr.Rating/20),
						FontSize(a.fs(12)))
				}
				if len(t.tracks) > n {
					a.L(fmt.Sprintf("… and %d more", len(t.tracks)-n), FontSize(a.fs(11)), TextColorVec(p.textDim))
				}
			} else if t.evalErr == "" {
				a.L("The smartlist matches no tracks.", TextColorVec(p.textDim))
			}

			// Destination picker.
			a.L("Location of the new playlist", FontSize(a.fs(11)), TextColorVec(p.textDim))
			if t.selParent == 0 {
				a.L("Pick a folder below (click a row)", FontSize(a.fs(12)), TextColorVec(p.textError))
			} else {
				a.L(fmt.Sprintf("Inside: %s", t.parentLabel), FontSize(a.fs(12)), TextColorVec(p.textOk))
			}
			Container(Attrs(Grow(1), Expand, Gap(1)), func() {
				for _, n := range t.flat {
					t.treeRow(a, n)
				}
			})

			// Create.
			Container(Attrs(Row, CrossMid, Gap(8)), func() {
				if CtrlButton(SymBoxPlus, fmt.Sprintf("Create playlist with %d tracks", len(t.tracks)), !t.creating && t.selParent != 0 && len(t.tracks) > 0) {
					t.create(a)
				}
				if t.status != "" {
					style := TextColorVec(p.textOk)
					if !t.statusIsOk {
						style = TextColorVec(p.textError)
					}
					a.L(t.status, FontSize(a.fs(12)), style)
				}
			})
			ScrollBars()
		})
	})
}

// treeRow renders one selectable row of the playlist tree.
func (t *PlaylistCreatorTool) treeRow(a *App, n *PlaylistNode) {
	p := a.pal()
	selected := t.selParent == n.ID
	NextAccessName(fmt.Sprintf("plc-tree-%d", n.ID))
	Container(Attrs(Row, CrossMid, Pad2(6, 2), Gap(6)), func() {
		AssignAccess()
		// Hovered/clicked must be read while the row is still the current
		// container (before the children change it).
		clicked := IsClicked()
		if selected || IsHovered() {
			ModAttrs(BackgroundVec(p.rowHover))
		}
		if selected {
			ModAttrs(BackgroundVec(p.rowSel))
		}
		Spacer(float32(14 * n.depth))
		if n.IsFolder {
			Icon(SymFolder, TextColorVec(p.textDim), FontSize(a.fs(13)))
		} else {
			Icon(SymList, TextColorVec(p.textDim), FontSize(a.fs(13)))
		}
		a.L(n.Title, FontSize(a.fs(12)))
		if clicked {
			// Direct mutation: the click handler runs inside the frame,
			// which already holds the render lock — WithFrameLock here
			// would deadlock the app.
			t.selParent = n.ID
			t.parentLabel = strings.Join(t.pathOf(n), " → ")
		}
	})
}

// pathOf returns the folder chain of a node (root first).
func (t *PlaylistCreatorTool) pathOf(n *PlaylistNode) []string {
	byID := map[int64]*PlaylistNode{}
	var walk func(nodes []*PlaylistNode)
	walk = func(nodes []*PlaylistNode) {
		for _, c := range nodes {
			byID[c.ID] = c
			walk(c.Children)
		}
	}
	walk(t.tree)
	var chain []string
	for node := n; node != nil; {
		chain = append([]string{node.Title}, chain...)
		node = byID[node.ParentID]
	}
	return chain
}

// create materializes the playlist and reports the outcome. It runs in a
// background goroutine — the entity inserts take noticeable time for large
// smartlists, and blocking the render frame freezes the whole app (macOS
// shows the beachball until the frame returns).
func (t *PlaylistCreatorTool) create(a *App) {
	if t.creating || a.lib == nil || t.selParent == 0 {
		return
	}
	t.creating = true
	ids := make([]int64, len(t.tracks))
	for i, tr := range t.tracks {
		ids[i] = tr.ID
	}
	name := strings.TrimSpace(t.name)
	t.status, t.statusIsOk = fmt.Sprintf("Creating %q with %d tracks…", name, len(ids)), true
	RequestNextFrame()
	go func() {
		id, err := a.lib.CreatePlaylist(name, t.selParent, ids)
		WithFrameLock(func() {
			t.creating = false
			if err != nil {
				msg := err.Error()
				if strings.Contains(msg, "UNIQUE constraint failed: Playlist.title") {
					msg = "a playlist with this name already exists in that folder"
				}
				t.status, t.statusIsOk = "Could not create the playlist: "+msg, false
				return
			}
			t.status = fmt.Sprintf("Created %q (%d tracks) in %s — reload Engine DJ to see it.",
				name, len(ids), t.parentLabel)
			t.statusIsOk = true

			// Refresh the tree so the new playlist appears in the picker.
			if tree, err := a.lib.PlaylistTree(); err == nil {
				t.tree = tree
				t.flat = nil
				var flatten func(nodes []*PlaylistNode)
				flatten = func(nodes []*PlaylistNode) {
					for _, n := range nodes {
						t.flat = append(t.flat, n)
						flatten(n.Children)
					}
				}
				flatten(tree)
				t.selParent = id
				t.parentLabel = strings.Join(t.pathOfByID(tree, id), " → ")
			}
		})
		RequestNextFrame()
	}()
}

// pathOfByID finds the folder chain of a node id in a freshly loaded tree.
func (t *PlaylistCreatorTool) pathOfByID(tree []*PlaylistNode, id int64) []string {
	byID := map[int64]*PlaylistNode{}
	var walk func(nodes []*PlaylistNode)
	walk = func(nodes []*PlaylistNode) {
		for _, c := range nodes {
			byID[c.ID] = c
			walk(c.Children)
		}
	}
	walk(tree)
	var chain []string
	for node := byID[id]; node != nil; {
		chain = append([]string{node.Title}, chain...)
		node = byID[node.ParentID]
	}
	return chain
}

// smartlistNode is one folder/leaf of the smartlist path tree.
type smartlistNode struct {
	name     string
	children []*smartlistNode
	items    []int // indices into PlaylistCreatorTool.smartlist
}

// buildSmartlistTree groups the smartlists by their parent path
// ("dynamic;House;" → dynamic ▸ House ▸ items).
func buildSmartlistTree(sls []SmartlistInfo) *smartlistNode {
	root := &smartlistNode{}
	for i, sl := range sls {
		node := root
		for _, seg := range strings.Split(sl.Path, ";") {
			if seg == "" {
				continue
			}
			var next *smartlistNode
			for _, c := range node.children {
				if c.name == seg {
					next = c
					break
				}
			}
			if next == nil {
				next = &smartlistNode{name: seg}
				node.children = append(node.children, next)
			}
			node = next
		}
		node.items = append(node.items, i)
	}
	return root
}

// smartlistMenu builds the dropdown content: the smartlists in tree format
// (folder rows, indented leaves, a tick on the selected one) with typeahead
// filtering over folder and playlist names.
func (t *PlaylistCreatorTool) smartlistMenu() {
	query := strings.ToLower(MenuFilterQuery())
	root := buildSmartlistTree(t.smartlist)
	var visible func(n *smartlistNode) bool
	visible = func(n *smartlistNode) bool {
		if query == "" {
			return true
		}
		if strings.Contains(strings.ToLower(n.name), query) {
			return true
		}
		for _, i := range n.items {
			if strings.Contains(strings.ToLower(t.smartlist[i].Title), query) {
				return true
			}
		}
		for _, c := range n.children {
			if visible(c) {
				return true
			}
		}
		return false
	}
	var walk func(n *smartlistNode, depth int)
	walk = func(n *smartlistNode, depth int) {
		for _, c := range n.children {
			if !visible(c) {
				continue
			}
			// Folder row: a pure header (the smartlists are listed below it).
			Container(Attrs(Row), func() {
				Spacer(float32(16 * depth))
				MenuItemExt(c.name, ButtonAttrs{Icon: SymFolder, Disabled: true})
			})
			for _, i := range c.items {
				if query != "" && !strings.Contains(strings.ToLower(t.smartlist[i].Title), query) &&
					!strings.Contains(strings.ToLower(c.name), query) {
					continue
				}
				icon := SymList
				if i == t.selSmartlist {
					icon = SymITick
				}
				Container(Attrs(Row), func() {
					Spacer(float32(16 * (depth + 1)))
					if MenuItem(icon, t.smartlist[i].Title) {
						t.selectSmartlist(i)
					}
				})
			}
			walk(c, depth+1)
		}
	}
	walk(root, 0)
}
