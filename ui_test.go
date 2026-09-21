package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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
		filename TEXT, path TEXT, fileType TEXT, bpmAnalyzed REAL, length INTEGER,
		bpm INTEGER, year INTEGER, playOrder INTEGER, genre TEXT, comment TEXT, composer TEXT,
		rating INTEGER, key INTEGER
	);
	CREATE TABLE PerformanceData (
		trackId INTEGER PRIMARY KEY, trackData BLOB, quickCues BLOB, loops BLOB, activeOnLoadLoops INTEGER
	);`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	tracks := []struct {
		id     int64
		title  string
		art    string
		rating int64
		key    int64
	}{
		{5, "Emotion", "Purple Disco Machine", 20, 10}, // 10 = 1B (B major)
		{7, "Galaxy", "Alex Metric", 0, -1},            // no key
	}
	for _, tr := range tracks {
		if _, err := db.Exec(`INSERT INTO Track (id, title, artist, filename, path, fileType, bpmAnalyzed, length, bpm, year, playOrder, rating, key)
			VALUES (?, ?, ?, ?, ?, 'mp3', 124.0, 200, 124, 2024, 3, ?, ?)`, tr.id, tr.title, tr.art, tr.title+".mp3", tr.title+".mp3", tr.rating, tr.key); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO PerformanceData (trackId, trackData, quickCues, loops, activeOnLoadLoops)
			VALUES (?, x'', x'', x'', 0)`, tr.id); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// TestDriveFilterReturnKey focuses the filter box, types a query and presses
// Return: the search must run without touching the Search button.
func TestDriveFilterReturnKey(t *testing.T) {
	InitFontSubsystem()
	ResetInputSession()
	GetHost().WindowSize = Vec2{1180, 740}

	a := NewApp(buildTestLibrary(t))
	if a.TrackCount != 2 {
		t.Fatalf("expected 2 unfiltered tracks, got %d", a.TrackCount)
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

	time.Sleep(60 * time.Millisecond)

	// Focus the filter box and type the query.
	if _, err := drive.ClickOne(port, "filter-input"); err != nil {
		t.Fatalf("click filter-input: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if err := drive.Text(port, "Emotion"); err != nil {
		t.Fatalf("type query: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if a.Filter != "" {
		t.Fatalf("typing must not auto-search: a.Filter = %q", a.Filter)
	}

	// Return runs the search.
	if err := drive.Key(port, "return"); err != nil {
		t.Fatalf("key return: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	if a.Filter != "Emotion" {
		t.Fatalf("after Return: a.Filter = %q, want \"Emotion\"", a.Filter)
	}
	if a.TrackCount != 1 {
		t.Errorf("after Return: a.TrackCount = %d, want 1", a.TrackCount)
	}
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

	// Arrow keys navigate the list in display (artist) order:
	// Alex Metric (7) sorts before Purple Disco Machine (5), so from track-5
	// "up" selects track-7, "down" goes back, and a second "up" clamps at the
	// first row.
	if err := drive.Key(port, "up"); err != nil {
		t.Fatalf("key up: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if a.Selected != 7 {
		t.Fatalf("after arrow-up: a.Selected = %d, want 7", a.Selected)
	}
	if err := drive.Key(port, "down"); err != nil {
		t.Fatalf("key down: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if a.Selected != 5 {
		t.Fatalf("after arrow-down: a.Selected = %d, want 5", a.Selected)
	}
	if a.listScroll < 0 {
		t.Errorf("listScroll = %v, want >= 0 after navigation", a.listScroll)
	}
	if err := drive.Key(port, "up"); err != nil {
		t.Fatalf("key up: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if a.Selected != 7 {
		t.Fatalf("after arrow-up: a.Selected = %d, want 7", a.Selected)
	}
	if err := drive.Key(port, "up"); err != nil {
		t.Fatalf("key up at first row: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if a.Selected != 7 {
		t.Fatalf("arrow-up past the first row moved selection: a.Selected = %d, want 7", a.Selected)
	}
}

// TestDriveQuitShortcut verifies the Cmd-Q (Ctrl-Q on non-mac) shortcut
// triggers the app quit path, and that a plain "q" keypress does not.
func TestDriveQuitShortcut(t *testing.T) {
	InitFontSubsystem()
	ResetInputSession()
	GetHost().WindowSize = Vec2{1180, 740}

	dbPath := buildTestLibrary(t)
	a := NewApp(dbPath)

	var quitCalled atomic.Bool
	a.onQuit = func() { quitCalled.Store(true) }

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

	// Plain "q" (e.g. typed into a field) must not quit.
	if err := drive.Key(port, "q"); err != nil {
		t.Fatalf("plain q: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if quitCalled.Load() {
		t.Fatal("plain q triggered quit")
	}

	// Cmd-Q quits.
	if err := drive.Key(port, "q", "cmd"); err != nil {
		t.Fatalf("cmd-q: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if !quitCalled.Load() {
		t.Fatal("Cmd-Q did not trigger quit")
	}
}

// TestGlobalEditCommentTags runs the Global Edit tool over the test library:
// track 5 carries the real 5-cue/3-loop fixture blobs, so it must gain
// "#cued" and "#looped" (sorted into the comment), while track 7 (no cues,
// no loops) stays untouched.
func TestGlobalEditCommentTags(t *testing.T) {
	dbPath := buildTestLibrary(t)
	dir := filepath.Dir(dbPath)
	mp3 := filepath.Join(dir, "Emotion.mp3")
	mp3b := filepath.Join(dir, "Galaxy.mp3")
	if err := os.WriteFile(mp3, bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x00}, 256), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mp3b, bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x00}, 256), 0o644); err != nil {
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
		if _, err := db.Exec(`UPDATE Track SET path = ? WHERE id = 7`, mp3b); err != nil {
			t.Fatal(err)
		}
		// Real performance blobs: 5 cues + 3 loops on track 5.
		if _, err := db.Exec(`UPDATE PerformanceData SET quickCues = ?, loops = ? WHERE trackId = 5`,
			mustHex(t, fixtTrack5QuickCues), mustHex(t, fixtTrack5Loops)); err != nil {
			t.Fatal(err)
		}
	}

	a := NewApp(dbPath)
	// Seed the file comment (unsorted, with a non-tag word).
	if err := SaveMediaTags(mp3, MediaTags{Title: "Emotion", Comment: "#ztag some note #atag"}); err != nil {
		t.Fatal(err)
	}

	g := a.Tools[2].(*GlobalTool)
	g.sortTags, g.addTags, g.alsoDB = true, true, true
	g.dryRun = false
	changed, unchanged, failed, skipped := g.Apply(a)

	if changed != 1 || unchanged != 1 || failed != 0 || skipped != 0 {
		t.Fatalf("apply: changed=%d unchanged=%d failed=%d skipped=%d", changed, unchanged, failed, skipped)
	}

	// File comment: tags sorted, #cued + #looped added, non-tag words kept.
	tags, err := ReadMediaTags(mp3)
	if err != nil {
		t.Fatal(err)
	}
	want := "#atag #cued #looped #ztag some note"
	if tags.Comment != want {
		t.Errorf("file comment = %q, want %q", tags.Comment, want)
	}

	// DB comment synced as well.
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var dbComment string
	if err := db.QueryRow(`SELECT comment FROM Track WHERE id = 5`).Scan(&dbComment); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if dbComment != want {
		t.Errorf("DB comment = %q, want %q", dbComment, want)
	}

	// A second run is a no-op (already sorted, tags present).
	changed, _, _, _ = g.Apply(a)
	if changed != 0 {
		t.Errorf("second run changed %d track(s), want 0", changed)
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

	// Click the 4th rating star: the rating is written to the DB immediately
	// and the in-memory track list follows.
	if _, err := drive.ClickOne(port, "rating-star-4"); err != nil {
		t.Fatalf("click rating-star-4: %v", err)
	}
	time.Sleep(80 * time.Millisecond)

	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var dbRating int64
	if err := db.QueryRow(`SELECT rating FROM Track WHERE id = 5`).Scan(&dbRating); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if dbRating != 80 {
		t.Errorf("DB rating after clicking star-4 = %d, want 80", dbRating)
	}
	if a.Tracks[1].Rating != 80 { // ordered by artist: Alex Metric(7), Purple Disco Machine(5)
		t.Errorf("in-memory rating = %d, want 80", a.Tracks[1].Rating)
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
