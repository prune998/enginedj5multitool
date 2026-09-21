package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// TagsTool is a manual MP3 (ID3v2) tag editor for the selected track. It can
// also sync the edited metadata back into the Engine DJ Track table. #tags in
// the comment are rendered as interactive bubbles.
type TagsTool struct {
	mp3Only bool
	lastSel int64

	path    string // resolved audio file path
	pathErr string // file missing / not an mp3

	tags    MediaTags
	readErr string

	alsoDB bool // also update the Engine DJ Track table
	newTag string
	saved  bool
}

func (t *TagsTool) Name() string    { return "MP3 Tags" }
func (t *TagsTool) Icon() IconGlyph { return SymEdit }

func (t *TagsTool) View(a *App) {
	Container(Attrs(Row, Grow(1), Expand), func() {
		a.captureSplitRow()

		Container(Attrs(FixWidth(a.splitWidth()), Expand, Clip, Gap(8)), func() {
			extra := &TableColumn[TrackRecord]{
				Label: "Type", Width: 60,
				Cell: func(r TrackRecord) { a.L(upper(r.FileType), FontSize(12)) },
				Less: func(a, b TrackRecord) bool { return a.FileType < b.FileType },
			}
			a.BrowserPanel(extra)
		})

		a.Splitter()

		Container(Attrs(Grow(1), Expand, Clip, Pad2(0, 10), Gap(8)), func() {
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
	a.L("MP3 Tags", FontSize(18), FontWeight(WeightBold))

	rec, ok := a.SelectedTrack()
	if !ok || a.Selected == 0 {
		a.L("Select a track on the left.", TextColorVec(a.pal().textDim))
		return
	}
	t.ensureLoaded(a, rec)

	// File info
	Container(Attrs(Gap(2), Pad2(4, 0)), func() {
		Container(Attrs(Row, CrossMid, Gap(6)), func() {
			Icon(SymAudio, FontSize(13), TextColorVec(a.pal().textDim))
			a.L(fmt.Sprintf("#%d  %s — %s", rec.ID, rec.Artist, rec.Title), FontWeight(WeightBold))
		})
		if t.pathErr != "" {
			a.L(t.pathErr, FontSize(11), TextColor(0, 70, 40, 1))
		} else {
			a.L(t.path, FontSize(11), TextColorVec(a.pal().textDim))
		}
	})

	if t.readErr != "" {
		a.L("Error: "+t.readErr, TextColor(0, 70, 40, 1))
		return
	}
	if !IsMP3(rec) {
		a.L("Not an MP3 file — tag editing supports MP3 (ID3v2) only.",
			TextColor(40, 70, 40, 1))
		return
	}

	t.Form(a)
	t.TagBubbles(a)
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

// Form renders the editable tag fields. Title/Artist/Album/Composer span the
// full panel width; Genre, Year, Track #, Disc # and BPM share one row with
// widths derived from the data actually present in the library database.
func (t *TagsTool) Form(a *App) {
	yearW, trackW, discW, bpmW := t.smallFieldWidths(a)
	Container(Attrs(Expand, Gap(8)), func() {
		t.fieldFull(a, "Title", &t.tags.Title)
		t.fieldFull(a, "Artist", &t.tags.Artist)
		t.fieldFull(a, "Album", &t.tags.Album)
		t.fieldFull(a, "Album artist", &t.tags.AlbumArtist)
		t.fieldFull(a, "Composer", &t.tags.Composer)
		Container(Attrs(Row, Expand, Gap(8)), func() {
			a.captureFormRow()
			genreW := a.genreFieldWidth(yearW + trackW + discW + bpmW)
			Container(Attrs(FixWidth(genreW), Gap(2)), func() {
				a.L("Genre", FontSize(11), TextColorVec(a.pal().textDim))
				at := DefaultTextInputAttrs()
				at.MinWidth = genreW
				a.input(&t.tags.Genre, at)
			})
			t.fixedField(a, "Year", &t.tags.Year, yearW)
			t.fixedField(a, "Track #", &t.tags.Track, trackW)
			t.fixedField(a, "Disc #", &t.tags.Disc, discW)
			t.fixedField(a, "BPM", &t.tags.BPM, bpmW)
		})
		Container(Attrs(Expand, Gap(2)), func() {
			a.L("Comment", FontSize(11), TextColorVec(a.pal().textDim))
			a.textArea(&t.tags.Comment)
		})
	})
}

// smallFieldWidths derives input widths for Year / Track # / Disc # / BPM
// from the widest value found in the loaded library rows (the DB data), so
// the fields are exactly as wide as their content needs to be.
func (t *TagsTool) smallFieldWidths(a *App) (year, track, disc, bpm float32) {
	maxYear, maxTrack, maxDisc, maxBPM := 0, 0, 0, 0
	for _, r := range a.Tracks {
		if l := digitsLen(r.Year); l > maxYear {
			maxYear = l
		}
		if l := digitsLen(r.PlayOrder); l > maxTrack {
			maxTrack = l
		}
		if l := digitsLen(r.BPMFile); l > maxBPM {
			maxBPM = l
		}
	}
	// Disc # has no dedicated DB column; the common "1".."9" is a good basis.
	if maxDisc < 1 {
		maxDisc = 1
	}
	return dataWidth(maxYear), dataWidth(maxTrack), dataWidth(maxDisc), dataWidth(maxBPM)
}

func digitsLen(n int64) int {
	if n <= 0 {
		return 0
	}
	return len(strconv.FormatInt(n, 10))
}

// dataWidth converts a max character count into an input width, with a
// floor for readability and a cap so fields never balloon.
func dataWidth(chars int) float32 {
	w := 30 + float32(chars)*9
	if w < 58 {
		w = 58
	}
	if w > 120 {
		w = 120
	}
	return w
}

// fieldFull renders a label + themed input spanning the full panel width.
func (t *TagsTool) fieldFull(a *App, label string, buf *string) {
	Container(Attrs(Expand, Gap(2)), func() {
		a.L(label, FontSize(11), TextColorVec(a.pal().textDim))
		a.input(buf, DefaultTextInputAttrs())
	})
}

// fixedField renders a label + themed input sized to its data (fixed width).
func (t *TagsTool) fixedField(a *App, label string, buf *string, w float32) {
	Container(Attrs(FixWidth(w), Gap(2)), func() {
		NextAccessName("field-" + sanitizeAccess(label))
		AssignAccess()
		a.L(label, FontSize(11), TextColorVec(a.pal().textDim))
		at := DefaultTextInputAttrs()
		at.MinWidth = w
		a.input(buf, at)
	})
}

func sanitizeAccess(label string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '#' {
			return -1
		}
		return r
	}, label)
}

// parseHashTags extracts the unique #tags of a comment, in order of
// appearance (without the leading '#'). Trailing punctuation is stripped, so
// "#pop," yields "pop".
func parseHashTags(comment string) []string {
	var out []string
	seen := map[string]bool{}
	for _, tok := range strings.Fields(comment) {
		if len(tok) < 2 || tok[0] != '#' {
			continue
		}
		name := strings.TrimLeft(tok[1:], "#")
		name = strings.TrimRight(name, ",.!?;:…")
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// removeTag rebuilds a comment without the given #tag (matching tokens with
// or without trailing punctuation).
func removeTag(comment, name string) string {
	var kept []string
	for _, tok := range strings.Fields(comment) {
		if strings.TrimRight(strings.TrimLeft(tok, "#"), ",.!?;:…") == name {
			continue
		}
		kept = append(kept, tok)
	}
	return strings.Join(kept, " ")
}

// addTag appends a #tag to the comment (no duplicates).
func addTag(comment, name string) string {
	if strings.Contains(" "+comment+" ", " #"+strings.TrimSpace(name)+" ") {
		return comment
	}
	name = strings.TrimSpace(name)
	if strings.ContainsAny(name, " \t") {
		return comment
	}
	if strings.TrimSpace(comment) == "" {
		return "#" + name
	}
	return strings.TrimSpace(comment) + " #" + name
}

// TagBubbles renders the comment's #tags as colored bubbles: click one to
// remove it from the comment, or add a new one below.
func (t *TagsTool) TagBubbles(a *App) {
	tags := parseHashTags(t.tags.Comment)
	p := a.pal()
	a.L("Tags", FontSize(11), TextColorVec(p.textDim))
	Container(Attrs(Expand, Gap(6), Pad2(2, 0)), func() {
		Container(Attrs(Row, Wrap, CrossMid, Gap(6)), func() {
			for _, name := range tags {
				n := name
				hue := tagHue(n)
				Container(Attrs(Row, CrossMid, Gap(4), Pad2(2, 9), Corners(10),
					Background(hue, 45, lightnessFor(a), 1), Gap(2)), func() {
					if IsHovered() {
						ModAttrs(Background(hue, 55, hoverLightnessFor(a), 1))
					}
					if PressAction() {
						t.tags.Comment = removeTag(t.tags.Comment, n)
					}
					a.L("#"+n, FontSize(12), TextColorVec(p.bubbleInk))
					a.L("×", FontSize(12), TextColorVec(p.bubbleInk))
				})
			}
			Container(Attrs(Grow(1), MinWidth(140), MaxWidth(220)), func() {
				at := DefaultTextInputAttrs()
				at.Placeholder = "#tag"
				a.input(&t.newTag, at)
			})
			if CtrlButton(SymIPlus, "Add", strings.TrimSpace(t.newTag) != "") {
				t.tags.Comment = addTag(t.tags.Comment, strings.TrimSpace(strings.TrimPrefix(t.newTag, "#")))
				t.newTag = ""
			}
			if CtrlButton(TypArrowSortedDown, "Sort tags", len(parseHashTags(t.tags.Comment)) > 1) {
				t.tags.Comment = sortCommentTags(t.tags.Comment)
			}
		})
	})
}

func lightnessFor(a *App) float32 {
	if a.dark() {
		return 32
	}
	return 80
}

func hoverLightnessFor(a *App) float32 {
	if a.dark() {
		return 40
	}
	return 72
}

// naturalLess compares two tag names (without '#') using natural order:
// runs of digits compare numerically, so "#9aaa" sorts before "#92test".
// Comparison is case-insensitive.
func naturalLess(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		ca, cb := a[i], b[j]
		if isDigitByte(ca) && isDigitByte(cb) {
			si, sj := i, j
			for i < len(a) && isDigitByte(a[i]) {
				i++
			}
			for j < len(b) && isDigitByte(b[j]) {
				j++
			}
			// Numeric compare: strip leading zeros, then shorter run wins;
			// equal length decides byte-wise (both are digit strings).
			ta := strings.TrimLeft(a[si:i], "0")
			tb := strings.TrimLeft(b[sj:j], "0")
			if len(ta) != len(tb) {
				return len(ta) < len(tb)
			}
			if ta != tb {
				return ta < tb
			}
			continue
		}
		if ca != cb {
			return ca < cb
		}
		i++
		j++
	}
	// Prefix case: the shorter name sorts first.
	return len(a)-i < len(b)-j
}

func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }

// sortCommentTags reorders the #tags of a comment in natural alphabetical
// order (numeric runs in numeric order: #9aaa before #92test). Non-tag words
// keep their relative order after the sorted tags.
func sortCommentTags(comment string) string {
	fields := strings.Fields(comment)
	var tags, rest []string
	for _, tok := range fields {
		if len(tok) > 1 && tok[0] == '#' {
			tags = append(tags, tok)
		} else {
			rest = append(rest, tok)
		}
	}
	sort.SliceStable(tags, func(i, j int) bool {
		return naturalLess(strings.TrimLeft(tags[i], "#"), strings.TrimLeft(tags[j], "#"))
	})
	return strings.Join(append(tags, rest...), " ")
}

// SaveRow shows the save controls and DB-sync toggle.
func (t *TagsTool) SaveRow(a *App, rec TrackRecord) {
	Container(Attrs(Row, CrossMid, Gap(10), Pad2(6, 0)), func() {
		if CtrlButton(SymITick, "Save tags to file", a.lib != nil && t.pathErr == "") {
			t.Save(a, rec)
		}
		CheckBox(&t.alsoDB, "Also update Engine DJ database")
		if t.saved {
			a.L("Saved ✓", TextColor(140, 45, 34, 1), FontSize(12))
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
