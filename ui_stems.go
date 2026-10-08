package main

import (
	"fmt"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// ui_stems.go: the playback section inside the MP3 Tags pane. Every track
// can play its own audio file; tracks with an Engine .stems file play the
// separated stems instead, with live per-stem filters (Vocals / Bass /
// Drums / Other) that can isolate one stem or any combination.

// startPlayback stops any current playback and starts the player for the
// given track. Called from the MP3 Tags pane.
func (a *App) startPlayback(rec TrackRecord) {
	a.stopPlayback()
	if a.lib == nil {
		return
	}
	regularPath := a.lib.ResolveMedia(rec.Path)
	if regularPath == "" {
		return
	}
	a.stemsPlayer = NewAudioPlayer(rec, regularPath, a.lib.StemsFile(rec.ID))
	a.stemsPlayerTrack = rec.ID
	a.stemsPlayer.SetStemMask(a.stemMaskBits())
	a.stemsPlayer.SetVolume(int(a.volume + 0.5))
	a.stemsPlayer.Play()
}

// stopPlayback halts current playback.
func (a *App) stopPlayback() {
	if a.stemsPlayer != nil {
		a.stemsPlayer.Stop()
	}
}

// stemMaskBits renders the app-level stem toggles as a bitmask.
func (a *App) stemMaskBits() uint32 {
	var mask uint32
	for i := range a.stemsOn {
		if a.stemsOn[i] {
			mask |= 1 << uint(i)
		}
	}
	return mask
}

// setStemToggle flips one stem's state and applies it live.
func (a *App) setStemToggle(i int, on bool) {
	a.stemsOn[i] = on
	if a.stemsPlayer != nil {
		a.stemsPlayer.SetStemMask(a.stemMaskBits())
	}
}

// volume is the session-wide player volume in percent (0..100). It lives on
// the App so it survives track changes: every new player starts at it.
// (Session-only, like the theme — never written to the config file.)

// setVolume stores the volume and applies it to the live player.
func (a *App) setVolume(pct float32) {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	a.volume = pct
	if a.stemsPlayer != nil {
		a.stemsPlayer.SetVolume(int(pct + 0.5))
	}
}

// volumeIcon picks the speaker glyph for a volume level (0..100).
func volumeIcon(v int) IconGlyph {
	switch {
	case v <= 0:
		return SymVolMute
	case v <= 33:
		return SymVolLow
	case v <= 66:
		return SymVolMid
	default:
		return SymVolHigh
	}
}

// playbackSection renders the player inside the MP3 Tags pane for the
// selected track — available for every track regardless of format. For
// stemmed tracks it adds seek controls and the per-stem filter toggles.
func (a *App) playbackSection(rec TrackRecord) {
	pk := a.pal()
	a.L("Playback", FontSize(a.fs(13)), FontWeight(WeightBold))
	mine := a.stemsPlayer != nil && a.stemsPlayerTrack == rec.ID
	playing := mine && a.stemsPlayer.playing.Load()
	Container(Attrs(Row, CrossMid, Gap(8), Pad2(4, 0)), func() {
		NextAccessName("playback-row")
		AssignAccess()
		Container(Attrs(), func() {
			NextAccessName("playback-play")
			AssignAccess()
			if CtrlButton(SymPlay, "Play", !playing) {
				a.startPlayback(rec)
			}
		})
		if playing {
			if CtrlButton(SymPause, "Pause", true) {
				a.stemsPlayer.TogglePause()
			}
		}
		if CtrlButton(SymCancel, "Stop", playing) {
			a.stopPlayback()
		}
		if mine {
			status, errMsg, _ := a.stemsPlayer.Status()
			if errMsg != "" {
				a.wrappedText(errMsg, 420, FontSize(a.fs(11)), TextColorVec(pk.textError))
			} else if status != "" {
				a.L(status, FontSize(a.fs(11)), TextColorVec(pk.textDim))
			}
		}
	})
	// Volume: session-wide (survives track changes) and applied live — the
	// slider also works before playback starts; a player created later
	// starts at this level.
	Container(Attrs(Row, CrossMid, Gap(8), Pad2(2, 0)), func() {
		NextAccessName("volume-row")
		AssignAccess()
		Icon(volumeIcon(int(a.volume+0.5)), FontSize(a.fs(14)), TextColorVec(pk.textDim))
		before := a.volume
		Slider(&a.volume, SliderAttrs{Min: 0, Max: 100, Step: 1, Width: 160})
		if a.volume != before {
			a.setVolume(a.volume)
		}
		a.L(fmt.Sprintf("%d%%", int(a.volume+0.5)), FontSize(a.fs(11)), TextColorVec(pk.textDim))
	})
	a.stemsGenSection(rec)
	if !mine {
		// Not playing this track: still offer the stem mix for stemmed
		// tracks so it can be set before pressing Play.
		if a.lib != nil && a.stemsSet[rec.ID] {
			a.stemsRow(0xf, pk)
		}
		return
	}
	paused := a.stemsPlayer.Paused()
	Container(Attrs(Row, CrossMid, Gap(8), Pad2(4, 0)), func() {
		if CtrlButton(SymPrev, "Back 10s", !paused) {
			a.stemsPlayer.Seek(-10)
		}
		if CtrlButton(SymNext, "Forward 10s", !paused) {
			a.stemsPlayer.Seek(10)
		}
		pos, total := a.stemsPlayer.Position(), a.stemsPlayer.Total()
		a.L(fmt.Sprintf("%d:%02d / %s", int(pos)/60, int(pos)%60,
			stemsDuration(total)), FontSize(a.fs(11)), TextColorVec(pk.textDim))
	})
	if a.stemsPlayer.HasStems() {
		a.stemsRow(a.stemsPlayer.StemMask(), pk)
	}
}

// stemsGenSection renders the stems generation block of the playback panel:
// a Generate button (hidden while this track has a run going), the live
// stage/progress of the active run, and the outcome. Generation sends the
// track's audio to the local StemDeck server (see stemsgen.go), waits for
// the separation, downloads the stem WAVs, and writes the encrypted
// .stems file into the Engine Library.
func (a *App) stemsGenSection(rec TrackRecord) {
	p := a.pal()
	a.L("Generate stems (StemDeck)", FontWeight(WeightBold), FontSize(a.fs(13)))

	if a.stemsGen == nil || a.stemsGen.Track.ID != rec.ID {
		Container(Attrs(Row, CrossMid, Gap(8), Pad2(4, 0)), func() {
			NextAccessName("stems-generate")
			AssignAccess()
			busy := a.stemsGen != nil && !a.stemsGen.Finished()
			if CtrlButton(SymAudio, "Generate Stems", !busy) {
				a.startStemsGen(rec)
			}
			if busy {
				a.L("Another generation is running.", FontSize(a.fs(11)), TextColorVec(p.textDim))
			} else if a.lib != nil && a.stemsSet[rec.ID] {
				a.L("This track already has stems — generating again replaces them.",
					FontSize(a.fs(11)), TextColorVec(p.textDim))
			} else {
				a.L("Splits the song via the local StemDeck server into Vocals / Bass / Drums / Other and writes the .stems file.",
					FontSize(a.fs(11)), TextColorVec(p.textDim))
			}
		})
		return
	}

	job := a.stemsGen
	state, stage, errMsg, progress := job.Status()
	Container(Attrs(Gap(2), Pad2(4, 0)), func() {
		row := Attrs(Row, CrossMid, Gap(8))
		Container(row, func() {
			switch state {
			case "done":
				Icon(SymITick, FontSize(a.fs(13)), TextColorVec(p.textOk))
				a.L("Done", FontWeight(WeightBold), FontSize(a.fs(12)), TextColorVec(p.textOk))
			case "error":
				Icon(SymWarn, FontSize(a.fs(13)), TextColorVec(p.textError))
				a.L("Failed", FontWeight(WeightBold), FontSize(a.fs(12)), TextColorVec(p.textError))
			case "cancelled":
				Icon(SymWarn, FontSize(a.fs(13)), TextColorVec(p.textDim))
				a.L("Cancelled", FontSize(a.fs(12)), TextColorVec(p.textDim))
			default:
				a.L(genStateLabel(state), FontWeight(WeightBold), FontSize(a.fs(12)))
			}
			if state == "processing" && progress > 0 {
				a.L(fmt.Sprintf("%d%%", int(progress*100+0.5)), FontSize(a.fs(12)), TextColorVec(p.textDim))
			}
			switch state {
			case "done", "error", "cancelled":
			default:
				if CtrlButton(SymCancel, "Cancel", true) {
					server := a.StemdeckURL
					if server == "" {
						server = DefaultStemdeckURL
					}
					job.Cancel(server)
				}
			}
		})
		if state == "processing" && progress > 0 {
			ProgressBar(float32(progress))
		}
		if stage != "" && state != "done" {
			a.wrappedText(stage, a.paneTextWidth(), FontSize(a.fs(11)), TextColorVec(p.textDim))
		}
		if errMsg != "" {
			a.errorText(errMsg, a.paneTextWidth())
		}
	})
}

// genStateLabel maps a generation state to its display label.
func genStateLabel(state string) string {
	switch state {
	case "uploading":
		return "Uploading…"
	case "processing":
		return "Separating…"
	case "downloading":
		return "Downloading stems…"
	case "encoding":
		return "Encoding stems…"
	}
	return state
}

// stemsRow renders the four stem filter toggles plus an All reset. mask is
// the currently applied bitmask (used for the button accents).
func (a *App) stemsRow(mask uint32, pk palette) {
	a.L("Stems (toggle which stems are in the mix):", FontSize(a.fs(11)), TextColorVec(pk.textDim))
	Container(Attrs(Row, Wrap, CrossMid, Gap(8), Pad2(2, 1)), func() {
		NextAccessName("stems-row")
		AssignAccess()
		for i, name := range StemNames {
			on := a.stemsOn[i]
			accent := pk.textDim
			if on {
				accent = pk.textOk
			}
			icon := IconGlyph(SymBoxCross)
			if on {
				icon = IconGlyph(SymBoxTick)
			}
			if CtrlButtonWithAccent(icon, name, accent, true) {
				a.setStemToggle(i, !on)
			}
		}
		if CtrlButton(SymBoxTick, "All", a.stemMaskBits() != 0xf) {
			for i := range a.stemsOn {
				a.setStemToggle(i, true)
			}
		}
	})
}

// stemsDuration renders seconds as m:ss (blank when unknown).
func stemsDuration(sec float64) string {
	if sec <= 0 {
		return "-:--"
	}
	return fmt.Sprintf("%d:%02d", int(sec)/60, int(sec)%60)
}

// buildStemsSet scans the library for tracks with stems. Called
// synchronously from Refresh: the a.lib check and capture happen on the
// caller's goroutine, and only the scan (a few thousand stat calls) runs
// in the background. Close waits for the scan via stemsScanDone, so no
// goroutine ever touches the library handle after Close.
func (a *App) buildStemsSet() {
	if a.lib == nil || !a.lib.StemsAvailable() {
		return
	}
	lib, tracks := a.lib, a.Tracks
	done := make(chan struct{})
	a.stemsScanDone = done
	go func() {
		defer close(done)
		set := map[int64]bool{}
		for _, tr := range tracks {
			if lib.HasStems(tr.ID) {
				set[tr.ID] = true
			}
		}
		WithFrameLock(func() { a.stemsSet = set })
		RequestNextFrame()
	}()
}

// stemsColumn is the browser column with the stem icon (clickable when the
// track has stems).
func stemsColumn(a *App) TableColumn[TrackRecord] {
	return TableColumn[TrackRecord]{
		Label: "Stems",
		Width: 48,
		Cell: func(r TrackRecord) {
			if a.lib == nil || !a.stemsSet[r.ID] {
				return
			}
			NextAccessName(fmt.Sprintf("stems-%d", r.ID))
			Container(Attrs(Pad2(2, 0)), func() {
				AssignAccess()
				open := IsClicked()
				Icon(SymAudio, TextColorVec(a.pal().textOk), FontSize(a.fs(13)))
				if open {
					// Jump to the MP3 Tags pane with the track selected —
					// the stems player lives there.
					a.Selected = r.ID
					for i, tool := range a.Tools {
						if tool.Name() == "MP3 Tags" {
							a.ActiveTool = i
							break
						}
					}
				}
			})
		},
	}
}
