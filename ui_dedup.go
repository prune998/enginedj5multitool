package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// DedupTool finds duplicate library entries — groups that resolve to the
// same audio file on disk, and songs that carry the same artist & title
// tags but live in different files — and shows how their metadata (path,
// rating, key) differs. Extra entries can be removed from the database
// (the audio files themselves are never touched).
type DedupTool struct {
	running  bool
	done     int
	scanDone bool
	groups   []dupGroup
	total    int
}

type dupGroup struct {
	byTags  bool   // false: entries share one audio file (path); true: same artist & title tags in different files
	path    string // file kind: the resolved audio file shared by the entries
	missing bool   // file kind: the shared file does not exist on disk
	entries []dupEntry
}

type dupEntry struct {
	rec         TrackRecord
	path        string // resolved audio file of this entry
	missing     bool   // this entry's file does not exist on disk
	fileEntries int    // same-tags groups: DB entries on this file (1 = just this one)
	keep        bool   // first entry of a group: the one to keep
	selected    bool   // checked for deletion
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
			a.wrappedText("Finds duplicate library entries: groups that point to the same audio file on disk, and songs that carry the same artist & title tags but live in different files. The first entry of each group is kept; extras can be removed from the database (the audio files themselves are never touched).",
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

// scanSync resolves every track's audio path and builds two kinds of
// duplicate groups: entries that share the same file on disk, and songs
// with the same artist & title tags that live in different files.
func (t *DedupTool) scanSync(a *App) ([]dupGroup, int) {
	byPath := map[string][]dupEntry{}
	var pathOrder []string
	missing := map[string]bool{}

	byTags := map[string][]dupEntry{}
	var tagOrder []string

	for i, rec := range a.Tracks {
		if i%500 == 0 {
			WithFrameLock(func() {
				t.done = i
			})
		}
		if rec.Path == "" {
			continue
		}
		path := canonicalPath(a.lib.ResolveMedia(rec.Path))
		isMissing := false
		if st, err := os.Stat(path); err != nil || st.IsDir() {
			missing[path] = true
			isMissing = true
		}
		if _, ok := byPath[path]; !ok {
			pathOrder = append(pathOrder, path)
		}
		byPath[path] = append(byPath[path], dupEntry{rec: rec, path: path, missing: isMissing})

		if key := tagDupKey(rec.Artist, rec.Title); key != "" {
			if _, ok := byTags[key]; !ok {
				tagOrder = append(tagOrder, key)
			}
			byTags[key] = append(byTags[key], dupEntry{rec: rec, path: path, missing: isMissing})
		}
	}

	var groups []dupGroup
	sort.Strings(pathOrder)
	for _, path := range pathOrder {
		entries := byPath[path]
		if len(entries) < 2 {
			continue
		}
		entries[0].keep = true
		groups = append(groups, dupGroup{path: path, missing: missing[path], entries: entries})
	}
	sort.Strings(tagOrder)
	for _, key := range tagOrder {
		entries := byTags[key]
		if len(entries) < 2 {
			continue
		}
		// Display (and keeper) order follows the file path so groups are
		// deterministic across scans.
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
		// One representative per distinct file: entries sharing a path are
		// already reported as a same-file group — a same-tags group is about
		// one song living in different files. The representative is the
		// same entry the file group keeps (first in library order).
		var reps []dupEntry
		for _, e := range entries {
			if n := len(reps); n > 0 && reps[n-1].path == e.path {
				reps[n-1].fileEntries++
				continue
			}
			reps = append(reps, e)
		}
		if len(reps) < 2 {
			continue // one file only: fully covered by the file-duplicate view
		}
		reps[0].keep = true
		groups = append(groups, dupGroup{byTags: true, entries: reps})
	}
	return groups, len(a.Tracks)
}

// tagDupKey normalizes an artist/title pair into a same-song key: both must
// be present, case is folded and whitespace collapsed so "The Beatless" and
// "the  Beatless " group together. An empty result means "cannot match" —
// entries without a real artist or title are never tag-duplicates.
func tagDupKey(artist, title string) string {
	artist, title = strings.TrimSpace(artist), strings.TrimSpace(title)
	if artist == "" || title == "" {
		return ""
	}
	return strings.Join(strings.Fields(strings.ToLower(artist+" "+title)), " ")
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
// database and updates the session's track list. One entry can be an extra
// in two groups at once (same file AND same artist & title), so each track
// is deleted exactly once.
func (t *DedupTool) deleteSelected(a *App) {
	if t.running || a.lib == nil {
		return
	}
	selected := map[int64]bool{}
	for gi := range t.groups {
		g := &t.groups[gi]
		for _, e := range g.entries {
			if !e.keep && e.selected {
				selected[e.rec.ID] = true
			}
		}
	}

	deleted, failed := 0, 0
	deletedIDs := map[int64]bool{}
	for id := range selected {
		if err := a.lib.DeleteTrack(id); err != nil {
			failed++
			continue
		}
		deleted++
		deletedIDs[id] = true
	}

	// Drop deleted tracks from the session list and from the groups display.
	var session []TrackRecord
	for _, r := range a.Tracks {
		if !deletedIDs[r.ID] {
			session = append(session, r)
		}
	}
	a.Tracks = session
	a.TrackCount = len(session)

	var keptGroups []dupGroup
	for _, g := range t.groups {
		var kept []dupEntry
		for _, e := range g.entries {
			if !deletedIDs[e.rec.ID] {
				kept = append(kept, e)
			}
		}
		if len(kept) > 1 {
			g.entries = kept
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

	fileGroups, tagGroups := 0, 0
	extra := 0
	for _, g := range t.groups {
		if g.byTags {
			tagGroups++
		} else {
			fileGroups++
		}
		extra += len(g.entries) - 1
	}
	a.L("Last scan", FontWeight(WeightBold), FontSize(14))
	a.wrappedText(fmt.Sprintf("%d track(s) scanned: %d group(s) of identical files, %d group(s) of same artist & title (different files), %d extra entr%s.",
		t.total, fileGroups, tagGroups, extra, plural(extra)),
		a.paneTextWidth(), FontSize(12), TextColorVec(p.textDim))
	if len(t.groups) == 0 {
		a.L("No duplicates found — every song file is referenced by a single entry, and no two entries share the same artist & title.",
			FontSize(12), TextColorVec(p.textOk))
		return
	}
	if sel := t.selectedCount(); sel > 0 {
		a.L(fmt.Sprintf("%d entr%s checked for deletion.", sel, plural(sel)),
			FontSize(12), TextColorVec(a.pal().textError))
	}
	Container(Attrs(Row, CrossMid, Gap(10), Pad2(4, 0)), func() {
		if CtrlButton(SymBan, fmt.Sprintf("Delete selected (%d)", t.selectedCount()),
			!t.running && t.selectedCount() > 0) {
			t.deleteSelected(a)
		}
		if CtrlButton(SymSearch, "Re-scan", !t.running) {
			t.startScan(a)
		}
	})

	// Two sections with independent display caps: the report renders inline
	// (no virtualization), so each kind shows its first groups plus a
	// "more" line — otherwise thousands of file groups would hide the
	// same-tags section entirely.
	const maxShownPerKind = 10

	fileShown := 0
	if fileGroups > 0 {
		a.L(fmt.Sprintf("Identical files (%d)", fileGroups), FontWeight(WeightBold), FontSize(13))
	}
	for gi := range t.groups {
		g := &t.groups[gi]
		if g.byTags {
			continue
		}
		if fileShown >= maxShownPerKind {
			a.L(fmt.Sprintf("… and %d more group(s)", fileGroups-fileShown),
				FontSize(12), TextColorVec(p.textDim))
			break
		}
		fileShown++
		t.renderGroup(a, g)
	}

	tagShown := 0
	if tagGroups > 0 {
		a.L(fmt.Sprintf("Same artist & title — different files (%d)", tagGroups),
			FontWeight(WeightBold), FontSize(13))
	}
	for gi := range t.groups {
		g := &t.groups[gi]
		if !g.byTags {
			continue
		}
		if tagShown >= maxShownPerKind {
			a.L(fmt.Sprintf("… and %d more group(s)", tagGroups-tagShown),
				FontSize(12), TextColorVec(p.textDim))
			break
		}
		tagShown++
		t.renderGroup(a, g)
	}
}

// renderGroup draws one duplicate group: a header (the shared audio file
// for file groups, the song for same-tags groups) and its entries with
// keep/delete controls.
func (t *DedupTool) renderGroup(a *App, g *dupGroup) {
	p := a.pal()
	Container(Attrs(Gap(2), Pad2(6, 0)), func() {
		Container(Attrs(Row, CrossMid, Gap(6)), func() {
			if g.byTags {
				a.L(fmt.Sprintf("%s — %s", g.entries[0].rec.Artist, g.entries[0].rec.Title),
					FontSize(11), FontWeight(WeightBold))
			} else {
				if g.missing {
					a.L("missing", FontSize(10), TextColorVec(p.textError))
				}
				a.L(g.path, FontSize(11), FontWeight(WeightBold))
			}
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
				a.L(fmt.Sprintf("#%d", e.rec.ID), FontSize(12))
				if g.byTags {
					if e.missing {
						a.L("missing", FontSize(10), TextColorVec(p.textError))
					}
					a.L(e.path, FontSize(11), TextColorVec(p.textDim))
					if e.fileEntries > 0 {
						a.L(fmt.Sprintf("+%d on this file", e.fileEntries), FontSize(10), TextColorVec(p.textDim))
					}
				} else {
					a.L(fmt.Sprintf("%s — %s", e.rec.Artist, e.rec.Title), FontSize(12))
				}
				a.L(fmt.Sprintf("%d★", stars), FontSize(11), TextColorVec(p.textDim))
				a.L(key, FontSize(11), TextColorVec(p.textDim))
			})
		}
	})
}
