package main

import (
	"fmt"
	"os"
	"strings"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// GlobalTool applies bulk comment changes to every track matching the current
// filter: re-order the #tags and add #cued / #looped when a track has more
// than one cue or loop.
type GlobalTool struct {
	sortTags bool
	addTags  bool
	alsoDB   bool
	dryRun   bool

	lastRun *globalReport
}

type globalReport struct {
	dryRun    bool
	changed   int
	unchanged int
	failed    int
	skipped   int
	lines     []reportLine
}

func (t *GlobalTool) Name() string    { return "Global Edit" }
func (t *GlobalTool) Icon() IconGlyph { return SymList }

func (t *GlobalTool) View(a *App) {
	Container(Attrs(Row, Grow(1), Expand), func() {
		a.captureSplitRow()

		// Middle: track browser, resizable via the splitter.
		Container(Attrs(FixWidth(a.splitWidth()), Expand, Clip, Gap(8)), func() {
			a.BrowserPanel(nil)
		})

		a.Splitter()

		// Right: global edit content, scrolling when taller than the pane.
		Container(Attrs(Grow(1), Expand, Viewport, Pad2(0, 10), Gap(8)), func() {
			a.L("Global Edit", FontSize(a.fs(18)), FontWeight(WeightBold))
			a.L(fmt.Sprintf("Applies to the %d track(s) currently shown in the list (use the search box to narrow it down).",
				a.TrackCount), FontSize(a.fs(11)), TextColorVec(a.pal().textDim))

			Container(Attrs(Gap(6), Pad2(8, 0)), func() {
				CheckBox(&t.sortTags, "Re-order the comment #tags alphabetically")
				CheckBox(&t.addTags, "Add #cued / #looped when a track has more than one cue or loop")
				CheckBox(&t.alsoDB, "Also update the comment in the Engine DJ database")
				CheckBox(&t.dryRun, "Dry run (report only, no writes)")
			})

			Container(Attrs(Row, CrossMid, Gap(10), Pad2(4, 0)), func() {
				enabled := a.lib != nil && a.TrackCount > 0
				if CtrlButton(SymITick, fmt.Sprintf("Apply to filtered (%d)", a.TrackCount), enabled) {
					t.Apply(a)
				}
			})

			t.ReportPanel(a)
			ScrollBars()
		})
	})
}

// Apply runs the enabled transformations over the filtered tracks.
func (t *GlobalTool) Apply(a *App) (changed, unchanged, failed, skipped int) {
	rep := &globalReport{dryRun: t.dryRun}
	for _, rec := range a.Tracks {
		if !IsTaggable(rec) {
			skipped++
			continue
		}
		d, err := a.lib.LoadDetail(rec)
		if err != nil {
			failed++
			rep.lines = append(rep.lines, reportLine{fmt.Sprintf("#%d: %v", rec.ID, err), "err"})
			continue
		}
		addApplicable := t.addTags && (countSetCues(d) > 1 || countSetLoops(d) > 1)
		if !t.sortTags && !addApplicable {
			// Nothing would change: skip the file entirely.
			unchanged++
			continue
		}

		path := a.lib.ResolveMedia(rec.Path)
		if st, err := os.Stat(path); err != nil || st.IsDir() {
			failed++
			rep.lines = append(rep.lines, reportLine{fmt.Sprintf("#%d: file not found", rec.ID), "err"})
			continue
		}
		tags, err := ReadMediaTags(path)
		if err != nil {
			failed++
			rep.lines = append(rep.lines, reportLine{fmt.Sprintf("#%d: unreadable tags", rec.ID), "err"})
			continue
		}

		newComment := tags.Comment
		var notes []string
		if addApplicable {
			if countSetCues(d) > 1 {
				if s := addTag(newComment, "cued"); s != newComment {
					newComment = s
					notes = append(notes, "+#cued")
				}
			}
			if countSetLoops(d) > 1 {
				if s := addTag(newComment, "looped"); s != newComment {
					newComment = s
					notes = append(notes, "+#looped")
				}
			}
		}
		if t.sortTags {
			if s := sortCommentTags(newComment); s != newComment {
				newComment = s
				notes = append(notes, "sorted")
			}
		}
		if newComment == tags.Comment {
			unchanged++
			continue
		}

		if t.dryRun {
			changed++
			rep.lines = append(rep.lines, reportLine{fmt.Sprintf("#%d: %s → %q", rec.ID, strings.Join(notes, ", "), newComment), ""})
			continue
		}

		tags.Comment = newComment
		rating := rec.Rating
		if err := SaveMediaTagsFull(path, tags, nil, &rating); err != nil {
			failed++
			rep.lines = append(rep.lines, reportLine{fmt.Sprintf("#%d: write failed: %v", rec.ID, err), "err"})
			continue
		}
		if t.alsoDB {
			if err := a.lib.UpdateTrackMetadata(rec.ID, tags); err != nil {
				failed++
				rep.lines = append(rep.lines, reportLine{fmt.Sprintf("#%d: DB sync failed: %v", rec.ID, err), "err"})
				continue
			}
		}
		changed++
		if len(rep.lines) < 12 {
			rep.lines = append(rep.lines, reportLine{fmt.Sprintf("#%d: %s → %q", rec.ID, strings.Join(notes, ", "), newComment), ""})
		}
	}
	rep.changed, rep.unchanged, rep.failed, rep.skipped = changed, unchanged, failed, skipped
	t.lastRun = rep
	return
}

// ReportPanel shows the outcome of the last run.
func (t *GlobalTool) ReportPanel(a *App) {
	if t.lastRun == nil {
		return
	}
	rep := t.lastRun
	p := a.pal()
	a.L("Last run", FontWeight(WeightBold), FontSize(a.fs(14)))
	verb := "updated"
	if rep.dryRun {
		verb = "would update"
	}
	a.L(fmt.Sprintf("%d track(s) %s, %d already clean, %d failed, %d skipped (unsupported file type).",
		rep.changed, verb, rep.unchanged, rep.failed, rep.skipped),
		FontSize(a.fs(12)), TextColorVec(p.textDim))
	a.reportLines(rep.lines, a.paneTextWidth())
	if rep.dryRun && rep.changed > 0 {
		a.L("Dry run — nothing written. Uncheck Dry run to apply.", FontSize(a.fs(12)), TextColorVec(p.textDim))
	}
}
