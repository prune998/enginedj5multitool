package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	id3v2 "github.com/bogem/id3v2/v2"
)

// MediaTags holds the editable MP3 (ID3v2) tags of a track.
type MediaTags struct {
	Title       string
	Artist      string
	Album       string
	AlbumArtist string
	Genre       string
	Year        string
	Track       string
	Disc        string
	Composer    string
	BPM         string
	Comment     string
}

// frame IDs used by the editor
const (
	frTitle       = "TIT2"
	frArtist      = "TPE1"
	frAlbumArtist = "TPE2"
	frAlbum       = "TALB"
	frGenre       = "TCON"
	frTrack       = "TRCK"
	frDisc        = "TPOS"
	frComposer    = "TCOM"
	frBPM         = "TBPM"
	frComment     = "COMM"
	frYearV3      = "TYER" // ID3v2.3
	frYearV4      = "TDRC" // ID3v2.4
)

func yearFrameID(tag *id3v2.Tag) string {
	if tag.Version() >= 4 {
		return frYearV4
	}
	return frYearV3
}

func getTextFrame(tag *id3v2.Tag, id string) string {
	return tag.GetTextFrame(id).Text
}

func setTextFrame(tag *id3v2.Tag, id, text string) {
	if text == "" {
		tag.DeleteFrames(id)
		return
	}
	tag.AddTextFrame(id, tag.DefaultEncoding(), text)
}

// ReadMediaTags reads the editable tags from an MP3 file. Files without an
// ID3v2 tag yield zero values (not an error).
func ReadMediaTags(path string) (MediaTags, error) {
	var t MediaTags
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return t, err
		}
		return t, nil // no/undecodable ID3v2 tag: treat as empty
	}
	defer tag.Close()

	t.Title = getTextFrame(tag, frTitle)
	t.Artist = getTextFrame(tag, frArtist)
	t.Album = getTextFrame(tag, frAlbum)
	t.AlbumArtist = getTextFrame(tag, frAlbumArtist)
	t.Genre = getTextFrame(tag, frGenre)
	t.Track = getTextFrame(tag, frTrack)
	t.Disc = getTextFrame(tag, frDisc)
	t.Composer = getTextFrame(tag, frComposer)
	t.BPM = getTextFrame(tag, frBPM)
	t.Year = getTextFrame(tag, frYearV4)
	if t.Year == "" {
		t.Year = getTextFrame(tag, frYearV3)
	}
	if cf, ok := commentFrame(tag); ok {
		t.Comment = cf.Text
	}
	return t, nil
}

func commentFrame(tag *id3v2.Tag) (id3v2.CommentFrame, bool) {
	frames := tag.GetFrames(frComment)
	if len(frames) == 0 {
		return id3v2.CommentFrame{}, false
	}
	// prefer a standard comment (empty description, "eng" language)
	for _, f := range frames {
		if cf, ok := f.(id3v2.CommentFrame); ok && cf.Description == "" {
			return cf, true
		}
	}
	if cf, ok := frames[0].(id3v2.CommentFrame); ok {
		return cf, true
	}
	return id3v2.CommentFrame{}, false
}

// setComment replaces the standard comment frame, keeping language and
// description of the existing one when present.
func setComment(tag *id3v2.Tag, text string) {
	existing, has := commentFrame(tag)
	lang, desc := "eng", ""
	if has {
		lang, desc = existing.Language, existing.Description
	}
	tag.DeleteFrames(frComment)
	if text == "" {
		return
	}
	tag.AddCommentFrame(id3v2.CommentFrame{
		Encoding:    tag.DefaultEncoding(),
		Language:    lang,
		Description: desc,
		Text:        text,
	})
}

// SaveMediaTags writes the editable tags to an MP3 file, preserving the
// existing ID3v2 version and all untouched frames (album art, TBPM from
// analysis, custom frames...). Files without an existing ID3v2 tag get one.
func SaveMediaTags(path string, t MediaTags) error {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err == nil {
		defer tag.Close()
		applyMediaTags(tag, t)
		return tag.Save()
	}
	if errors.Is(err, os.ErrNotExist) {
		return err
	}
	// No readable ID3v2 tag: create a fresh one and prepend it to the audio.
	return writeFreshTag(path, t)
}

func applyMediaTags(tag *id3v2.Tag, t MediaTags) {
	yearID := yearFrameID(tag)
	setTextFrame(tag, frTitle, t.Title)
	setTextFrame(tag, frArtist, t.Artist)
	setTextFrame(tag, frAlbum, t.Album)
	setTextFrame(tag, frAlbumArtist, t.AlbumArtist)
	setTextFrame(tag, frGenre, t.Genre)
	setTextFrame(tag, frTrack, t.Track)
	setTextFrame(tag, frDisc, t.Disc)
	setTextFrame(tag, frComposer, t.Composer)
	setTextFrame(tag, frBPM, t.BPM)
	setTextFrame(tag, yearID, t.Year)
	// drop the "other" year frame so versions don't disagree
	other := frYearV3
	if yearID == frYearV3 {
		other = frYearV4
	}
	if getTextFrame(tag, other) == t.Year {
		tag.DeleteFrames(other)
	}
	setComment(tag, t.Comment)
}

// writeFreshTag builds a new ID3v2 tag from scratch and prepends it to the
// raw audio data of a file that has no tag yet.
func writeFreshTag(path string, t MediaTags) error {
	audio, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if isID3Header(audio) {
		return fmt.Errorf("%s: has an unparsable ID3v2 tag", path)
	}
	tag := id3v2.NewEmptyTag()
	applyMediaTags(tag, t)
	var buf bytes.Buffer
	if _, err := tag.WriteTo(&buf); err != nil {
		return err
	}
	buf.Write(audio)
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func isID3Header(data []byte) bool {
	return len(data) >= 10 && string(data[:3]) == "ID3"
}

// IsMP3 reports whether the track's file looks like an MP3.
func IsMP3(rec TrackRecord) bool {
	return strings.EqualFold(strings.TrimSpace(rec.FileType), "mp3") ||
		strings.HasSuffix(strings.ToLower(rec.Path), ".mp3")
}
