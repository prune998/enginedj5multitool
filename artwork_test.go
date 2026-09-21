package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	id3v2 "github.com/bogem/id3v2/v2"
)

// jpegFixture is a minimal byte sequence that sniffs as JPEG (not decodable).
var jpegFixture = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{0x00}, 64)...)

// pngFixture is a real 1x1 PNG (decodable by image/png).
func pngFixture() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func TestSniffImageMIME(t *testing.T) {
	if got := sniffImageMIME(jpegFixture, ""); got != "image/jpeg" {
		t.Errorf("jpeg sniff = %q", got)
	}
	if got := sniffImageMIME(pngFixture(), ""); got != "image/png" {
		t.Errorf("png sniff = %q", got)
	}
	if got := sniffImageMIME([]byte("hello"), "image/JPEG; charset=binary"); got != "image/jpeg" {
		t.Errorf("content-type fallback = %q", got)
	}
	if got := sniffImageMIME([]byte("hello"), "text/html"); got != "" {
		t.Errorf("non-image should be empty, got %q", got)
	}
}

func TestEmbeddedArtRoundTrip(t *testing.T) {
	p := mp3Fixture(t)

	// No artwork yet.
	art, err := ReadEmbeddedArt(p)
	if err != nil {
		t.Fatal(err)
	}
	if art != nil {
		t.Fatalf("expected no embedded art, got %+v", art)
	}

	// Save text tags + artwork.
	in := MediaTags{Title: "T", Artist: "A"}
	if err := SaveMediaTagsWithArt(p, in, &MediaArt{MIME: "image/jpeg", Data: jpegFixture}); err != nil {
		t.Fatalf("save with art: %v", err)
	}

	// Art comes back, and text tags survived.
	got, err := ReadEmbeddedArt(p)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.MIME != "image/jpeg" || !bytes.Equal(got.Data, jpegFixture) {
		t.Fatalf("embedded art mismatch: %+v", got)
	}
	// The APIC frame carries the standard description.
	tag, err := id3v2.Open(p, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatal(err)
	}
	frames := tag.GetFrames("APIC")
	if len(frames) != 1 {
		t.Fatalf("APIC frames = %d, want 1", len(frames))
	}
	if pf := frames[0].(id3v2.PictureFrame); pf.Description != "Album cover" || pf.PictureType != 3 {
		t.Errorf("picture frame = description %q, type %d; want \"Album cover\", type 3", pf.Description, pf.PictureType)
	}
	tag.Close()
	tags, err := ReadMediaTags(p)
	if err != nil {
		t.Fatal(err)
	}
	if tags.Title != "T" || tags.Artist != "A" {
		t.Errorf("tags = %+v", tags)
	}

	// Saving text-only again must PRESERVE the artwork.
	if err := SaveMediaTags(p, MediaTags{Title: "T2", Artist: "A"}); err != nil {
		t.Fatal(err)
	}
	art, err = ReadEmbeddedArt(p)
	if err != nil {
		t.Fatal(err)
	}
	if art == nil {
		t.Fatal("artwork lost after text-only save")
	}

	// Saving with new art replaces it.
	newArt := &MediaArt{MIME: "image/png", Data: pngFixture()}
	if err := SaveMediaTagsWithArt(p, MediaTags{Title: "T3", Artist: "A"}, newArt); err != nil {
		t.Fatal(err)
	}
	art, err = ReadEmbeddedArt(p)
	if err != nil {
		t.Fatal(err)
	}
	if art == nil || art.MIME != "image/png" || !bytes.Equal(art.Data, pngFixture()) {
		t.Fatalf("art replacement mismatch: %+v", art)
	}
}

func TestStripBrackets(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Behind The Wheel (Extended Mix By Fuvi Clan)", "Behind The Wheel"},
		{"Sweet Addiction (feat. Her) (Live Edit)", "Sweet Addiction"},
		{"Song [HQ] {2003 rip}", "Song"},
		{"(Live) Song", "Song"},
		{"Song (feat. X (remix))", "Song"}, // nested groups
		{"Emotion", "Emotion"},             // unchanged
		{"Song (((", "Song ((("},           // unmatched brackets left alone
		{"Multiple   spaces (x) here", "Multiple spaces here"},
	}
	for _, tc := range cases {
		if got := stripBrackets(tc.in); got != tc.want {
			t.Errorf("stripBrackets(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFetchArtMusicBrainz(t *testing.T) {
	var caaHits int
	var recordingQuery, releaseQuery url.Values
	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("MusicBrainz request missing User-Agent")
		}
		q := r.URL.Query()
		switch r.URL.Path {
		case "/recording":
			recordingQuery = q
			json.NewEncoder(w).Encode(map[string]any{
				"recordings": []map[string]any{{
					"releases": []map[string]any{{"id": "mbid-123"}},
				}},
			})
		case "/release":
			releaseQuery = q
			json.NewEncoder(w).Encode(map[string]any{
				"releases": []map[string]any{{"id": "album-mbid"}},
			})
		default:
			t.Errorf("unexpected MB path %q", r.URL.Path)
		}
	}))
	defer mb.Close()
	caa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caaHits++
		if r.URL.Path != "/mbid-123/front-500" {
			t.Errorf("CAA path = %q", r.URL.Path)
		}
		w.Write(jpegFixture)
	}))
	defer caa.Close()

	oldBase, oldCAA := musicBrainzBaseURL, coverArtURL
	musicBrainzBaseURL, coverArtURL = mb.URL, caa.URL
	defer func() { musicBrainzBaseURL, coverArtURL = oldBase, oldCAA }()

	// Primary: recording search by artist + song title.
	art, err := fetchArtMusicBrainz("Artist", "Song", "Album")
	if err != nil {
		t.Fatal(err)
	}
	if art.MIME != "image/jpeg" || !bytes.Equal(art.Data, jpegFixture) {
		t.Errorf("art = %+v", art)
	}
	if recordingQuery.Get("query") != `artist:"Artist" AND recording:"Song"` {
		t.Errorf("recording query = %q", recordingQuery.Get("query"))
	}
	if caaHits != 1 {
		t.Errorf("CAA hits = %d", caaHits)
	}

	// Fallback: no recording match → release search by album.
	caaHits = 0
	mb.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/recording":
			recordingQuery = q
			json.NewEncoder(w).Encode(map[string]any{"recordings": []any{}})
		case "/release":
			releaseQuery = q
			json.NewEncoder(w).Encode(map[string]any{
				"releases": []map[string]any{{"id": "album-mbid"}},
			})
		}
	})
	caa.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caaHits++
		if r.URL.Path != "/album-mbid/front-500" {
			t.Errorf("fallback CAA path = %q", r.URL.Path)
		}
		w.Write(jpegFixture)
	})
	if art, err := fetchArtMusicBrainz("Artist", "Song", "Album"); err != nil {
		t.Fatal(err)
	} else if len(art.Data) == 0 {
		t.Error("fallback art empty")
	}
	if recordingQuery.Get("query") != `artist:"Artist" AND recording:"Song"` {
		t.Errorf("fallback recording query = %q", recordingQuery.Get("query"))
	}
	if releaseQuery.Get("query") != `artist:"Artist" AND release:"Album"` {
		t.Errorf("fallback release query = %q", releaseQuery.Get("query"))
	}
	if caaHits != 1 {
		t.Errorf("fallback CAA hits = %d", caaHits)
	}

	// Neither recording nor album matches → clear error.
	mb.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/recording":
			json.NewEncoder(w).Encode(map[string]any{"recordings": []any{}})
		case "/release":
			json.NewEncoder(w).Encode(map[string]any{"releases": []any{}})
		}
	})
	if _, err := fetchArtMusicBrainz("Artist", "Song", "Album"); err == nil {
		t.Error("expected error when nothing matches")
	}
}

func TestFetchArtDiscogs(t *testing.T) {
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(jpegFixture)
	}))
	defer imgSrv.Close()

	var gotAuth string
	var searchQueries []url.Values
	ds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		searchQueries = append(searchQueries, r.URL.Query())
		if r.URL.Query().Get("track") != "" {
			// Title search: no match on the first call → exercises the fallback.
			json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"cover_image": imgSrv.URL + "/cover.jpg"}},
		})
	}))
	defer ds.Close()

	oldSearch := discogsSearchURL
	discogsSearchURL = ds.URL
	defer func() { discogsSearchURL = oldSearch }()

	if _, err := fetchArtDiscogs("Artist", "Song", "Album", ""); err == nil {
		t.Error("expected error without a token")
	}

	searchQueries = nil
	art, err := fetchArtDiscogs("Artist", "Song", "Album", "my-token")
	if err != nil {
		t.Fatal(err)
	}
	if art.MIME != "image/jpeg" || len(art.Data) == 0 {
		t.Errorf("art = %+v", art)
	}
	if gotAuth != "Discogs token=my-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if len(searchQueries) != 2 {
		t.Fatalf("search calls = %d, want 2 (title then album fallback)", len(searchQueries))
	}
	if searchQueries[0].Get("track") != "Song" || searchQueries[0].Get("artist") != "Artist" {
		t.Errorf("title search query = %v", searchQueries[0])
	}
	if searchQueries[1].Get("release_title") != "Album" {
		t.Errorf("album fallback query = %v", searchQueries[1])
	}
}

func ratingPtr(v int64) *int64 { return &v }

// popmFor returns the POPM frame with the given email ("" = any).
func popmFor(t *testing.T, path, email string) []id3v2.PopularimeterFrame {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tag.Close()
	var out []id3v2.PopularimeterFrame
	for _, f := range tag.GetFrames(frPOPM) {
		if pf, ok := f.(id3v2.PopularimeterFrame); ok {
			if email == "" || pf.Email == email {
				out = append(out, pf)
			}
		}
	}
	return out
}

func TestPOPMRatingWritten(t *testing.T) {
	p := mp3Fixture(t)

	// Save with a rating: POPM must carry 80/100 → 204 (linear 0-255 scale).
	if err := SaveMediaTagsFull(p, MediaTags{Title: "T"}, nil, ratingPtr(80)); err != nil {
		t.Fatal(err)
	}
	frames := popmFor(t, p, popmEmail)
	if len(frames) != 1 {
		t.Fatalf("got %d POPM frames for %q, want 1", len(frames), popmEmail)
	}
	if frames[0].Rating != 204 {
		t.Errorf("POPM rating = %d, want 204", frames[0].Rating)
	}

	// Updating the rating replaces our frame (no duplicates).
	if err := WriteRatingPOPM(p, 20); err != nil {
		t.Fatal(err)
	}
	frames = popmFor(t, p, popmEmail)
	if len(frames) != 1 || frames[0].Rating != 51 {
		t.Fatalf("after update: %d frames, rating %d; want 1 frame @ 51", len(frames), frames[0].Rating)
	}
}

func TestPOPMReplacesOnlyOurFrame(t *testing.T) {
	p := mp3Fixture(t)
	if err := SaveMediaTags(p, MediaTags{Title: "T"}); err != nil {
		t.Fatal(err)
	}
	// Another tool's popularimeter frame.
	tag, err := id3v2.Open(p, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatal(err)
	}
	tag.AddFrame(frPOPM, id3v2.PopularimeterFrame{
		Email: "itunes@localhost", Rating: 10, Counter: new(big.Int),
	})
	if err := tag.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writeTagToFile(p, tag); err != nil {
		t.Fatal(err)
	}

	// Our rating update must not touch the other tool's frame.
	if err := WriteRatingPOPM(p, 60); err != nil {
		t.Fatal(err)
	}
	ours := popmFor(t, p, popmEmail)
	if len(ours) != 1 || ours[0].Rating != 153 {
		t.Fatalf("our POPM = %+v, want 1 frame @ 153", ours)
	}
	theirs := popmFor(t, p, "itunes@localhost")
	if len(theirs) != 1 || theirs[0].Rating != 10 {
		t.Fatalf("other tool's POPM = %+v, want preserved @ 10", theirs)
	}
}

func TestSaveMediaTagsWithArtOnTaglessFile(t *testing.T) {
	p := mp3Fixture(t)
	if err := SaveMediaTagsWithArt(p, MediaTags{Title: "X"}, &MediaArt{MIME: "image/jpeg", Data: jpegFixture}); err != nil {
		t.Fatal(err)
	}
	art, err := ReadEmbeddedArt(p)
	if err != nil {
		t.Fatal(err)
	}
	if art == nil || art.MIME != "image/jpeg" {
		t.Fatalf("art = %+v", art)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}

// TestSaveWritesPadding walks the saved ID3v2 tag the way ExifTool does and
// asserts that trailing zero padding follows the last frame — ExifTool warns
// "Missing ID3 terminating frame" when a v2.4 tag ends flush at its last
// frame. Also checks the audio bytes and that repeated saves don't grow the
// file.
func TestSaveWritesPadding(t *testing.T) {
	p := mp3Fixture(t)
	audio := bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x00}, 512)

	if err := SaveMediaTags(p, MediaTags{Title: "Padded", Artist: "A"}); err != nil {
		t.Fatal(err)
	}
	walkTag := func(path string) (int, int, error) { // walkEnd, declaredEnd
		d, err := os.ReadFile(path)
		if err != nil {
			return 0, 0, err
		}
		if string(d[:3]) != "ID3" {
			return 0, 0, errors.New("no ID3 header")
		}
		size := int(d[6]&0x7f)<<21 | int(d[7]&0x7f)<<14 | int(d[8]&0x7f)<<7 | int(d[9]&0x7f)
		end := 10 + size
		off := 10
		for off+10 <= end {
			fid := string(d[off : off+4])
			if fid == "\x00\x00\x00\x00" {
				break // padding reached
			}
			// v2.4 sync-safe frame size
			fsize := int(d[off+4]&0x7f)<<21 | int(d[off+5]&0x7f)<<14 | int(d[off+6]&0x7f)<<7 | int(d[off+7]&0x7f)
			off += 10 + fsize
		}
		return off, end, nil
	}

	walkEnd, declaredEnd, err := walkTag(p)
	if err != nil {
		t.Fatal(err)
	}
	if walkEnd == declaredEnd {
		t.Fatal("tag ends flush at the last frame — ExifTool would warn 'Missing ID3 terminating frame'")
	}
	if declaredEnd-walkEnd < 4 {
		t.Fatalf("padding too small: %d bytes", declaredEnd-walkEnd)
	}
	// Padding must be zeros.
	d, _ := os.ReadFile(p)
	if pad := d[walkEnd:declaredEnd]; !bytes.Equal(pad, make([]byte, len(pad))) {
		t.Error("padding is not all zeros")
	}
	// Audio intact at the end of the file.
	if !bytes.HasSuffix(d, audio) {
		t.Error("audio data corrupted")
	}

	// Saving again must produce the same file size (padding is rebuilt, not
	// accumulated).
	s1, _ := os.Stat(p)
	if err := SaveMediaTags(p, MediaTags{Title: "Padded", Artist: "A"}); err != nil {
		t.Fatal(err)
	}
	s2, _ := os.Stat(p)
	if s1.Size() != s2.Size() {
		t.Errorf("file grew on re-save: %d -> %d", s1.Size(), s2.Size())
	}
}
