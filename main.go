package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"

	_ "modernc.org/sqlite"
)

// appVersion is overridden at build time via -ldflags "-X main.appVersion=...".
var appVersion = "dev"

func main() {
	lc := LoadOrCreateConfig()
	if lc.Err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", lc.Err)
	}

	dbPath := flag.String("db", lc.Library, "path to the Engine DJ database (m.db)")
	filter := flag.String("filter", "", "substring filter on title/artist/album/filename (case-insensitive)")
	fix := flag.Bool("fix", false, "reorder cues/loops (chronological, latest at slot 8) and apply standard slot colours")
	dryRun := flag.Bool("dry-run", false, "with -fix: show what would change without updating the DB")
	ui := flag.Bool("ui", false, "launch the graphical interface")
	tool := flag.Int("tool", lc.Tool, "with -ui: index of the tool tab to open (0 = Cues & Loops, 1 = MP3 Tags)")
	theme := flag.String("theme", lc.Theme, "UI theme: auto (OS default), light or dark")
	snapshot := flag.String("snapshot", "", "render one frame of the UI headlessly to this PNG and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	// Config values became the flag defaults; merge back so the UI and the
	// persisted settings see the effective values.
	lc.Library = *dbPath
	lc.Theme = *theme
	lc.Tool = *tool

	if *showVersion {
		fmt.Println(appVersion)
		return
	}

	// No CLI action flags → run the GUI.
	if !*fix && *snapshot == "" && !*ui && !flagChanged("filter") {
		runUI(lc, "", *filter)
		return
	}
	if *ui || *snapshot != "" {
		runUI(lc, *snapshot, *filter)
		return
	}

	if _, err := os.Stat(*dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot open %s: %v\n", *dbPath, err)
		os.Exit(1)
	}

	lib, err := OpenLibrary(*dbPath, !*fix || *dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer lib.Close()

	tracks, err := lib.Tracks(*filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: query: %v\n", err)
		os.Exit(1)
	}

	useColor := isTerminal()
	if *fix {
		if *dryRun {
			fmt.Println("DRY RUN — no changes will be written")
			fmt.Println()
		}
		runFix(lib, tracks, useColor, !*dryRun)
		return
	}
	runDisplay(lib, tracks, useColor)
}

func flagChanged(name string) bool {
	changed := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			changed = true
		}
	})
	return changed
}

func runDisplay(lib *Library, tracks []TrackRecord, useColor bool) {
	var (
		shown, withCues, withLoops, cuesOrdered, loopsOrdered int
		totalCues, totalLoops                                 int
		parseErrors                                           int
	)
	for _, rec := range tracks {
		d, err := lib.LoadDetail(rec)
		if err != nil {
			parseErrors++
			fmt.Fprintf(os.Stderr, "warning: track %d: %v\n", rec.ID, err)
			continue
		}

		header := fmt.Sprintf("#%d  %s — %s  [%s]", rec.ID, rec.Artist, rec.Title, formatDuration(rec.Length))
		fmt.Println(header)
		fmt.Println(strings.Repeat("─", min(len(header)+2, 100)))

		var setSamples []float64
		for _, c := range d.QC.Cues {
			if c.Sample < 0 {
				continue
			}
			setSamples = append(setSamples, c.Sample)
			totalCues++
			label := c.Label
			if label == "" {
				label = fmt.Sprintf("Cue %d", c.Num)
			}
			fmt.Printf("  %s Cue %d  %-14s %9s  %s  %s\n",
				swatch(c.RGBA, useColor), c.Num, label, fmtTime(c.Sample/d.SampleRate), hexColor(c.RGBA), colorName(c.RGBA))
		}
		if len(d.QC.Cues) > 0 {
			cuesCount := len(setSamples)
			ordered := inOrderSamples(setSamples)
			status := "yes"
			if !ordered {
				status = "NO"
			}
			if cuesCount > 0 {
				withCues++
				if ordered {
					cuesOrdered++
				}
			}
			fmt.Printf("  → %d cue(s), in order: %s\n\n", cuesCount, status)
		}

		setStarts := make([]float64, 0, 8)
		for _, l := range d.Loops {
			if l.Start < 0 || !l.StartSet {
				continue
			}
			setStarts = append(setStarts, l.Start)
			totalLoops++
			label := l.Label
			if label == "" {
				label = fmt.Sprintf("Loop %d", l.Num)
			}
			dur := 0.0
			beats := 0.0
			if l.End > l.Start {
				dur = (l.End - l.Start) / d.SampleRate
				if rec.BPM > 0 {
					beats = dur * rec.BPM / 60
				}
			}
			extra := ""
			if beats > 0 {
				extra = fmt.Sprintf("  (%.2fs ≈ %.1f beats)", dur, beats)
			}
			fmt.Printf("  %s Loop %d  %-14s %9s → %-9s %s  %s%s\n",
				swatch(l.RGBA, useColor), l.Num, label, fmtTime(l.Start/d.SampleRate), fmtTime(l.End/d.SampleRate), hexColor(l.RGBA), colorName(l.RGBA), extra)
		}
		if len(d.Loops) > 0 {
			ordered := inOrderSamples(setStarts)
			status := "yes"
			if !ordered {
				status = "NO"
			}
			if len(setStarts) > 0 {
				withLoops++
				if ordered {
					loopsOrdered++
				}
			}
			fmt.Printf("  → %d loop(s), in order: %s\n\n", len(setStarts), status)
		}
		shown++
	}

	fmt.Println(strings.Repeat("═", 60))
	fmt.Printf("Tracks shown: %d\n", shown)
	fmt.Printf("  with cues : %4d   (%d set cues)   in order: %d / out of order: %d\n",
		withCues, totalCues, cuesOrdered, withCues-cuesOrdered)
	fmt.Printf("  with loops: %4d   (%d set loops)  in order: %d / out of order: %d\n",
		withLoops, totalLoops, loopsOrdered, withLoops-loopsOrdered)
	if parseErrors > 0 {
		fmt.Printf("  unparseable blobs skipped: %d\n", parseErrors)
	}
}

func runFix(lib *Library, tracks []TrackRecord, useColor bool, apply bool) {
	var (
		matched, unchanged                       int
		cuesMoved, cuesRecolored, cuesRelabeled  int
		loopsMoved, loopsRecolored, loopsRelabel int
		pending                                  []*FixResult
	)
	for _, rec := range tracks {
		matched++
		res, err := lib.FixTrack(rec, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: track %d skipped: %v\n", rec.ID, err)
			continue
		}
		if res == nil {
			unchanged++
			continue
		}

		header := fmt.Sprintf("#%d  %s — %s  [%s]", rec.ID, rec.Artist, rec.Title, formatDuration(rec.Length))
		fmt.Println(header)
		if res.Compute.QCChanged {
			printChangeList("cues", res.Compute.CueChanges, useColor, cueColors(res.Compute.NewCues))
			for _, ch := range res.Compute.CueChanges {
				if ch.moved {
					cuesMoved++
				}
				if ch.recolored {
					cuesRecolored++
				}
				if ch.relabeled {
					cuesRelabeled++
				}
			}
		}
		if res.Compute.LPChanged {
			printChangeList("loops", res.Compute.LoopChanges, useColor, loopColors(res.Compute.NewLoops))
			for _, ch := range res.Compute.LoopChanges {
				if ch.moved {
					loopsMoved++
				}
				if ch.recolored {
					loopsRecolored++
				}
				if ch.relabeled {
					loopsRelabel++
				}
			}
		}
		fmt.Println()
		pending = append(pending, res)
	}

	written := 0
	if apply && len(pending) > 0 {
		for _, res := range pending {
			if err := res.Write(lib); err != nil {
				fmt.Fprintf(os.Stderr, "error: update track %d: %v\n", res.ID, err)
				os.Exit(1)
			}
			written++
		}
	}

	fmt.Println(strings.Repeat("═", 60))
	fmt.Printf("Tracks matched: %d, needing changes: %d (already clean: %d)\n", matched, len(pending), unchanged)
	fmt.Printf("  cues : %d moved, %d recolored, %d relabeled\n", cuesMoved, cuesRecolored, cuesRelabeled)
	fmt.Printf("  loops: %d moved, %d recolored, %d relabeled, %d activeOnLoadLoops remapped\n",
		loopsMoved, loopsRecolored, loopsRelabel, countActiveRemaps(pending))
	switch {
	case !apply && len(pending) > 0:
		fmt.Println("DRY RUN — nothing written. Re-run without -dry-run to apply.")
	case apply:
		fmt.Printf("Wrote %d track(s) to the DB. Note: Track.lastEditTime was bumped by the DB trigger.\n", written)
	}
}

func countActiveRemaps(pending []*FixResult) int {
	n := 0
	for _, res := range pending {
		if res.Compute.ActiveCh {
			n++
		}
	}
	return n
}

func cueColors(cues []Cue) map[int][4]byte {
	colors := map[int][4]byte{}
	for _, c := range cues {
		if c.Sample >= 0 {
			colors[c.Num] = c.RGBA
		}
	}
	return colors
}

func loopColors(loops []Loop) map[int][4]byte {
	colors := map[int][4]byte{}
	for _, l := range loops {
		if l.StartSet && l.Start >= 0 {
			colors[l.Num] = l.RGBA
		}
	}
	return colors
}

func printChangeList(what string, changes []itemChange, useColor bool, colors map[int][4]byte) {
	fmt.Printf("  %s: %d set\n", what, len(changes))
	for _, ch := range changes {
		tag := changeTags(ch)
		if tag != "" {
			tag = "  (" + tag + ")"
		}
		rgba := colors[ch.newSlot]
		end := ""
		if ch.end != "" && ch.end != "-" {
			end = " → " + ch.end
		}
		fmt.Printf("    %s slot %d ← %d  %s%s  %s %s%s\n",
			swatch(rgba, useColor), ch.newSlot, ch.oldSlot, ch.when, end,
			hexColor(rgba), colorName(rgba), tag)
	}
}

func formatDuration(seconds int64) string {
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ = sql.ErrNoRows
