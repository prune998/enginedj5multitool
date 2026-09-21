package main

import (
	"fmt"
	"os"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// TagsTool is a manual MP3 (ID3v2) tag editor for the selected track. It can
// also sync the edited metadata back into the Engine DJ Track table.
type TagsTool struct {
	mp3Only bool
	lastSel int64

	path    string // resolved audio file path
	pathErr string // file missing / not an mp3

	tags    MediaTags
	readErr string

	alsoDB bool // also update the Engine DJ Track table
	saved  bool
}

func (t *TagsTool) Name() string    { return "MP3 Tags" }
func (t *TagsTool) Icon() IconGlyph { return SymEdit }

func (t *TagsTool) View(a *App) {
	Container(Attrs(Row, Grow(1), Expand, Gap(10)), func() {
		Container(Attrs(Grow(1), Expand, Clip, Gap(8)), func() {
			extra := &TableColumn[TrackRecord]{
				Label: "Type", Width: 60,
				Cell: func(r TrackRecord) { Label(upper(r.FileType), FontSize(12)) },
				Less: func(a, b TrackRecord) bool { return a.FileType < b.FileType },
			}
			a.BrowserPanel(extra)
		})

		Container(Attrs(FixWidth(500), Expand, Clip, Gap(8)), func() {
			t.EditorPanel(a)
		})
	})
}

func upper(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'a' && r <= 'z' {
			out[i] = r - 32
		}
	}
	return string(out)
}

func (t *TagsTool) EditorPanel(a *App) {
	Label("MP3 Tags", FontSize(18), FontWeight(WeightBold))

	rec, ok := a.SelectedTrack()
	if !ok || a.Selected == 0 {
		Label("Select a track on the left.", TextColor(220, 8, 40, 1))
		return
	}
	t.ensureLoaded(a, rec)

	// File info
	Container(Attrs(Gap(2), Pad2(4, 0)), func() {
		Container(Attrs(Row, CrossMid, Gap(6)), func() {
			Icon(SymAudio, FontSize(13), TextColor(220, 8, 40, 1))
			Label(fmt.Sprintf("#%d  %s — %s", rec.ID, rec.Artist, rec.Title), FontWeight(WeightBold))
		})
		if t.pathErr != "" {
			Label(t.pathErr, FontSize(11), TextColor(0, 70, 40, 1))
		} else {
			Label(t.path, FontSize(11), TextColor(220, 8, 40, 1))
		}
	})

	if t.readErr != "" {
		Label("Error: "+t.readErr, TextColor(0, 70, 40, 1))
		return
	}
	if !IsMP3(rec) {
		Label("Not an MP3 file — tag editing supports MP3 (ID3v2) only.",
			TextColor(40, 70, 40, 1))
		return
	}

	t.Form()
	t.SaveRow(a, rec)
}

// ensureLoaded (re)reads the tags from the file whenever the selection changes.
func (t *TagsTool) ensureLoaded(a *App, rec TrackRecord) {
	if a.Selected == t.lastSel && (t.readErr != "" || t.path != "" || t.pathErr != "") {
		return
	}
	t.lastSel = a.Selected
	t.saved = false
	t.path = ResolveMediaPath(a.lib.Dir, a.MusicRoot, rec.Path)
	t.tags = MediaTags{}
	t.readErr, t.pathErr = "", ""

	if st, err := os.Stat(t.path); err != nil || st.IsDir() {
		t.pathErr = "Audio file not found: " + t.path
		t.readErr = "unavailable"
		return
	}
	tags, err := ReadMediaTags(t.path)
	if err != nil {
		t.readErr = err.Error()
		return
	}
	t.tags = tags
}

// Form renders the editable tag fields.
func (t *TagsTool) Form() {
	Container(Attrs(Gap(6)), func() {
		fieldRow("Title", &t.tags.Title)
		fieldRow("Artist", &t.tags.Artist)
		Container(Attrs(Row, Gap(8)), func() {
			Container(Attrs(Expand), func() {
				Label("Album", FontSize(11), TextColor(220, 8, 40, 1))
				TextInput(&t.tags.Album)
			})
			Container(Attrs(Expand), func() {
				Label("Album artist", FontSize(11), TextColor(220, 8, 40, 1))
				TextInput(&t.tags.AlbumArtist)
			})
		})
		Container(Attrs(Row, Gap(8)), func() {
			Container(Attrs(Expand), func() {
				Label("Genre", FontSize(11), TextColor(220, 8, 40, 1))
				TextInput(&t.tags.Genre)
			})
			Container(Attrs(FixWidth(110)), func() {
				Label("Year", FontSize(11), TextColor(220, 8, 40, 1))
				TextInput(&t.tags.Year)
			})
			Container(Attrs(FixWidth(110)), func() {
				Label("Track #", FontSize(11), TextColor(220, 8, 40, 1))
				TextInput(&t.tags.Track)
			})
			Container(Attrs(FixWidth(110)), func() {
				Label("Disc #", FontSize(11), TextColor(220, 8, 40, 1))
				TextInput(&t.tags.Disc)
			})
		})
		Container(Attrs(Row, Gap(8)), func() {
			Container(Attrs(Expand), func() {
				Label("Composer", FontSize(11), TextColor(220, 8, 40, 1))
				TextInput(&t.tags.Composer)
			})
			Container(Attrs(FixWidth(110)), func() {
				Label("BPM", FontSize(11), TextColor(220, 8, 40, 1))
				TextInput(&t.tags.BPM)
			})
		})
		Container(Attrs(Gap(2)), func() {
			Label("Comment", FontSize(11), TextColor(220, 8, 40, 1))
			TextArea(&t.tags.Comment)
		})
	})
}

func fieldRow(label string, buf *string) {
	Container(Attrs(Gap(2)), func() {
		Label(label, FontSize(11), TextColor(220, 8, 40, 1))
		TextInput(buf)
	})
}

// SaveRow shows the save controls and DB-sync toggle.
func (t *TagsTool) SaveRow(a *App, rec TrackRecord) {
	Container(Attrs(Row, CrossMid, Gap(10), Pad2(6, 0)), func() {
		if CtrlButton(SymITick, "Save tags to file", a.lib != nil && t.pathErr == "") {
			t.Save(a, rec)
		}
		CheckBox(&t.alsoDB, "Also update Engine DJ database")
		if t.saved {
			Label("Saved ✓", TextColor(140, 45, 34, 1), FontSize(12))
		}
	})
}

func (t *TagsTool) Save(a *App, rec TrackRecord) {
	if err := SaveMediaTags(t.path, t.tags); err != nil {
		Toast(SymFail, "Tag write failed", err.Error())
		return
	}
	msg := "ID3v2 tags written to " + baseName(t.path)
	if t.alsoDB {
		if err := a.lib.UpdateTrackMetadata(rec.ID, t.tags); err != nil {
			Toast(SymFail, "File saved, DB sync failed", err.Error())
			return
		}
		msg += " and Engine DB updated"
		a.Refresh()
		// keep the reloaded selection in view
		t.lastSel = 0
	}
	t.saved = true
	Toast(SymITick, "Saved", msg)
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == os.PathSeparator {
			return p[i+1:]
		}
	}
	return p
}
