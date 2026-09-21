package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
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

// MediaArt is an embedded or downloaded picture (MP3 APIC frame).
type MediaArt struct {
	MIME string
	Data []byte
}

// ReadEmbeddedArt returns the artwork embedded in an MP3 file, preferring the
// front cover (APIC picture type 3). Returns nil when the file has none.
func ReadEmbeddedArt(path string) (*MediaArt, error) {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, nil // no/undecodable tag: no artwork
	}
	defer tag.Close()

	frames := tag.GetFrames(frPicture)
	var any *MediaArt
	for _, f := range frames {
		pf, ok := f.(id3v2.PictureFrame)
		if !ok || len(pf.Picture) == 0 {
			continue
		}
		mime := pf.MimeType
		if m := sniffImageMIME(pf.Picture, mime); m != "" {
			mime = m
		}
		art := &MediaArt{MIME: mime, Data: pf.Picture}
		if pf.PictureType == 3 { // front cover wins
			return art, nil
		}
		if any == nil {
			any = art
		}
	}
	return any, nil
}

const frPicture = "APIC"
const frPOPM = "POPM"

// popmEmail identifies our popularimeter frame so updates never touch frames
// written by other tools (iTunes, Mixed In Key, ...).
const popmEmail = "enginedj5multitool@localhost"

// applyPOPM stores a 0-100 rating in the file's POPM (popularimeter) frame.
// The 0-100 scale maps linearly onto the 0-255 POPM range, which puts the
// 5-star steps exactly on the usual 51/102/153/204/255 star thresholds.
// Frames from other tools (different email) are preserved.
func applyPOPM(tag *id3v2.Tag, rating int64) {
	if rating < 0 {
		rating = 0
	}
	if rating > 100 {
		rating = 100
	}
	tag.AddFrame(frPOPM, id3v2.PopularimeterFrame{
		Email:   popmEmail,
		Rating:  uint8(rating * 255 / 100),
		Counter: new(big.Int),
	})
}

// WriteRatingPOPM updates the POPM frame of a file with a 0-100 rating.
func WriteRatingPOPM(path string, rating int64) error {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return err
	}
	applyPOPM(tag, rating)
	if cerr := tag.Close(); cerr != nil {
		return cerr
	}
	return writeTagToFile(path, tag)
}

// SaveMediaTags writes the editable tags to an MP3 file, preserving the
// existing ID3v2 version and all untouched frames (album art, TBPM from
// analysis, custom frames...). Files without an existing ID3v2 tag get one.
func SaveMediaTags(path string, t MediaTags) error {
	return saveMediaTags(path, t, nil, nil)
}

// SaveMediaTagsWithArt behaves like SaveMediaTags and additionally replaces
// the embedded artwork with art (when art is non-nil).
func SaveMediaTagsWithArt(path string, t MediaTags, art *MediaArt) error {
	return saveMediaTags(path, t, art, nil)
}

// SaveMediaTagsFull writes the editable tags and optionally replaces the
// embedded artwork (art) and/or stores the 0-100 rating in the POPM frame
// (rating).
func SaveMediaTagsFull(path string, t MediaTags, art *MediaArt, rating *int64) error {
	return saveMediaTags(path, t, art, rating)
}

func saveMediaTags(path string, t MediaTags, art *MediaArt, rating *int64) error {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err == nil {
		applyMediaTags(tag, t)
		applyArt(tag, art)
		if rating != nil {
			applyPOPM(tag, *rating)
		}
		// Close before rewriting: on Windows the file must not be open for
		// the rename below to succeed.
		if cerr := tag.Close(); cerr != nil {
			return cerr
		}
		return writeTagToFile(path, tag)
	}
	if errors.Is(err, os.ErrNotExist) {
		return err
	}
	// No readable ID3v2 tag: create a fresh one and prepend it to the audio.
	return writeFreshTag(path, t, art, rating)
}

// applyArt replaces the embedded picture when art is non-nil; otherwise the
// existing artwork frames are left untouched.
func applyArt(tag *id3v2.Tag, art *MediaArt) {
	if art == nil || len(art.Data) == 0 {
		return
	}
	tag.DeleteFrames(frPicture)
	tag.AddAttachedPicture(id3v2.PictureFrame{
		Encoding:    tag.DefaultEncoding(),
		MimeType:    art.MIME,
		PictureType: 3, // front cover
		Description: "Album cover",
		Picture:     art.Data,
	})
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
func writeFreshTag(path string, t MediaTags, art *MediaArt, rating *int64) error {
	header := make([]byte, 10)
	if f, err := os.Open(path); err == nil {
		_, rerr := io.ReadFull(f, header)
		f.Close()
		if rerr == nil && isID3Header(header[:]) {
			return fmt.Errorf("%s: has an unparsable ID3v2 tag", path)
		}
	}
	tag := id3v2.NewEmptyTag()
	applyMediaTags(tag, t)
	applyArt(tag, art)
	if rating != nil {
		applyPOPM(tag, *rating)
	}
	return writeTagToFile(path, tag)
}

const id3Padding = 1024 // trailing zero bytes written after the last frame

// syncsafeBytes encodes n as a 4-byte sync-safe integer (ID3v2 size field).
func syncsafeBytes(n uint32) [4]byte {
	return [4]byte{byte(n>>21) & 0x7f, byte(n>>14) & 0x7f, byte(n>>7) & 0x7f, byte(n) & 0x7f}
}

// syncsafeValue decodes a 4-byte sync-safe integer.
func syncsafeValue(b []byte) uint32 {
	return uint32(b[0]&0x7f)<<21 | uint32(b[1]&0x7f)<<14 | uint32(b[2]&0x7f)<<7 | uint32(b[3]&0x7f)
}

// writeTagToFile serializes the tag (header + frames) followed by trailing
// zero padding that is INCLUDED in the declared tag size, then the audio
// data, writing through a temp file + rename.
//
// The padding matters: readers like ExifTool warn "Missing ID3 terminating
// frame" when a v2.4 tag's last frame ends flush at the declared size.
func writeTagToFile(path string, tag *id3v2.Tag) error {
	// Where the audio starts in the current file (after the old tag, if any).
	audioStart := int64(0)
	if f, err := os.Open(path); err == nil {
		var header [10]byte
		_, rerr := io.ReadFull(f, header[:])
		f.Close()
		if rerr == nil && isID3Header(header[:]) {
			audioStart = 10 + int64(syncsafeValue(header[6:10]))
		}
	}

	// Serialize the tag (header + frames; empty when there are no frames —
	// in that case the old tag is simply stripped).
	var tagBuf bytes.Buffer
	if tag.Count() > 0 {
		if _, err := tag.WriteTo(&tagBuf); err != nil {
			return err
		}
		if tagBuf.Len() < 10 {
			return fmt.Errorf("%s: serialized tag too short (%d bytes)", path, tagBuf.Len())
		}
		// Patch the declared size to include the padding.
		total := uint32(tagBuf.Len() - 10 + id3Padding)
		ss := syncsafeBytes(total)
		copy(tagBuf.Bytes()[6:10], ss[:])
	}

	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode()
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".id3v2-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if tagBuf.Len() > 0 {
		if _, err := tmp.Write(tagBuf.Bytes()); err != nil {
			return err
		}
		if _, err := tmp.Write(make([]byte, id3Padding)); err != nil {
			return err
		}
	}

	// Copy the audio (everything after the old tag — this also preserves an
	// ID3v1 tag at the end of the file).
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	if _, err := src.Seek(audioStart, io.SeekStart); err != nil {
		src.Close()
		return err
	}
	_, err = io.Copy(tmp, src)
	src.Close()
	if err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	committed = true
	return nil
}

func isID3Header(data []byte) bool {
	return len(data) >= 10 && string(data[:3]) == "ID3"
}

// IsMP3 reports whether the track's file looks like an MP3.
func IsMP3(rec TrackRecord) bool {
	return strings.EqualFold(strings.TrimSpace(rec.FileType), "mp3") ||
		strings.HasSuffix(strings.ToLower(rec.Path), ".mp3")
}
