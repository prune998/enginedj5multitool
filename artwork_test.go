package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
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

func TestFetchArtMusicBrainz(t *testing.T) {
	var caaHits int
	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("MusicBrainz request missing User-Agent")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"releases": []map[string]any{{"id": "mbid-123"}},
		})
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

	oldSearch, oldCAA := musicBrainzSearchURL, coverArtURL
	musicBrainzSearchURL, coverArtURL = mb.URL, caa.URL
	defer func() { musicBrainzSearchURL, coverArtURL = oldSearch, oldCAA }()

	art, err := fetchArtMusicBrainz("Artist", "Album")
	if err != nil {
		t.Fatal(err)
	}
	if art.MIME != "image/jpeg" || !bytes.Equal(art.Data, jpegFixture) {
		t.Errorf("art = %+v", art)
	}
	if caaHits != 1 {
		t.Errorf("CAA hits = %d", caaHits)
	}

	// No matching release → clear error.
	musicBrainzSearchURL = mb.URL // still up
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"releases": []any{}})
	}))
	defer empty.Close()
	musicBrainzSearchURL = empty.URL
	if _, err := fetchArtMusicBrainz("Artist", "Album"); err == nil {
		t.Error("expected error for no results")
	}
}

func TestFetchArtDiscogs(t *testing.T) {
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(jpegFixture)
	}))
	defer imgSrv.Close()

	var gotAuth string
	ds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"cover_image": imgSrv.URL + "/cover.jpg"}},
		})
	}))
	defer ds.Close()

	oldSearch := discogsSearchURL
	discogsSearchURL = ds.URL
	defer func() { discogsSearchURL = oldSearch }()

	if _, err := fetchArtDiscogs("Artist", "Album", ""); err == nil {
		t.Error("expected error without a token")
	}

	art, err := fetchArtDiscogs("Artist", "Album", "my-token")
	if err != nil {
		t.Fatal(err)
	}
	if art.MIME != "image/jpeg" || len(art.Data) == 0 {
		t.Errorf("art = %+v", art)
	}
	if gotAuth != "Discogs token=my-token" {
		t.Errorf("Authorization = %q", gotAuth)
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
