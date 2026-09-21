package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// RelinkTool scans the library for tracks whose audio file is missing and
// tries to find the moved file under a root folder, matching on artist,
// album, title and file size. Relinking updates Track.path in the database.
//
// The flow is two-stage: the first click of "Auto relink" discovers the
// missing files, searches the root folder and PROPOSES a diff (old path →
// new path) per track, each with a checkbox; the next step applies the
// checked proposals ("Relink selected") or all of them ("Relink all").
// Afterwards the next click re-scans to confirm.
type RelinkTool struct {
	root        string
	prefilled   bool
	running     bool
	phase       string // "scan" | "index" | "search" | "relink"
	done        int
	total       int
	scanned     bool           // a scan has listed the missing tracks
	missingList []missingTrack // retained from the scan for the relink stage
	report      *relinkReport
}

type relinkReport struct {
	scan      bool // true = discovery + proposal report
	total     int
	found     int
	missing   int
	proposed  int // relinkable proposals
	ambiguous int
	noMatch   int
	relinked  int
	skipped   int
	failed    int
	lines     []reportLine
}

type missingTrack struct {
	rec           TrackRecord
	old           string     // last known path
	propose       *fileEntry // best candidate from the search (nil = none)
	ambiguous     bool       // several equally good matches
	selected      bool       // proposal checked for relinking
	noMatch       bool       // no candidate found anywhere
	proposeDelete bool       // checked: remove the track from the Engine DJ DB
}

type fileEntry struct {
	path string
	base string // lowercased file name (with extension)
	dir  string // lowercased parent directory
	size int64
}

func (t *RelinkTool) Name() string    { return "Relink" }
func (t *RelinkTool) Icon() IconGlyph { return SymLink }

// defaultRelinkRoot guesses the Music app folder for the relink search.
func defaultRelinkRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, c := range []string{
		filepath.Join(home, "Music", "Music", "Media", "Music"), // Music.app (macOS)
		filepath.Join(home, "Music", "Music", "Media"),
		filepath.Join(home, "Music"),
	} {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return filepath.Join(home, "Music")
}

func (t *RelinkTool) View(a *App) {
	p := a.pal()
	Container(Attrs(Row, Grow(1), Expand), func() {
		a.captureSplitRow()

		Container(Attrs(FixWidth(a.splitWidth()), Expand, Clip, Gap(8)), func() {
			a.BrowserPanel(nil)
		})

		a.Splitter()

		Container(Attrs(Grow(1), Expand, Viewport, Pad2(0, 10), Gap(8)), func() {
			a.L("Relink", FontSize(18), FontWeight(WeightBold))
			a.wrappedText("Scans the library for tracks whose audio file has gone missing, then searches the root folder below for the moved file — matching on file name, artist, album, title and file size — and points the database at the new location.",
				a.paneTextWidth(), FontSize(11), TextColorVec(p.textDim))

			Container(Attrs(Gap(2), Pad2(8, 0)), func() {
				a.L("Root folder to search", FontSize(11), TextColorVec(p.textDim))
				if !t.prefilled {
					t.prefilled = true
					if strings.TrimSpace(t.root) == "" {
						t.root = defaultRelinkRoot()
					}
				}
				a.input(&t.root, DefaultTextInputAttrs())
			})

			Container(Attrs(Row, CrossMid, Gap(10), Pad2(4, 0)), func() {
				if !t.scanned {
					if CtrlButton(SymLink, "Auto relink", a.lib != nil && !t.running) {
						t.startScan(a)
					}
				} else {
					allN, selN, delN := t.proposalCount(), t.selectedCount(), t.deleteCount()
					if CtrlButton(SymLink, fmt.Sprintf("Relink all (%d)", allN), !t.running && allN > 0) {
						t.relinkAll(a)
					}
					if CtrlButton(SymITick, fmt.Sprintf("Relink selected (%d)", selN), !t.running && selN > 0) {
						t.relinkSelected(a)
					}
					if CtrlButton(SymBan, fmt.Sprintf("Delete selected (%d)", delN), !t.running && delN > 0) {
						t.deleteSelected(a)
					}
				}
				if t.running {
					Container(Attrs(Row, CrossMid, Gap(6)), func() {
						BusyDots()
						a.L(fmt.Sprintf("%s — %d/%d", t.phase, t.done, t.total),
							FontSize(12), TextColorVec(p.textDim))
					})
				}
			})

			t.ReportPanel(a)
			ScrollBars()
		})
	})
}

// proposalCount returns the number of relinkable proposals from the scan.
func (t *RelinkTool) proposalCount() int {
	n := 0
	for i := range t.missingList {
		if t.missingList[i].propose != nil {
			n++
		}
	}
	return n
}

// deleteCount returns the number of no-match tracks checked for deletion.
func (t *RelinkTool) deleteCount() int {
	n := 0
	for i := range t.missingList {
		if t.missingList[i].noMatch && t.missingList[i].proposeDelete {
			n++
		}
	}
	return n
}

// selectedCount returns the number of checked proposals.
func (t *RelinkTool) selectedCount() int {
	n := 0
	for i := range t.missingList {
		if t.missingList[i].propose != nil && t.missingList[i].selected {
			n++
		}
	}
	return n
}

func (t *RelinkTool) startScan(a *App) {
	if t.running || a.lib == nil {
		return
	}
	t.running = true
	t.report = nil
	go func() {
		rep, missingList := t.scanSync(a)
		WithFrameLock(func() {
			t.running = false
			t.scanned = true
			t.missingList = missingList
			t.report = rep
		})
		RequestNextFrame()
	}()
}

func (t *RelinkTool) relinkAll(a *App) {
	for i := range t.missingList {
		if t.missingList[i].propose != nil {
			t.missingList[i].selected = true
		}
	}
	t.startRelink(a)
}

func (t *RelinkTool) relinkSelected(a *App) {
	t.startRelink(a)
}

// deleteSelected removes the no-match tracks checked for deletion from the
// Engine DJ database and the session's track list.
func (t *RelinkTool) deleteSelected(a *App) {
	if t.running || a.lib == nil {
		return
	}
	deleted, failed := 0, 0
	var kept []missingTrack
	for i := range t.missingList {
		m := &t.missingList[i]
		if !m.noMatch || !m.proposeDelete {
			kept = append(kept, *m)
			continue
		}
		if err := a.lib.DeleteTrack(m.rec.ID); err != nil {
			failed++
			kept = append(kept, *m)
			continue
		}
		deleted++
		for j := range a.Tracks {
			if a.Tracks[j].ID == m.rec.ID {
				a.Tracks = append(a.Tracks[:j], a.Tracks[j+1:]...)
				break
			}
		}
	}
	t.missingList = kept
	t.total = len(a.Tracks)
	if t.report != nil {
		t.report.total = len(a.Tracks)
	}
	Toast(SymITick, "Deleted from Engine DJ", fmt.Sprintf("%d track(s) removed%s", deleted, map[bool]string{true: fmt.Sprintf(", %d failed", failed), false: ""}[failed > 0]))
	a.TrackCount = len(a.Tracks)
}

func (t *RelinkTool) startRelink(a *App) {
	if t.running || a.lib == nil {
		return
	}
	t.running = true
	t.report = nil
	missing := t.missingList
	go func() {
		rep := t.relinkSync(a, missing)
		WithFrameLock(func() {
			t.running = false
			t.scanned = false // next click re-scans to confirm
			t.missingList = nil
			t.report = rep
		})
		RequestNextFrame()
	}()
}

// audioExt reports whether a file name looks like an audio file the players
// support.
func audioExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp3", ".m4a", ".aiff", ".aif", ".wav", ".flac", ".ogg":
		return true
	}
	return false
}

func normKey(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// scoreFile rates how well a file on disk matches a track's metadata.
func scoreFile(e fileEntry, rec TrackRecord) int {
	title := normKey(stripBrackets(rec.Title))
	artist := normKey(rec.Artist)
	album := normKey(stripBrackets(rec.Album))
	base := e.base
	score := 0

	if old := strings.ToLower(filepath.Base(rec.Path)); base == old {
		score += 100 // same file name as before
	}
	if title != "" && base == title+strings.ToLower(filepath.Ext(e.path)) {
		score += 90 // exactly "<title>.<ext>"
	}
	if title != "" && len(title) > 3 && strings.Contains(base, title) {
		score += 40
	}
	if artist != "" && len(artist) > 3 && strings.Contains(e.dir, artist) {
		score += 20
	}
	if album != "" && len(album) > 3 && strings.Contains(e.dir, album) {
		score += 10
	}
	if artist != "" && len(artist) > 3 && strings.Contains(base, artist) {
		score += 10
	}
	if rec.FileBytes > 0 && e.size > 0 {
		diff := e.size - rec.FileBytes
		if diff < 0 {
			diff = -diff
		}
		if diff*100/rec.FileBytes <= 2 {
			score += 30 // same size (±2%): strong signal
		}
	}
	if rec.Path != "" && strings.EqualFold(filepath.Ext(e.path), filepath.Ext(rec.Path)) {
		score += 10 // same audio format
	}
	return score
}

// scanSync is stage 1: discover the tracks with missing files and, when a
// root folder is given, search it and propose a new path per track (checked
// by default).
func (t *RelinkTool) scanSync(a *App) (*relinkReport, []missingTrack) {
	rep := &relinkReport{scan: true, total: len(a.Tracks)}
	setProgress := func(phase string, done, total int) {
		WithFrameLock(func() {
			t.phase, t.done, t.total = phase, done, total
		})
		RequestNextFrame()
	}

	// Discover missing audio files.
	var missing []missingTrack
	for i, rec := range a.Tracks {
		setProgress("scan", i, len(a.Tracks))
		if rec.Path == "" {
			continue
		}
		path := ResolveMediaPath(a.lib.Dir, a.MusicRoot, rec.Path)
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			rep.found++
			continue
		}
		rep.missing++
		missing = append(missing, missingTrack{rec: rec, old: rec.Path})
	}
	if rep.missing == 0 {
		return rep, nil
	}

	// Index the root folder (when a valid one is given) and propose a new
	// path per missing track.
	root := strings.TrimSpace(t.root)
	rootOK := false
	var index []fileEntry
	if root == "" {
		rep.lines = append(rep.lines, reportLine{
			text:  "No root folder set — set one above, then press Relink all.",
			style: "",
		})
	} else if st, err := os.Stat(root); err != nil || !st.IsDir() {
		rep.lines = append(rep.lines, reportLine{
			text:  fmt.Sprintf("Root folder not found: %s", root),
			style: "err",
		})
	} else {
		rootOK = true
		fileCount := 0
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && path != root {
					return fs.SkipDir
				}
				return nil
			}
			if !audioExt(d.Name()) {
				return nil
			}
			if st, err := d.Info(); err == nil {
				index = append(index, fileEntry{
					path: path,
					base: strings.ToLower(d.Name()),
					dir:  strings.ToLower(filepath.Base(filepath.Dir(path))),
					size: st.Size(),
				})
			}
			fileCount++
			if fileCount%500 == 0 {
				setProgress("search", fileCount, fileCount+1)
			}
			return nil
		})
	}

	for i := range missing {
		m := &missing[i]
		if !rootOK {
			continue
		}
		type scored struct {
			e     fileEntry
			score int
		}
		var cands []scored
		for _, e := range index {
			if s := scoreFile(e, m.rec); s >= 60 {
				cands = append(cands, scored{e, s})
			}
		}
		if len(cands) == 0 {
			m.noMatch = true
			rep.noMatch++
			continue
		}
		sort.Slice(cands, func(a, b int) bool { return cands[a].score > cands[b].score })
		if len(cands) > 1 && cands[1].score == cands[0].score {
			m.ambiguous = true
			rep.ambiguous++
			continue
		}
		m.propose = &cands[0].e
		m.selected = true
		rep.proposed++
	}
	return rep, missing
}

// relinkSync applies the checked proposals.
func (t *RelinkTool) relinkSync(a *App, missing []missingTrack) *relinkReport {
	rep := &relinkReport{total: len(a.Tracks), missing: len(missing)}
	setProgress := func(phase string, done, total int) {
		WithFrameLock(func() {
			t.phase, t.done, t.total = phase, done, total
		})
		RequestNextFrame()
	}
	for i, m := range missing {
		setProgress("relink", i, len(missing))
		if m.propose == nil {
			continue // no match / ambiguous: nothing to relink
		}
		if !m.selected {
			rep.skipped++
			continue
		}
		// The proposal may have vanished since the scan.
		if st, err := os.Stat(m.propose.path); err != nil || st.IsDir() {
			rep.failed++
			rep.lines = append(rep.lines, reportLine{
				text:  fmt.Sprintf("#%d: proposed file vanished: %s", m.rec.ID, m.propose.path),
				style: "err",
			})
			continue
		}
		if err := a.lib.SetTrackPath(m.rec.ID, m.propose.path); err != nil {
			rep.failed++
			rep.lines = append(rep.lines, reportLine{
				text:  fmt.Sprintf("#%d: DB update failed: %v", m.rec.ID, err),
				style: "err",
			})
			continue
		}
		rep.relinked++
		rep.lines = append(rep.lines, reportLine{
			text:  fmt.Sprintf("#%d %s — %s: relinked to %s", m.rec.ID, m.rec.Artist, m.rec.Title, m.propose.path),
			style: "ok",
		})
	}
	return rep
}

// ReportPanel shows the scan proposals (interactive) or the last run's
// results.
func (t *RelinkTool) ReportPanel(a *App) {
	p := a.pal()
	w := a.paneTextWidth()

	if t.report != nil && !t.report.scan {
		rep := t.report
		a.L("Last run", FontWeight(WeightBold), FontSize(14))
		a.L(fmt.Sprintf("%d track(s) checked: %d missing, %d relinked, %d skipped (not selected), %d failed.",
			rep.total, rep.missing, rep.relinked, rep.skipped, rep.failed),
			FontSize(12), TextColorVec(p.textDim))
		a.reportLines(rep.lines, w)
		return
	}

	// Scan results: interactive proposal blocks (only after a scan).
	if !t.scanned {
		return
	}
	rep := t.report
	a.L("Missing files", FontWeight(WeightBold), FontSize(14))
	a.L(fmt.Sprintf("%d track(s) checked: %d found, %d missing. Proposals: %d relinkable, %d ambiguous, %d without match.",
		rep.total, rep.found, rep.missing, rep.proposed, rep.ambiguous, rep.noMatch),
		FontSize(12), TextColorVec(p.textDim))
	a.L("Check the proposals to keep, then press Relink selected — or Relink all. No-match tracks can be checked for deletion from the Engine DJ database.",
		FontSize(11), TextColorVec(p.textDim))

	missingShown := 0
	for i := range t.missingList {
		m := &t.missingList[i]
		if missingShown >= 10 {
			a.L(fmt.Sprintf("… and %d more missing track(s)", rep.missing-missingShown),
				FontSize(12), TextColorVec(p.textDim))
			break
		}
		missingShown++

		Container(Attrs(Gap(2), Pad2(4, 0)), func() {
			Container(Attrs(Row, CrossMid, Gap(6)), func() {
				if m.propose != nil {
					CheckBox(&m.selected, "")
				} else if m.noMatch {
					CheckBox(&m.proposeDelete, "")
				}
				a.L(fmt.Sprintf("#%d %s — %s", m.rec.ID, m.rec.Artist, m.rec.Title),
					FontSize(12), FontWeight(WeightBold), TextColorVec(p.textError))
			})
			a.errorText("    missing: "+m.old, w)
			if m.propose != nil {
				a.wrappedText("    proposal: "+m.propose.path, w, FontSize(12), TextColorVec(p.textOk))
			} else if m.ambiguous {
				a.errorText("    multiple equally good matches — skipped", w)
			} else {
				a.errorText("    no match found — check to delete this track from the Engine DJ database", w)
			}
		})
		Container(Attrs(FixHeight(1), Expand, BackgroundVec(p.grabber)), func() {})
	}
}
