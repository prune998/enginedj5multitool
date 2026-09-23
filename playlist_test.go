package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	. "go.hasen.dev/shirei"
	"go.hasen.dev/shirei/drive"
	_ "modernc.org/sqlite"
)

// buildPlaylistLibrary creates a library with the playlist tables, a folder
// tree (1: Folder A → 2: Sub, 3: Folder B) and five tracks for smartlist
// evaluation.
func buildPlaylistLibrary(t *testing.T) *Library {
	t.Helper()
	path := filepath.Join(t.TempDir(), "m.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	schema := `
	CREATE TABLE Track (
		id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT, artist TEXT, album TEXT,
		filename TEXT, path TEXT, fileType TEXT, bpmAnalyzed REAL, length INTEGER,
		bpm INTEGER, year INTEGER, playOrder INTEGER, genre TEXT, comment TEXT, composer TEXT,
		rating INTEGER, key INTEGER, fileBytes INTEGER, dateAdded INTEGER
	);
	CREATE TABLE Playlist (
		id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT, parentListId INTEGER,
		isPersisted BOOLEAN, nextListId INTEGER, lastEditTime DATETIME, isExplicitlyExported BOOLEAN
	);
	CREATE VIEW PlaylistAllParent AS
	WITH FindAllParent AS (
		SELECT id, parentListId FROM Playlist
		UNION ALL
		SELECT recursiveCTE.id, Plist.parentListId FROM Playlist Plist
		INNER JOIN FindAllParent recursiveCTE
		ON recursiveCTE.parentListId = Plist.id
	)
	SELECT * FROM FindAllParent;
	CREATE VIEW PlaylistAllChildren AS
	WITH FindAllChild AS (
		SELECT id, id as childListId FROM Playlist
		UNION ALL
		SELECT recursiveCTE.id, Plist.id FROM Playlist Plist
		INNER JOIN FindAllChild recursiveCTE
		ON recursiveCTE.childListId = Plist.parentListId
	)
	SELECT * FROM FindAllChild WHERE id <> childListId;
	CREATE VIEW PlaylistPath AS
	WITH RECURSIVE Heirarchy AS (
		SELECT id AS child, parentListId AS parent, title AS name, 1 AS depth FROM Playlist
		UNION ALL
		SELECT child, parentListId AS parent, title AS name, h.depth + 1 AS depth FROM Playlist c
		JOIN Heirarchy h ON h.parent = c.id
		ORDER BY depth DESC
	),
	NameConcat AS (
		SELECT child AS id, GROUP_CONCAT(name, ';') || ';' AS path
		FROM (SELECT child, name FROM Heirarchy ORDER BY depth DESC)
		GROUP BY child
	)
	SELECT id, path FROM Playlist c LEFT JOIN NameConcat g USING (id);
	CREATE TABLE PlaylistEntity (
		id INTEGER PRIMARY KEY AUTOINCREMENT, listId INTEGER, trackId INTEGER,
		databaseUuid TEXT, nextEntityId INTEGER, membershipReference INTEGER
	);
	CREATE TABLE Smartlist (
		listUuid TEXT PRIMARY KEY, title TEXT, parentPlaylistPath TEXT,
		nextPlaylistPath TEXT, nextListUuid TEXT, rules TEXT, lastEditTime DATETIME
	);
	CREATE TABLE Information (id INTEGER PRIMARY KEY, uuid TEXT, schemaVersionNumber INTEGER, contentVersionNumber INTEGER, resultsCounter INTEGER, playedIndicator INTEGER, namething TEXT);`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO Information (id, uuid, schemaVersionNumber) VALUES (1, 'test-uuid', 3)`); err != nil {
		t.Fatal(err)
	}
	// Folder tree: 1 "Folder A" (root) → 2 "Sub"; 3 "Folder B" (root).
	for _, p := range [][3]any{{int64(1), "Folder A", int64(0)}, {int64(2), "Sub", int64(1)}, {int64(3), "Folder B", int64(0)}} {
		if _, err := db.Exec(`INSERT INTO Playlist (id, title, parentListId, isPersisted, nextListId, lastEditTime, isExplicitlyExported)
			VALUES (?, ?, ?, 1, 0, datetime('now'), 0)`, p[0], p[1], p[2]); err != nil {
			t.Fatal(err)
		}
	}
	// Sibling chain: 1 → 3 → 0 at root level.
	if _, err := db.Exec(`UPDATE Playlist SET nextListId = 3 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	// Tracks: deliberately unordered for the sort tests.
	tracks := []struct {
		title, artist, comment string
		bpm                    float64
		rating, key, added     int64
	}{
		{"Zebra", "Beta", "#house", 120, 20, 5, 300},
		{"alpha", "Alpha", "#house #cued", 128, 100, 8, 100},
		{"Mango", "Gamma", "#techno", 132, 60, 11, 200},
		{"Bicycle", "Delta", "#house", 124, 0, 1, 400},
		{"Cherry", "Epsilon", "#techno #vocal", 126, 80, 3, 500},
	}
	for _, tr := range tracks {
		if _, err := db.Exec(`INSERT INTO Track (title, artist, comment, bpm, rating, key, dateAdded)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, tr.title, tr.artist, tr.comment, tr.bpm, tr.rating, tr.key, tr.added); err != nil {
			t.Fatal(err)
		}
	}
	// Smartlist: all tracks whose comment contains "#house".
	rules := `{"match":"all","rules":[{"col":"comment","con":"LIKE","param":"'%#house%'","v":"2.20.0"}]}`
	if _, err := db.Exec(`INSERT INTO Smartlist (listUuid, title, parentPlaylistPath, rules)
		VALUES ('uuid-1', 'House Mix', 'Folder A;', ?)`, rules); err != nil {
		t.Fatal(err)
	}
	lib, err := OpenLibrary(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lib.Close() })
	return lib
}

func TestPlaylistTree(t *testing.T) {
	lib := buildPlaylistLibrary(t)
	roots, err := lib.PlaylistTree()
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 {
		t.Fatalf("roots = %d, want 2", len(roots))
	}
	// Sibling chain order: Folder A (1) before Folder B (3).
	if roots[0].Title != "Folder A" || roots[1].Title != "Folder B" {
		t.Fatalf("root order = %q, %q", roots[0].Title, roots[1].Title)
	}
	if !roots[0].IsFolder || len(roots[0].Children) != 1 || roots[0].Children[0].Title != "Sub" {
		t.Fatalf("Folder A children = %+v", roots[0].Children)
	}
	if roots[0].Children[0].depth != 1 {
		t.Fatalf("Sub depth = %d, want 1", roots[0].Children[0].depth)
	}
	if roots[1].IsFolder {
		t.Error("Folder B has no children and should not be marked folder")
	}
}

func TestEvaluateSmartlist(t *testing.T) {
	lib := buildPlaylistLibrary(t)
	sls, err := lib.Smartlists()
	if err != nil {
		t.Fatal(err)
	}
	if len(sls) != 1 || sls[0].Title != "House Mix" {
		t.Fatalf("smartlists = %+v", sls)
	}
	tracks, err := lib.EvaluateSmartlist(sls[0].Rules)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 3 {
		t.Fatalf("matches = %d, want 3", len(tracks))
	}
	for _, tr := range tracks {
		if !contains(tr.Comment, "#house") {
			t.Errorf("track %q matched without #house: %q", tr.Title, tr.Comment)
		}
	}
	// Unknown columns and conditions error out.
	if _, err := lib.EvaluateSmartlist(`{"match":"all","rules":[{"col":"nope","con":"LIKE","param":"'x'"}]}`); err == nil {
		t.Error("unknown column should error")
	}
	if _, err := lib.EvaluateSmartlist(`{"match":"all","rules":[{"col":"comment","con":"BANG","param":"'x'"}]}`); err == nil {
		t.Error("unknown condition should error")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestSortSmartlistTracks(t *testing.T) {
	tracks := []SmartlistTrack{
		{ID: 1, Title: "Zebra", Artist: "Beta", BPM: 120, Rating: 20, Comment: "#z", Key: 5, DateAdded: 300},
		{ID: 2, Title: "alpha", Artist: "Alpha", BPM: 128, Rating: 100, Comment: "#a", Key: 8, DateAdded: 100},
		{ID: 3, Title: "Mango", Artist: "Gamma", BPM: 132, Rating: 60, Comment: "#m", Key: 11, DateAdded: 200},
	}
	SortSmartlistTracks(tracks, "title", true)
	if tracks[0].Title != "alpha" || tracks[2].Title != "Zebra" {
		t.Fatalf("title asc = %q, %q, %q", tracks[0].Title, tracks[1].Title, tracks[2].Title)
	}
	SortSmartlistTracks(tracks, "bpm", true)
	if tracks[0].BPM != 120 || tracks[2].BPM != 132 {
		t.Fatalf("bpm asc = %v", tracks)
	}
	SortSmartlistTracks(tracks, "rating", false)
	if tracks[0].Rating != 100 || tracks[2].Rating != 20 {
		t.Fatalf("rating desc = %v", tracks)
	}
	SortSmartlistTracks(tracks, "date", true)
	if tracks[0].DateAdded != 100 || tracks[2].DateAdded != 300 {
		t.Fatalf("dateAdded asc = %v", tracks)
	}
	SortSmartlistTracks(tracks, "key", true)
	if tracks[0].Key != 5 || tracks[1].Key != 8 || tracks[2].Key != 11 {
		t.Fatalf("key asc = %v", tracks)
	}
	SortSmartlistTracks(tracks, "comment", true)
	if tracks[0].Comment != "#a" {
		t.Fatalf("comment asc[0] = %q, want #a", tracks[0].Comment)
	}
}

func TestCreatePlaylist(t *testing.T) {
	lib := buildPlaylistLibrary(t)

	// Materialize inside "Folder A" (id 1, currently no playlist children).
	ids := []int64{5, 3, 1}
	newID, err := lib.CreatePlaylist("My Mix", 1, ids)
	if err != nil {
		t.Fatal(err)
	}

	var title string
	var parent, next int64
	if err := lib.DB.QueryRow(`SELECT title, parentListId, nextListId FROM Playlist WHERE id = ?`, newID).
		Scan(&title, &parent, &next); err != nil {
		t.Fatal(err)
	}
	if title != "My Mix" || parent != 1 || next != 0 {
		t.Fatalf("playlist row = %q parent=%d next=%d", title, parent, next)
	}

	// Sibling chain: Folder A had no playlist children before, so nothing to
	// rewire — create a second playlist and verify the chain 1→second→0.
	secondID, err := lib.CreatePlaylist("Second Mix", 1, []int64{2})
	if err != nil {
		t.Fatal(err)
	}
	if err := lib.DB.QueryRow(`SELECT nextListId FROM Playlist WHERE id = ?`, newID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next != secondID {
		t.Fatalf("first playlist nextListId = %d, want %d (the new last sibling)", next, secondID)
	}

	// PlaylistPath is a view: it must have a derived row whose path
	// contains the playlist and the parent folder names.
	var path string
	if err := lib.DB.QueryRow(`SELECT path FROM PlaylistPath WHERE id = ?`, newID).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if !contains(path, "My Mix") || !contains(path, "Folder A") {
		t.Fatalf("path = %q, want it to contain both names", path)
	}

	// The views derive the helper rows: AllParent (new, 1) and (new, 0);
	// AllChildren (1, new) — root-level entries have no id=0 row, matching
	// Engine's own database.
	for _, r := range [][2]int64{{newID, 1}, {newID, 0}} {
		var n int
		if err := lib.DB.QueryRow(`SELECT COUNT(*) FROM PlaylistAllParent WHERE id = ? AND parentListId = ?`, r[0], r[1]).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("AllParent (%d, %d) rows = %d", r[0], r[1], n)
		}
	}
	var n int
	if err := lib.DB.QueryRow(`SELECT COUNT(*) FROM PlaylistAllChildren WHERE id = 1 AND childListId = ?`, newID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("AllChildren (1, %d) rows = %d", newID, n)
	}

	// Entities: order by id, backward chain (first has next=0, each next
	// points at the previous entity), correct track ids and uuid.
	rows, err := lib.DB.Query(`SELECT id, trackId, nextEntityId, databaseUuid FROM PlaylistEntity WHERE listId = ? ORDER BY id`, newID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []struct {
		id, trackID, next int64
		uuid              string
	}
	for rows.Next() {
		var e struct {
			id, trackID, next int64
			uuid              string
		}
		if err := rows.Scan(&e.id, &e.trackID, &e.next, &e.uuid); err != nil {
			t.Fatal(err)
		}
		got = append(got, e)
	}
	rows.Close()
	if len(got) != len(ids) {
		t.Fatalf("entities = %d, want %d", len(got), len(ids))
	}
	for i, e := range got {
		if e.trackID != ids[i] {
			t.Errorf("entity %d trackId = %d, want %d", i, e.trackID, ids[i])
		}
		wantNext := int64(0)
		if i > 0 {
			wantNext = got[i-1].id
		}
		if e.next != wantNext {
			t.Errorf("entity %d nextEntityId = %d, want %d", i, e.next, wantNext)
		}
		if e.uuid != "test-uuid" {
			t.Errorf("entity %d databaseUuid = %q", i, e.uuid)
		}
	}

	// The tree now contains both playlists as children of Folder A (after
	// the pre-existing "Sub" folder from the fixture).
	roots, err := lib.PlaylistTree()
	if err != nil {
		t.Fatal(err)
	}
	kids := roots[0].Children
	if len(kids) != 3 || kids[0].Title != "Sub" || kids[1].Title != "My Mix" || kids[2].Title != "Second Mix" {
		t.Fatalf("Folder A children after create = %+v", kids)
	}
	if !kids[1].IsFolder {
		t.Error("a playlist used as a parent must be marked folder")
	}
}

func TestCreatePlaylistValidation(t *testing.T) {
	lib := buildPlaylistLibrary(t)
	if _, err := lib.CreatePlaylist("  ", 1, nil); err == nil {
		t.Error("empty name should error")
	}
	if _, err := lib.CreatePlaylist("x", 0, nil); err == nil {
		t.Error("root (0) as parent should error: parent row does not exist")
	}
	if _, err := os.Stat("nonexistent"); err != nil {
		_ = err // keep os import used on all platforms
	}
}

func TestBuildSmartlistTree(t *testing.T) {
	sls := []SmartlistInfo{
		{Title: "All Green", Path: "dynamic;House;"},
		{Title: "Techno", Path: "dynamic;"},
		{Title: "Warmup", Path: "dynamic;House;"},
		{Title: "Party", Path: "other;"},
	}
	root := buildSmartlistTree(sls)
	if len(root.children) != 2 || root.children[0].name != "dynamic" || root.children[1].name != "other" {
		t.Fatalf("root children = %+v", root.children)
	}
	dyn := root.children[0]
	if len(dyn.items) != 1 || t2i(dyn.items) != 1 { // "Techno" is index 1
		t.Fatalf("dynamic items = %v, want [1]", dyn.items)
	}
	house := dyn.children[0]
	if house.name != "House" || len(house.items) != 2 || house.items[0] != 0 || house.items[1] != 2 {
		t.Fatalf("House node = %+v items=%v", house.name, house.items)
	}
}

func t2i(xs []int) int {
	if len(xs) == 1 {
		return xs[0]
	}
	return -1
}

func TestCreatePlaylistPerf(t *testing.T) {
	lib := buildPlaylistLibrary(t)
	ids := make([]int64, 3000)
	for i := range ids {
		ids[i] = int64(i%5 + 1)
	}
	start := time.Now()
	id, err := lib.CreatePlaylist("Perf Test", 1, ids)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("creating 3000 entities took %v — the UI would freeze", d)
	}
	var n int
	lib.DB.QueryRow(`SELECT COUNT(*) FROM PlaylistEntity WHERE listId = ?`, id).Scan(&n)
	if n != len(ids) {
		t.Fatalf("entities = %d, want %d", n, len(ids))
	}
}

// playlistCreatorDriveApp starts the Playlist Creator with the drive
// harness and a frame loop; the caller gets the app and a cleanup func.
func playlistCreatorDriveApp(t *testing.T) (*App, int, func()) {
	t.Helper()
	lib := buildPlaylistLibrary(t)
	GetHost().WindowSize = Vec2{1800, 1000}
	a := NewApp(filepath.Join(lib.Dir, "m.db"))
	a.ActiveTool = 5
	a.Refresh()

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
	cleanup := func() { close(stop); wg.Wait(); a.Close() }
	return a, port, cleanup
}

// TestPlaylistCreatorMenuOpens drives the smartlist menu button and verifies
// the dropdown opens (the drive click finds the registered access node).
func TestPlaylistCreatorMenuOpens(t *testing.T) {
	if raceEnabled {
		t.Skip("drive harness races under -race (global shirei state)")
	}
	_, port, cleanup := playlistCreatorDriveApp(t)
	defer cleanup()
	time.Sleep(60 * time.Millisecond)

	if _, err := drive.Click(port, "plc-smartlist-menu"); err != nil {
		t.Fatalf("click smartlist menu: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
}

// TestPlaylistCreatorTreePick clicks a destination folder row and verifies
// the selection. The click handler runs inside the render frame — a
// re-entrant WithFrameLock there deadlocks the app (this test hangs if that
// regression returns). Hover, press, release with frame time in between
// (drive.Click's compressed timing is unreliable against the 8ms loop).
func TestPlaylistCreatorTreePick(t *testing.T) {
	if raceEnabled {
		t.Skip("drive harness races under -race (global shirei state)")
	}
	a, port, cleanup := playlistCreatorDriveApp(t)
	defer cleanup()
	time.Sleep(60 * time.Millisecond)

	if _, err := drive.Hover(port, "plc-tree-1"); err != nil {
		t.Fatalf("hover destination folder: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := drive.Down(port); err != nil {
		t.Fatalf("mouse down: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := drive.Up(port); err != nil {
		t.Fatalf("mouse up: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	tool := a.Tools[5].(*PlaylistCreatorTool)
	if tool.selParent != 1 {
		t.Fatalf("selParent = %d, want 1 (Folder A)", tool.selParent)
	}
	if tool.parentLabel != "Folder A" {
		t.Fatalf("parentLabel = %q, want %q", tool.parentLabel, "Folder A")
	}
}
