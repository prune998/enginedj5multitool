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
