package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bogem/id3v2/v2"
	. "go.hasen.dev/shirei"
	"go.hasen.dev/shirei/drive"
)

// buildTestLibrary creates a minimal Engine DJ database with two tracks.
func buildTestLibrary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "m.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := `
	CREATE TABLE Track (
		id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT, artist TEXT, album TEXT,
		filename TEXT, path TEXT, fileType TEXT, bpmAnalyzed REAL, length INTEGER
	);
	CREATE TABLE PerformanceData (
		trackId INTEGER PRIMARY KEY, trackData BLOB, quickCues BLOB, loops BLOB, activeOnLoadLoops INTEGER
	);`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	tracks := []struct {
		id    int64
		title string
		art   string
	}{
		{5, "Emotion", "Purple Disco Machine"},
		{7, "Galaxy", "Alex Metric"},
	}
	for _, tr := range tracks {
		if _, err := db.Exec(`INSERT INTO Track (id, title, artist, filename, path, fileType, bpmAnalyzed, length)
			VALUES (?, ?, ?, ?, ?, 'mp3', 124.0, 200)`, tr.id, tr.title, tr.art, tr.title+".mp3", tr.title+".mp3"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO PerformanceData (trackId, trackData, quickCues, loops, activeOnLoadLoops)
			VALUES (?, x'', x'', x'', 0)`, tr.id); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// TestDriveTrackSelection clicks rows in the track browser via shirei's drive
// harness and verifies the selection updates and the tools follow along.
func TestDriveTrackSelection(t *testing.T) {
	InitFontSubsystem()
	ResetInputSession()
	GetHost().WindowSize = Vec2{1180, 740}

	dbPath := buildTestLibrary(t)
	a := NewApp(dbPath)
	if len(a.Tracks) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(a.Tracks))
	}

	port, err := drive.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	AcceptInputCommands(port)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				RunFrameFn(a.RootView)
				time.Sleep(8 * time.Millisecond)
			}
		}
	}()
	defer func() {
		close(stop)
		wg.Wait()
		a.lib.Close()
	}()

	time.Sleep(60 * time.Millisecond) // let a few frames render

	// Click the second track: selection must follow.
	if _, err := drive.ClickOne(port, "track-7"); err != nil {
		t.Fatalf("click track-7: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if a.Selected != 7 {
		t.Fatalf("after clicking track-7: a.Selected = %d, want 7", a.Selected)
	}
	cues := a.Tools[0].(*CuesTool)
	if cues.lastSel != 7 || cues.rec.ID != 7 {
		t.Errorf("cues tool did not follow selection: lastSel=%d rec=%d", cues.lastSel, cues.rec.ID)
	}

	// Click the first track.
	if _, err := drive.ClickOne(port, "track-5"); err != nil {
		t.Fatalf("click track-5: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if a.Selected != 5 {
		t.Fatalf("after clicking track-5: a.Selected = %d, want 5", a.Selected)
	}
	if cues.lastSel != 5 || cues.rec.ID != 5 {
		t.Errorf("cues tool did not follow selection: lastSel=%d rec=%d", cues.lastSel, cues.rec.ID)
	}
}

// TestDriveTagsToolLoad exercises the tag editor end to end: clicking a track
// loads its ID3v2 tags from the file into the form.
func TestDriveTagsToolLoad(t *testing.T) {
	InitFontSubsystem()
	ResetInputSession()
	GetHost().WindowSize = Vec2{1180, 740}

	dbPath := buildTestLibrary(t)

	// Create a real MP3 with tags next to the DB and point the track at it.
	dir := filepath.Dir(dbPath)
	mp3 := filepath.Join(dir, "Emotion.mp3")
	tagsIn := MediaTags{Title: "Emotion", Artist: "Purple Disco Machine", Genre: "House", BPM: "124"}
	if err := os.WriteFile(mp3, bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x00}, 256), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveMediaTags(mp3, tagsIn); err != nil {
		t.Fatal(err)
	}
	{
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`UPDATE Track SET path = ? WHERE id = 5`, mp3); err != nil {
			t.Fatal(err)
		}
	}

	a := NewApp(dbPath)
	tags := a.Tools[1].(*TagsTool)

	port, err := drive.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	AcceptInputCommands(port)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				RunFrameFn(a.RootView)
				time.Sleep(8 * time.Millisecond)
			}
		}
	}()
	defer func() {
		close(stop)
		wg.Wait()
		a.lib.Close()
	}()

	time.Sleep(60 * time.Millisecond)

	// Switch to the MP3 Tags tool, then select track 5.
	a.ActiveTool = 1
	time.Sleep(30 * time.Millisecond)
	if _, err := drive.ClickOne(port, "track-5"); err != nil {
		t.Fatalf("click track-5: %v", err)
	}
	time.Sleep(60 * time.Millisecond)

	if a.Selected != 5 {
		t.Fatalf("a.Selected = %d, want 5", a.Selected)
	}
	if tags.lastSel != 5 {
		t.Fatalf("tags tool lastSel = %d, want 5", tags.lastSel)
	}
	if tags.tags.Title != "Emotion" || tags.tags.Artist != "Purple Disco Machine" || tags.tags.BPM != "124" {
		t.Errorf("form tags = %+v, want loaded ID3 tags", tags.tags)
	}
	if tags.path != mp3 {
		t.Errorf("resolved path = %q, want %q", tags.path, mp3)
	}

	// Sanity: the file's tag is untouched and readable by the library itself.
	tagsFile, err := id3v2.Open(mp3, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tagsFile.Close()
	if tagsFile.Title() != "Emotion" {
		t.Errorf("file title = %q", tagsFile.Title())
	}
}
