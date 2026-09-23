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

	path     string // resolved audio file path
	pathText string // display copy of the path (selectable input)
	pathErr  string // file missing / not an mp3

	tags     MediaTags
	art      *MediaArt // embedded artwork from the file
	readErr  string
	readOnly bool // WAV: tags are shown but cannot be saved

	// artwork download state (mutated from the download goroutine under the
	// frame lock, read during frames)
	artBusy      string
	artErr       string
	pending      *MediaArt
	pendingSrc   string
	discogsToken string

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
				Cell: func(r TrackRecord) { a.L(upper(r.FileType), FontSize(a.fs(12))) },
				Less: func(a, b TrackRecord) bool { return a.FileType < b.FileType },
			}
			a.BrowserPanel(extra)
		})

		a.Splitter()

		Container(Attrs(Grow(1), Expand, Viewport, Pad2(0, 10), Gap(8)), func() {
			t.EditorPanel(a)
			ScrollBars()
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
	a.L("MP3 Tags", FontSize(a.fs(18)), FontWeight(WeightBold))

	rec, ok := a.SelectedTrack()
	if !ok || a.Selected == 0 {
		a.L("Select a track on the left.", TextColorVec(a.pal().textDim))
		return
	}
	t.ensureLoaded(a, rec)

	// File info
	Container(Attrs(Gap(2), Pad2(4, 0)), func() {
		Container(Attrs(Row, CrossMid, Gap(6)), func() {
			Icon(SymAudio, FontSize(a.fs(13)), TextColorVec(a.pal().textDim))
			a.L(fmt.Sprintf("#%d  %s — %s", rec.ID, rec.Artist, rec.Title), FontWeight(WeightBold))
		})
		if t.pathErr != "" {
			a.errorText(t.pathErr, a.paneTextWidth())
		} else {
			// Selectable path: select with the mouse and Cmd-C, or use the
			// Copy button. Re-bound each frame so edits never stick.
			t.pathText = t.path
			Container(Attrs(Row, CrossMid, Gap(8)), func() {
				Container(Attrs(Grow(1)), func() {
					a.input(&t.pathText, DefaultTextInputAttrs())
				})
				if CtrlButton(SymCopy, "Copy", true) {
					RequestTextCopy(t.path)
					Toast(SymITick, "Copied", "File path placed on the clipboard.")
				}
			})
		}
	})

	if t.readErr != "" {
		a.errorText("Error: "+t.readErr, a.paneTextWidth())
		return
	}
	if !IsTaggable(rec) {
		a.L("Unsupported file type — tag editing supports MP3 (ID3v2) and M4A (MP4) files.",
			TextColor(40, 70, 40, 1))
		return
	}

	t.ArtworkPanel(a, rec)
	t.RatingRow(a, rec)
	t.Form(a)
	t.TagBubbles(a)
	a.playbackSection(rec)
	t.SaveRow(a, rec)
}

// RatingRow renders the 5-star rating editor. The rating lives in the Engine
// DJ database (not the file tags), so changes are written immediately and the
// browser list updates in place.
func (t *TagsTool) RatingRow(a *App, rec TrackRecord) {
	p := a.pal()
	Container(Attrs(Row, CrossMid, Gap(10), Pad2(4, 0)), func() {
		a.L("Rating", FontWeight(WeightBold), FontSize(a.fs(14)))
		a.stars(rec.Rating, 20, func(star int) {
			rating := int64(star * 20)
			if star == 1 && rec.Rating == 20 {
				rating = 0 // clicking the single filled star clears the rating
			}
			if err := a.lib.SetTrackRating(rec.ID, rating); err != nil {
				Toast(SymFail, "Rating write failed", err.Error())
				return
			}
			// Mirror the rating into the file's POPM frame (MP3 only —
			// M4A ratings live in the Engine DJ database).
			if t.pathErr == "" && IsMP3(rec) {
				if err := WriteRatingPOPM(t.path, rating); err != nil {
					Toast(SymFail, "POPM write failed", err.Error())
				}
			}
			rec.Rating = rating
			for i := range a.Tracks {
				if a.Tracks[i].ID == rec.ID {
					a.Tracks[i].Rating = rating
					break
				}
			}
		})
		a.L(fmt.Sprintf("(%d/5 — stored in the Engine DJ database)", rec.Rating/20),
			FontSize(a.fs(11)), TextColorVec(p.textDim))
	})
}

// ArtworkPanel shows the embedded cover (or the downloaded preview) above the
// tag form, with buttons to fetch art from MusicBrainz / Discogs.
func (t *TagsTool) ArtworkPanel(a *App, rec TrackRecord) {
	p := a.pal()
	const boxSize = float32(150)

	Container(Attrs(Row, Gap(12), Pad2(4, 0)), func() {
		// Artwork box.
		Container(Attrs(FixSize(boxSize, boxSize), Corners(8), Clip,
			BackgroundVec(p.swatchEmpty), BorderWidth(1), BorderColorVec(p.inputBorder)), func() {
			src, key := t.displayArt()
			if src != nil {
				if rgba := decodeRGBA(src.Data); rgba != nil {
					id := UseImage(key, rgba)
					ImageView(id, Vec2{boxSize, boxSize})
					return
				}
			}
			Container(Attrs(Expand, Row, CrossMid), func() {
				a.L("no artwork", FontSize(a.fs(12)), TextColorVec(p.textDim))
			})
		})

		// Status + download controls.
		Container(Attrs(Grow(1), Gap(6)), func() {
			a.L("Artwork", FontWeight(WeightBold), FontSize(a.fs(14)))

			note := "No artwork embedded in this file."
			if t.art != nil {
				note = fmt.Sprintf("Embedded: %s, %d KB.", t.art.MIME, len(t.art.Data)/1024)
			}
			if t.pending != nil {
				note = fmt.Sprintf("Downloaded from %s (%d KB) — embedded when you Save.", t.pendingSrc, len(t.pending.Data)/1024)
			}
			a.L(note, FontSize(a.fs(11)), TextColorVec(p.textDim))

			Container(Attrs(Row, CrossMid, Gap(8)), func() {
				busy := t.artBusy != ""
				if CtrlButton(SymCloud, "MusicBrainz", !busy) {
					t.downloadArt(a, "MusicBrainz", rec)
				}
				if CtrlButton(SymDownload, "Discogs", !busy) {
					t.downloadArt(a, "Discogs", rec)
				}
			})

			if t.artBusy != "" {
				Container(Attrs(Row, CrossMid, Gap(6)), func() {
					BusyDots()
					a.L("Searching "+t.artBusy+"…", FontSize(a.fs(12)), TextColorVec(p.textDim))
				})
			} else if t.artErr != "" {
				a.errorText(t.artErr, a.paneTextWidth())
			} else if t.discogsToken == "" {
				a.L("Discogs downloads need a personal access token — set it in Settings.",
					FontSize(a.fs(11)), TextColorVec(p.textDim))
			}
		})
	})
}

// displayArt picks the artwork to show (downloaded preview wins over the
// embedded one) and a stable cache key for shirei's image registry.
func (t *TagsTool) displayArt() (*MediaArt, string) {
	if t.pending != nil {
		return t.pending, fmt.Sprintf("pending-%s-%d", t.pendingSrc, t.lastSel)
	}
	if t.art != nil {
		return t.art, fmt.Sprintf("embedded-%d-%x", t.lastSel, len(t.art.Data))
	}
	return nil, ""
}

// downloadArt fetches cover art in the background; results land under the
// frame lock so the frame function can read them safely. The search uses the
// bare artist name and song title — bracketed suffixes like "(Extended Mix)"
// are stripped — falling back to the album.
func (t *TagsTool) downloadArt(a *App, source string, rec TrackRecord) {
	artist := firstNonEmpty(t.tags.Artist, rec.Artist)
	title := stripBrackets(firstNonEmpty(t.tags.Title, rec.Title))
	if title == "" {
		title = strings.TrimSpace(firstNonEmpty(t.tags.Title, rec.Title))
	}
	album := stripBrackets(firstNonEmpty(t.tags.Album, rec.Album))
	if strings.TrimSpace(artist) == "" || (title == "" && album == "") {
		Toast(SymFail, "Cannot search", "No artist/title to search for.")
		return
	}
	t.artBusy = source
	t.artErr = ""
	token := t.discogsToken

	go func() {
		var art *MediaArt
		var err error
		switch source {
		case "Discogs":
			art, err = fetchArtDiscogs(artist, title, album, token)
		default:
			art, err = fetchArtMusicBrainz(artist, title, album)
		}
		WithFrameLock(func() {
			t.artBusy = ""
			if err != nil {
				t.artErr = err.Error()
			} else {
				t.pending = art
				t.pendingSrc = source
				t.artErr = ""
			}
		})
		RequestNextFrame()
	}()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ensureLoaded (re)reads the tags from the file whenever the selection changes.
func (t *TagsTool) ensureLoaded(a *App, rec TrackRecord) {
	if a.Selected == t.lastSel && (t.readErr != "" || t.path != "" || t.pathErr != "") {
		return
	}
	t.lastSel = a.Selected
	t.saved = false
	t.pending, t.pendingSrc, t.artBusy, t.artErr = nil, "", "", ""
	t.path = a.lib.ResolveMedia(rec.Path)
	t.tags = MediaTags{}
	t.art = nil
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
	if art, err := ReadEmbeddedArt(t.path); err == nil {
		t.art = art
	}
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
				a.L("Genre", FontSize(a.fs(11)), TextColorVec(a.pal().textDim))
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
			a.L("Comment", FontSize(a.fs(11)), TextColorVec(a.pal().textDim))
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
		a.L(label, FontSize(a.fs(11)), TextColorVec(a.pal().textDim))
		a.input(buf, DefaultTextInputAttrs())
	})
}

// fixedField renders a label + themed input sized to its data (fixed width).
func (t *TagsTool) fixedField(a *App, label string, buf *string, w float32) {
	Container(Attrs(FixWidth(w), Gap(2)), func() {
		NextAccessName("field-" + sanitizeAccess(label))
		AssignAccess()
		a.L(label, FontSize(a.fs(11)), TextColorVec(a.pal().textDim))
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
	a.L("Tags", FontSize(a.fs(11)), TextColorVec(p.textDim))
	Container(Attrs(Expand, Gap(6), Pad2(2, 0)), func() {
		// The bubbles wrap freely on their own lines...
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
					a.L("#"+n, FontSize(a.fs(12)), TextColorVec(p.bubbleInk))
					a.L("×", FontSize(a.fs(12)), TextColorVec(p.bubbleInk))
				})
			}
		})
		// ...and the add/sort controls sit on one line below them all.
		Container(Attrs(Row, CrossMid, Gap(6), Pad2(0, 4)), func() {
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
// statusTagRank orders the state tags that always sort to the very end
// (rightmost) of the comment: #cued, #looped, then #stem. They flag track
// readiness, not a genre.
var statusTagRank = map[string]int{"cued": 0, "looped": 1, "stem": 2}

func sortCommentTags(comment string) string {
	fields := strings.Fields(comment)
	var tags, last, rest []string
	for _, tok := range fields {
		if len(tok) > 1 && tok[0] == '#' {
			if _, ok := statusTagRank[strings.TrimLeft(tok, "#")]; ok {
				last = append(last, tok)
			} else {
				tags = append(tags, tok)
			}
		} else {
			rest = append(rest, tok)
		}
	}
	sort.SliceStable(tags, func(i, j int) bool {
		return naturalLess(strings.TrimLeft(tags[i], "#"), strings.TrimLeft(tags[j], "#"))
	})
	// Status tags keep their fixed relative order (cued → looped → stem).
	sort.SliceStable(last, func(i, j int) bool {
		return statusTagRank[strings.TrimLeft(last[i], "#")] < statusTagRank[strings.TrimLeft(last[j], "#")]
	})
	tags = append(tags, last...)
	return strings.Join(append(tags, rest...), " ")
}

// SaveRow shows the save controls and DB-sync toggle.
func (t *TagsTool) SaveRow(a *App, rec TrackRecord) {
	Container(Attrs(Row, CrossMid, Gap(10), Pad2(6, 0)), func() {
		if CtrlButton(SymITick, "Save changes", a.lib != nil && t.pathErr == "") {
			t.Save(a, rec)
		}
		CheckBox(&t.alsoDB, "Also update Engine DJ database")
		if t.saved {
			a.L("Saved ✓", TextColor(140, 45, 34, 1), FontSize(a.fs(12)))
		}
	})
}

func (t *TagsTool) Save(a *App, rec TrackRecord) {
	var err error
	rating := rec.Rating
	if t.pending != nil {
		err = SaveMediaTagsFull(t.path, t.tags, t.pending, &rating)
	} else {
		err = SaveMediaTagsFull(t.path, t.tags, nil, &rating)
	}
	if err != nil {
		Toast(SymFail, "Tag write failed", err.Error())
		return
	}
	msg := "ID3v2 tags written to " + baseName(t.path)
	if t.pending != nil {
		msg += fmt.Sprintf(" (+%d KB artwork)", len(t.pending.Data)/1024)
		t.art = t.pending
		t.pending = nil
	}
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
