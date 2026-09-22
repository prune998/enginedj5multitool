package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// mp3Fixture writes a small dummy MP3 (real frame sync header, no ID3 tag).
func mp3Fixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "song.mp3")
	// 0xFF 0xFB = MPEG-1 Layer III frame sync
	if err := os.WriteFile(p, bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x00}, 512), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadMediaTagsTaglessFile(t *testing.T) {
	p := mp3Fixture(t)
	tags, err := ReadMediaTags(p)
	if err != nil {
		t.Fatalf("tagless file should read as empty, got error: %v", err)
	}
	if tags != (MediaTags{}) {
		t.Errorf("expected zero tags, got %+v", tags)
	}
}

func TestMediaTagsRoundTrip(t *testing.T) {
	p := mp3Fixture(t)

	in := MediaTags{
		Title: "Song", Artist: "Artist", Album: "Album", AlbumArtist: "Album Artist",
		Genre: "House", Year: "2024", Track: "3/12", Disc: "1/2",
		Composer: "Composer", BPM: "124", Comment: "a comment",
	}
	if err := SaveMediaTags(p, in); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := ReadMediaTags(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != in {
		t.Errorf("round trip mismatch:\n got  %+v\n want %+v", got, in)
	}

	// Second save goes through the "existing tag" path and must update.
	in.Title = "Renamed"
	in.Comment = ""
	if err := SaveMediaTags(p, in); err != nil {
		t.Fatalf("second save: %v", err)
	}
	got, err = ReadMediaTags(p)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if got != in {
		t.Errorf("update mismatch:\n got  %+v\n want %+v", got, in)
	}
}

func TestSaveMediaTagsMissingFile(t *testing.T) {
	err := SaveMediaTags(filepath.Join(t.TempDir(), "nope.mp3"), MediaTags{})
	if err == nil {
		t.Error("expected error for missing file")
	}
	if _, err := ReadMediaTags(filepath.Join(t.TempDir(), "nope.mp3")); err == nil {
		t.Error("expected error reading missing file")
	}
}

func TestResolveMediaPath(t *testing.T) {
	root := t.TempDir()
	dbDir := filepath.Join(root, "Engine Library")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Relative path with ../ chains that resolves inside the DB dir.
	lib := filepath.Join(root, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	song := filepath.Join(lib, "song.mp3")
	if err := os.WriteFile(song, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ResolveMediaPath(dbDir, "", "", "../lib/song.mp3"); got != song {
		t.Errorf("relative to dbDir: got %q, want %q", got, song)
	}
	// With an Engine Library folder configured, the stored path resolves
	// against IT (../ chains climb out of the Engine Library folder).
	eng := filepath.Join(root, "Engine Library")
	if err := os.MkdirAll(eng, 0o755); err != nil {
		t.Fatal(err)
	}
	engRel, err := filepath.Rel(eng, song)
	if err != nil {
		t.Fatal(err)
	}
	engRel = filepath.ToSlash(engRel)
	if got := ResolveMediaPath(dbDir, "", eng, engRel); got != song {
		t.Errorf("engine-relative climb: got %q, want %q", got, song)
	}

	// Music.app media-tree flavors: the stored path is anchored at an artist
	// folder inside ~/Music/Music/Media/Music, but the DB lives elsewhere.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows reads this
	mediaMusic := filepath.Join(home, "Music", "Music", "Media", "Music")
	artistDir := filepath.Join(mediaMusic, "Aerosmith")
	if err := os.MkdirAll(artistDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mediaSong := filepath.Join(artistDir, "Sweet Emotion.mp3")
	if err := os.WriteFile(mediaSong, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// ../Music/Media/Music/<artist>/<file> → resolved via the home Music root.
	if got := ResolveMediaPath(dbDir, "", "", "../Music/Media/Music/Aerosmith/Sweet Emotion.mp3"); got != mediaSong {
		t.Errorf("../Music flavor: got %q, want %q", got, mediaSong)
	}
	// ./Music/Media/Music/<artist>/<file> → same target.
	if got := ResolveMediaPath(dbDir, "", "", "./Music/Media/Music/Aerosmith/Sweet Emotion.mp3"); got != mediaSong {
		t.Errorf("./Music flavor: got %q, want %q", got, mediaSong)
	}
	// Artist-anchored flavor ../<artist>/<file> → resolved via the media
	// Music root with the dots stripped.
	if got := ResolveMediaPath(dbDir, "", "", "../Aerosmith/Sweet Emotion.mp3"); got != mediaSong {
		t.Errorf("artist-anchored flavor: got %q, want %q", got, mediaSong)
	}

	// musicRoot override wins when the file exists there.
	musicRoot := filepath.Join(root, "music")
	artist := filepath.Join(musicRoot, "artist")
	if err := os.MkdirAll(artist, 0o755); err != nil {
		t.Fatal(err)
	}
	song2 := filepath.Join(artist, "song.mp3")
	if err := os.WriteFile(song2, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ResolveMediaPath(dbDir, musicRoot, "", "artist/song.mp3"); got != song2 {
		t.Errorf("musicRoot: got %q, want %q", got, song2)
	}

	// Absolute paths pass through untouched.
	if got := ResolveMediaPath(dbDir, musicRoot, "", song); got != song {
		t.Errorf("absolute: got %q, want %q", got, song)
	}

	// Unresolvable path: falls back to a plausible candidate (no crash).
	got := ResolveMediaPath(dbDir, "", "", "../../nope/song.mp3")
	if got == "" {
		t.Error("expected fallback candidate, got empty string")
	}
}

func TestIsMP3(t *testing.T) {
	if !IsMP3(TrackRecord{FileType: "mp3"}) {
		t.Error("fileType mp3 should match")
	}
	if !IsMP3(TrackRecord{Path: "/x/Song.MP3"}) {
		t.Error("extension .MP3 should match")
	}
	if IsMP3(TrackRecord{FileType: "m4a", Path: "/x/Song.m4a"}) {
		t.Error("m4a should not match")
	}
}

func TestParseYear(t *testing.T) {
	if y, ok := parseYear(" 2024 "); !ok || y != 2024 {
		t.Errorf("parseYear(2024) = %d, %v", y, ok)
	}
	if _, ok := parseYear(""); ok {
		t.Error("empty year should not parse")
	}
	if _, ok := parseYear("not-a-year"); ok {
		t.Error("garbage year should not parse")
	}
}

func TestResolveMediaPathEngineLibrary(t *testing.T) {
	dir := t.TempDir()
	eng := filepath.Join(dir, "Engine Library")
	if err := os.MkdirAll(filepath.Join(eng, "Artist"), 0o755); err != nil {
		t.Fatal(err)
	}
	song := filepath.Join(eng, "Artist", "Song.mp3")
	if err := os.WriteFile(song, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ResolveMediaPath(dir, "", eng, "Artist/Song.mp3"); got != song {
		t.Errorf("engine-library candidate: got %q, want %q", got, song)
	}
}
