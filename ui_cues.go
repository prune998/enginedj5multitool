package main

import (
	"fmt"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// CuesTool inspects and fixes hot cues and loops of the selected track, or of
// every track matching the current filter.
type CuesTool struct {
	dryRun  bool
	lastSel int64
	detail  *TrackDetail
	rec     TrackRecord
	loadErr string

	lastPreview int64
	preview     *FixResult
}

func (t *CuesTool) Name() string    { return "Cues & Loops" }
func (t *CuesTool) Icon() IconGlyph { return SymChapters }

func (t *CuesTool) View(a *App) {
	t.ensureLoaded(a)

	Container(Attrs(Row, Grow(1), Expand, Gap(10)), func() {
		// Left: track browser.
		Container(Attrs(Grow(1), Expand, Clip, Gap(8)), func() {
			a.BrowserPanel(nil)
		})

		// Right: detail panel.
		Container(Attrs(FixWidth(480), Expand, Clip, Gap(8)), func() {
			t.DetailPanel(a)
		})
	})
}

func (t *CuesTool) ensureLoaded(a *App) {
	if a.Selected == 0 {
		t.detail, t.rec, t.loadErr = nil, TrackRecord{}, ""
		return
	}
	if a.Selected == t.lastSel && t.detail != nil {
		return
	}
	t.lastSel = a.Selected
	t.preview = nil
	rec, ok := a.SelectedTrack()
	if !ok {
		t.detail, t.loadErr = nil, "track not found"
		return
	}
	d, err := a.lib.LoadDetail(rec)
	t.rec, t.detail, t.loadErr = rec, d, errString(err)
}

func errString(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}

// DetailPanel shows the cue/loop layout of the selected track plus the fix
// controls.
func (t *CuesTool) DetailPanel(a *App) {
	Label("Cues & Loops", FontSize(18), FontWeight(WeightBold))

	if a.Selected == 0 {
		Label("Select a track on the left.", TextColor(220, 8, 40, 1))
		return
	}
	if t.loadErr != "" {
		Label("Error: "+t.loadErr, TextColor(0, 70, 40, 1))
		return
	}
	if t.detail == nil {
		return
	}
	d := t.detail

	Label(fmt.Sprintf("#%d  %s — %s", t.rec.ID, t.rec.Artist, t.rec.Title),
		FontWeight(WeightBold))
	Label(fmt.Sprintf("%.0f Hz    %.1f BPM    %s", d.SampleRate, t.rec.BPM, formatDuration(t.rec.Length)),
		FontSize(12), TextColor(220, 8, 40, 1))

	cueStatus, loopStatus := orderStatus(d)
	t.slotTable(fmt.Sprintf("Cues — %d set (%s)", countSetCues(d), cueStatus), cueRows(d), d.SampleRate)
	t.slotTable(fmt.Sprintf("Loops — %d set (%s)", countSetLoops(d), loopStatus), loopRows(d), d.SampleRate)

	t.ActionsPanel(a)
	t.PreviewPanel()
}

type slotRow struct {
	num   int
	label string
	when  string
	end   string
	rgba  [4]byte
	empty bool
}

func countSetCues(d *TrackDetail) int {
	n := 0
	for _, c := range d.QC.Cues {
		if c.Sample >= 0 {
			n++
		}
	}
	return n
}

func countSetLoops(d *TrackDetail) int {
	n := 0
	for _, l := range d.Loops {
		if l.StartSet && l.Start >= 0 {
			n++
		}
	}
	return n
}

func orderStatus(d *TrackDetail) (string, string) {
	cueVals := make([]float64, 0, 8)
	for _, c := range d.QC.Cues {
		if c.Sample >= 0 {
			cueVals = append(cueVals, c.Sample)
		}
	}
	loopVals := make([]float64, 0, 8)
	for _, l := range d.Loops {
		if l.StartSet && l.Start >= 0 {
			loopVals = append(loopVals, l.Start)
		}
	}
	return orderWord(inOrderSamples(cueVals), len(d.QC.Cues) > 0),
		orderWord(inOrderSamples(loopVals), len(d.Loops) > 0)
}

func orderWord(ordered bool, anyBlob bool) string {
	if !anyBlob {
		return "no data"
	}
	if ordered {
		return "in order"
	}
	return "OUT OF ORDER"
}

func cueRows(d *TrackDetail) []slotRow {
	rows := make([]slotRow, 0, 8)
	for _, c := range d.QC.Cues {
		r := slotRow{num: c.Num, label: c.Label, rgba: c.RGBA, empty: c.Sample < 0}
		if !r.empty {
			r.label = orDefault(c.Label, fmt.Sprintf("Cue %d", c.Num))
			r.when = fmtTime(c.Sample / d.SampleRate)
		}
		rows = append(rows, r)
	}
	return rows
}

func loopRows(d *TrackDetail) []slotRow {
	rows := make([]slotRow, 0, 8)
	for _, l := range d.Loops {
		r := slotRow{num: l.Num, label: l.Label, rgba: l.RGBA, empty: l.Start < 0 || !l.StartSet}
		if !r.empty {
			r.label = orDefault(l.Label, fmt.Sprintf("Loop %d", l.Num))
			r.when = fmtTime(l.Start / d.SampleRate)
			r.end = fmtTime(l.End / d.SampleRate)
		}
		rows = append(rows, r)
	}
	return rows
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// slotTable renders the 8 slots of cues or loops with colour swatches.
func (t *CuesTool) slotTable(title string, rows []slotRow, sampleRate float64) {
	Label(title, FontWeight(WeightBold), FontSize(14))
	Container(Attrs(Gap(2)), func() {
		for _, r := range rows {
			row := r
			Container(Attrs(Row, CrossMid, Pad2(1, 6), Corners(4)), func() {
				if !row.empty {
					h, s, l := rgbToHSL(row.rgba)
					Container(Attrs(FixSize(18, 14), Corners(3), Background(h, s, l, 1)), func() {})
				} else {
					Container(Attrs(FixSize(18, 14), Corners(3), Background(0, 0, 88, 1)), func() {})
				}
				Container(Attrs(FixWidth(46)), func() {
					if row.empty {
						Label(fmt.Sprintf("%d", row.num), TextColor(0, 0, 65, 1), FontSize(12))
					} else {
						Label(fmt.Sprintf("%d", row.num), FontWeight(WeightBold), FontSize(12))
					}
				})
				Container(Attrs(FixWidth(130)), func() {
					if row.empty {
						Label("(empty)", TextColor(0, 0, 65, 1), FontSize(12))
					} else {
						Label(row.label, FontSize(12))
					}
				})
				Container(Attrs(Expand), func() {
					when := row.when
					if row.end != "" {
						when = when + " → " + row.end
					}
					Label(when, FontSize(12))
				})
				if !row.empty {
					Container(Attrs(FixWidth(64)), func() {
						Label(hexColor(row.rgba), FontSize(11), TextColor(220, 8, 40, 1))
					})
				}
			})
		}
	})
}

// ActionsPanel offers the fix operations for the selected track and for all
// filtered tracks.
func (t *CuesTool) ActionsPanel(a *App) {
	Label("Fix", FontWeight(WeightBold), FontSize(14))
	Container(Attrs(Row, CrossMid, Gap(10)), func() {
		CheckBox(&t.dryRun, "Dry run")
		Label("Reorders chronologically, latest → slot 8, standard colours, intro/outro labels.",
			FontSize(11), TextColor(220, 8, 40, 1))
	})
	Container(Attrs(Row, CrossMid, Gap(10), Pad2(4, 0)), func() {
		enabled := a.lib != nil && a.Selected != 0
		if CtrlButton(SymITick, "Fix selected track", enabled) {
			t.fixSelected(a)
		}
		if CtrlButton(SymChapters, fmt.Sprintf("Fix all filtered (%d)", a.TrackCount), a.lib != nil && a.TrackCount > 0) {
			t.fixAll(a)
		}
	})
}

func (t *CuesTool) fixSelected(a *App) {
	rec, ok := a.SelectedTrack()
	if !ok {
		return
	}
	res, err := a.lib.FixTrack(rec, t.dryRun)
	if err != nil {
		Toast(SymFail, "Fix failed", err.Error())
		return
	}
	if res == nil {
		ToastMessage("Track already clean — nothing to change.")
		return
	}
	changed := summaryChanges(res)
	if t.dryRun {
		t.preview = res
		t.lastPreview = rec.ID
		Toast(SymInfo, "Dry run", changed+" (preview below)")
		return
	}
	if err := res.Write(a.lib); err != nil {
		Toast(SymFail, "Write failed", err.Error())
		return
	}
	t.lastSel = 0 // force detail reload
	t.preview = nil
	Toast(SymITick, "Fixed", changed)
}

func (t *CuesTool) fixAll(a *App) {
	fixed, clean, failed := 0, 0, 0
	for _, rec := range a.Tracks {
		res, err := a.lib.FixTrack(rec, t.dryRun)
		if err != nil {
			failed++
			continue
		}
		if res == nil {
			clean++
			continue
		}
		if !t.dryRun {
			if err := res.Write(a.lib); err != nil {
				failed++
				continue
			}
		}
		fixed++
	}
	if t.dryRun {
		Toast(SymInfo, "Dry run", fmt.Sprintf("%d track(s) would change, %d already clean", fixed, clean))
		return
	}
	t.lastSel = 0
	t.preview = nil
	Toast(SymITick, "Fixed library", fmt.Sprintf("%d updated, %d already clean, %d failed", fixed, clean, failed))
}

func summaryChanges(res *FixResult) string {
	moved, recolored, relabeled := 0, 0, 0
	for _, ch := range append(append([]itemChange{}, res.Compute.CueChanges...), res.Compute.LoopChanges...) {
		if ch.moved {
			moved++
		}
		if ch.recolored {
			recolored++
		}
		if ch.relabeled {
			relabeled++
		}
	}
	return fmt.Sprintf("%d moved, %d recolored, %d relabeled", moved, recolored, relabeled)
}

// PreviewPanel shows the last dry-run result inline.
func (t *CuesTool) PreviewPanel() {
	if t.preview == nil || t.lastPreview != t.lastSel {
		return
	}
	Label("Dry-run preview", FontWeight(WeightBold), FontSize(14))
	for _, section := range []struct {
		what    string
		changes []itemChange
	}{
		{"Cues", t.preview.Compute.CueChanges},
		{"Loops", t.preview.Compute.LoopChanges},
	} {
		if len(section.changes) == 0 {
			continue
		}
		Label(section.what, FontSize(12), TextColor(220, 8, 40, 1))
		for _, ch := range section.changes {
			tag := changeTags(ch)
			line := fmt.Sprintf("slot %d ← %d   %s", ch.newSlot, ch.oldSlot, ch.when)
			if ch.end != "" && ch.end != "-" {
				line += " → " + ch.end
			}
			if tag != "" {
				line += "   (" + tag + ")   " + ch.label
			}
			Label(line, FontSize(12))
		}
	}
}
