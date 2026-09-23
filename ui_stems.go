package main

import (
	"fmt"
	"os"

	. "go.hasen.dev/shirei"
	shireiapp "go.hasen.dev/shirei/app"
	. "go.hasen.dev/shirei/widgets"
)

// ui_stems.go: the stems side panel. A stem icon in the track browser opens
// the panel for that track; it replaces the tool pane while open and offers
// play/stop with per-stem mute toggles (Drums / Bass / Other / Vocals).

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
	a.stemsPlayer.Play(a.mixer, func() { a.ensureAudio() })
}

// stopPlayback halts current playback.
func (a *App) stopPlayback() {
	if a.stemsPlayer != nil {
		a.stemsPlayer.Stop()
	}
}

// stemsSection renders the stems player inside the MP3 Tags pane for the
// selected track (nothing renders when the track has no stems).
func (a *App) stemsSection(rec TrackRecord) {
	if a.lib == nil || !a.stemsSet[rec.ID] {
		return
	}
	pk := a.pal()
	a.L("Stems", FontSize(a.fs(13)), FontWeight(WeightBold))
	playing := a.stemsPlayer != nil && a.stemsPlayerTrack == rec.ID && a.stemsPlayer.playing.Load()
	Container(Attrs(Row, CrossMid, Gap(8), Pad2(4, 0)), func() {
		if CtrlButton(SymPlay, "Play original file", !playing) {
			a.startPlayback(rec)
		}
		if CtrlButton(SymCancel, "Stop", playing) {
			a.stopPlayback()
		}
		if a.stemsPlayer != nil && a.stemsPlayerTrack == rec.ID {
			status, errMsg, _ := a.stemsPlayer.Status()
			if errMsg != "" {
				a.wrappedText(errMsg, 420, FontSize(a.fs(11)), TextColorVec(pk.textError))
			} else if status != "" {
				a.L(status, FontSize(a.fs(11)), TextColorVec(pk.textDim))
			}
		}
	})
	a.L("Stems (all selected — stem playback is not yet working):", FontSize(a.fs(11)), TextColorVec(pk.textDim))
	Container(Attrs(Row, Wrap, CrossMid, Gap(10), Pad2(2, 1)), func() {
		for _, name := range StemNames {
			Container(Attrs(Row, CrossMid, Gap(4)), func() {
				Icon(SymBoxTick, TextColorVec(pk.textOk), FontSize(a.fs(12)))
				a.L(name, FontSize(a.fs(12)))
			})
		}
	})
	a.wrappedText("Stems are the Engine DJ separations stored next to the library. Stem playback is not yet working — the stems payload is Engine-proprietary and cannot be decoded yet. Play plays the original file in any format.",
		a.paneTextWidth(), FontSize(a.fs(11)), TextColorVec(pk.textDim))
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

// ensureAudio starts the platform audio output once for the process.
func (a *App) ensureAudio() {
	if a.audioStarted {
		return
	}
	if err := shireiapp.StartAudio(48000, a.mixer.Fill); err != nil {
		// Already started (or no device): keep going — the mixer still
		// renders headlessly and a second StartAudio is a no-op error.
		fmt.Fprintln(os.Stderr, "audio:", err)
		return
	}
	a.audioStarted = true
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
