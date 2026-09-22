package main

import (
	"fmt"
	"os"
	"sort"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// DedupTool finds groups of DB entries that resolve to the same audio file
// on disk and shows how their metadata differs. Extra entries can be removed
// from the database (the audio file itself is never touched).
type DedupTool struct {
	running  bool
	done     int
	scanDone bool
	groups   []dupGroup
	total    int
}

type dupGroup struct {
	path    string // resolved audio file shared by the entries
	missing bool   // the shared file does not exist on disk
	entries []dupEntry
}

type dupEntry struct {
	rec      TrackRecord
	keep     bool // first entry of a group: the one to keep
	selected bool // checked for deletion
}

func (t *DedupTool) Name() string    { return "Dedup" }
func (t *DedupTool) Icon() IconGlyph { return SymCopy }

func (t *DedupTool) View(a *App) {
	p := a.pal()
	Container(Attrs(Row, Grow(1), Expand), func() {
		a.captureSplitRow()

		Container(Attrs(FixWidth(a.splitWidth()), Expand, Clip, Gap(8)), func() {
			a.BrowserPanel(nil)
		})

		a.Splitter()

		Container(Attrs(Grow(1), Expand, Viewport, Pad2(0, 10), Gap(8)), func() {
			a.L("Dedup", FontSize(18), FontWeight(WeightBold))
			a.wrappedText("Finds groups of library entries that point to the same audio file on disk, and shows how their metadata (rating, key, track number) differs. The first entry of each group is kept; extras can be removed from the database.",
				a.paneTextWidth(), FontSize(11), TextColorVec(p.textDim))

			Container(Attrs(Row, CrossMid, Gap(10), Pad2(6, 0)), func() {
				if CtrlButton(SymSearch, "Find duplicates", a.lib != nil && !t.running) {
					t.startScan(a)
				}
				if t.running {
					Container(Attrs(Row, CrossMid, Gap(6)), func() {
						BusyDots()
						a.L(fmt.Sprintf("scanning — %d/%d", t.done, t.total),
							FontSize(12), TextColorVec(p.textDim))
					})
				}
			})

			t.ReportPanel(a)
			ScrollBars()
		})
	})
}

func (t *DedupTool) startScan(a *App) {
	if t.running || a.lib == nil {
		return
	}
	t.running = true
	t.scanDone = false
	go func() {
		groups, total := t.scanSync(a)
		WithFrameLock(func() {
			t.running = false
			t.scanDone = true
			t.groups = groups
			t.total = total
		})
		RequestNextFrame()
	}()
}

// scanSync resolves every track's audio path and groups entries that share
// the same file on disk.
func (t *DedupTool) scanSync(a *App) ([]dupGroup, int) {
	byPath := map[string][]dupEntry{}
	var order []string
	missing := map[string]bool{}

	for i, rec := range a.Tracks {
		if i%500 == 0 {
			WithFrameLock(func() {
				t.done = i
			})
		}
		if rec.Path == "" {
			continue
		}
		path := ResolveMediaPath(a.lib.Dir, a.MusicRoot, rec.Path)
		if _, ok := byPath[path]; !ok {
			order = append(order, path)
		}
		byPath[path] = append(byPath[path], dupEntry{rec: rec})
		if st, err := os.Stat(path); err != nil || st.IsDir() {
			missing[path] = true
		}
	}

	var groups []dupGroup
	sort.Strings(order)
	for _, path := range order {
		entries := byPath[path]
		if len(entries) < 2 {
			continue
		}
		entries[0].keep = true
		groups = append(groups, dupGroup{path: path, missing: missing[path], entries: entries})
	}
	return groups, len(a.Tracks)
}

// selectedCount returns the number of extra entries checked for deletion.
func (t *DedupTool) selectedCount() int {
	n := 0
	for gi := range t.groups {
		for ei := range t.groups[gi].entries {
			e := &t.groups[gi].entries[ei]
			if !e.keep && e.selected {
				n++
			}
		}
	}
	return n
}

// deleteSelected removes the checked extra entries from the Engine DJ
// database and updates the session's track list.
func (t *DedupTool) deleteSelected(a *App) {
	if t.running || a.lib == nil {
		return
	}
	deleted, failed := 0, 0
	removeFromSession := map[int64]bool{}

	for gi := range t.groups {
		g := &t.groups[gi]
		var kept []dupEntry
		for _, e := range g.entries {
			if e.keep || !e.selected {
				kept = append(kept, e)
				continue
			}
			if err := a.lib.DeleteTrack(e.rec.ID); err != nil {
				failed++
				kept = append(kept, e)
				continue
			}
			deleted++
			removeFromSession[e.rec.ID] = true
		}
		g.entries = kept
	}

	// Drop deleted tracks from the session list and from the groups display.
	var session []TrackRecord
	for _, r := range a.Tracks {
		if !removeFromSession[r.ID] {
			session = append(session, r)
		}
	}
	a.Tracks = session
	a.TrackCount = len(session)

	var keptGroups []dupGroup
	for _, g := range t.groups {
		if len(g.entries) > 1 {
			keptGroups = append(keptGroups, g)
		}
	}
	t.groups = keptGroups

	Toast(SymITick, "Dedup", fmt.Sprintf("%d duplicate entr%s removed from the Engine DJ database%s",
		deleted, plural(deleted), failNote(failed)))
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func failNote(failed int) string {
	if failed > 0 {
		return fmt.Sprintf(", %d failed", failed)
	}
	return ""
}

// ReportPanel renders the duplicate groups (interactive) or scan progress.
func (t *DedupTool) ReportPanel(a *App) {
	p := a.pal()
	if t.running && !t.scanDone {
		return // progress is shown in the button row
	}
	if !t.scanDone {
		a.L("Press \"Find duplicates\" to scan the library.", FontSize(12), TextColorVec(p.textDim))
		return
	}

	extra := 0
	for _, g := range t.groups {
		extra += len(g.entries) - 1
	}
	a.L("Last scan", FontWeight(WeightBold), FontSize(14))
	a.L(fmt.Sprintf("%d track(s) scanned: %d group(s) of identical files, %d extra entrie%s.",
		t.total, len(t.groups), extra, plural(extra)),
		FontSize(12), TextColorVec(p.textDim))
	if len(t.groups) == 0 {
		a.L("No duplicates found — every song file is referenced by a single entry.",
			FontSize(12), TextColorVec(p.textOk))
		return
	}
	if sel := t.selectedCount(); sel > 0 {
		a.L(fmt.Sprintf("%d entr%s checked for deletion.", sel, plural(sel)),
			FontSize(12), TextColorVec(a.pal().textError))
	}

	shown := 0
	for gi := range t.groups {
		g := &t.groups[gi]
		if shown >= 10 {
			a.L(fmt.Sprintf("… and %d more group(s)", len(t.groups)-shown),
				FontSize(12), TextColorVec(p.textDim))
			break
		}
		shown++

		Container(Attrs(Gap(2), Pad2(6, 0)), func() {
			Container(Attrs(Row, CrossMid, Gap(6)), func() {
				if g.missing {
					a.L("missing", FontSize(10), TextColorVec(p.textError))
				}
				a.L(g.path, FontSize(11), FontWeight(WeightBold))
			})
			for ei := range g.entries {
				e := &g.entries[ei]
				entry := e
				stars := int(e.rec.Rating / 20)
				if stars > 5 {
					stars = 5
				}
				ki := keyInfoFor(e.rec.Key)
				key := "—"
				if ki.Live {
					key = ki.Code
				}
				Container(Attrs(Row, CrossMid, Gap(6), Pad4(1, 0, 0, 12)), func() {
					if e.keep {
						a.L("keep", FontSize(11), TextColor(45, 85, 52, 1))
					} else {
						CheckBox(&entry.selected, "")
					}
					a.L(fmt.Sprintf("#%d  %s — %s", e.rec.ID, e.rec.Artist, e.rec.Title), FontSize(12))
					a.L(fmt.Sprintf("%d★", stars), FontSize(11), TextColorVec(p.textDim))
					a.L(key, FontSize(11), TextColorVec(p.textDim))
				})
			}
		})
	}
}
