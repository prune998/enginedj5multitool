package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCanonicalPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/Volumes/Macintosh HD/Users/prune/Music/x.mp3", "/Users/prune/Music/x.mp3"},
		{"/Users/prune/Music/x.mp3", "/Users/prune/Music/x.mp3"},
		{"/Volumes/OtherDisk/Music/x.mp3", "/Volumes/OtherDisk/Music/x.mp3"}, // non-default mounts kept
	}
	for _, tc := range cases {
		if got := canonicalPath(tc.in); got != tc.want {
			t.Errorf("canonicalPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestDedupGroupsVolumeAliasPath: one entry stores the file under the
// /Volumes/Macintosh HD alias, the other under the real /Users/... path —
// both must group as the same file.
func TestDedupGroupsVolumeAliasPath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the /Volumes/Macintosh HD mountpoint alias is macOS-specific")
	}
	dbPath := buildTestLibrary(t)
	real := filepath.Join(t.TempDir(), "Song.mp3")
	if err := os.WriteFile(real, bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x00}, 256), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := "/Volumes/Macintosh HD" + real
	{
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`UPDATE Track SET path = ? WHERE id = 5`, alias); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE Track SET path = ? WHERE id = 7`, real); err != nil {
			t.Fatal(err)
		}
	}

	a := NewApp(dbPath)
	defer a.Close() // release the DB handle (Windows file locks)
	tool := a.Tools[4].(*DedupTool)
	groups, _ := tool.scanSync(a)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1 (the two paths are the same file)", len(groups))
	}
	if len(groups[0].entries) != 2 {
		t.Fatalf("group entries = %d, want 2", len(groups[0].entries))
	}
	if groups[0].path != real {
		t.Errorf("group path = %q, want the canonical real path %q", groups[0].path, real)
	}
}

// TestDedupFindsSameFile points two library entries at the same audio file
// and verifies the Dedup tool groups them; deleting the checked extra entry
// removes it from the DB and session.
func TestDedupFindsSameFile(t *testing.T) {
	dbPath := buildTestLibrary(t)
	dir := filepath.Dir(dbPath)

	// Both entries point at the same file, which exists on disk.
	shared := filepath.Join(dir, "Emotion.mp3")
	if err := os.WriteFile(shared, bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x00}, 256), 0o644); err != nil {
		t.Fatal(err)
	}
	{
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`UPDATE Track SET path = ? WHERE id IN (5, 7)`, shared); err != nil {
			t.Fatal(err)
		}
	}

	a := NewApp(dbPath)
	defer a.Close() // release the DB handle (Windows file locks)
	tool := a.Tools[4].(*DedupTool)

	groups, total := tool.scanSync(a)
	if total != 2 {
		t.Fatalf("scanned %d tracks, want 2", total)
	}
	if len(groups) != 1 || len(groups[0].entries) != 2 {
		t.Fatalf("groups = %+v, want 1 group with 2 entries", groups)
	}
	if groups[0].path != shared {
		t.Errorf("group path = %q, want %q", groups[0].path, shared)
	}
	if groups[0].missing {
		t.Error("shared file exists — group must not be marked missing")
	}
	if !groups[0].entries[0].keep || groups[0].entries[1].keep {
		t.Error("first entry must be the keeper, extras not keepers")
	}

	// Check the extra entry for deletion and apply.
	tool.groups = groups
	tool.scanDone = true
	tool.groups[0].entries[1].selected = true
	if sel := tool.selectedCount(); sel != 1 {
		t.Fatalf("selectedCount = %d, want 1", sel)
	}
	tool.deleteSelected(a)

	// DB: only track 5 remains (track 7 + its performance data gone).
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tracks, perf int
	if err := db.QueryRow(`SELECT COUNT(*) FROM Track`).Scan(&tracks); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM PerformanceData`).Scan(&perf); err != nil {
		t.Fatal(err)
	}
	if tracks != 1 || perf != 1 {
		t.Errorf("after delete: tracks=%d perf=%d, want 1/1", tracks, perf)
	}

	// Session list and groups updated. The keeper is the first entry of the
	// group in display order (Alex Metric, track 7); track 5 was the extra.
	if len(a.Tracks) != 1 || a.Tracks[0].ID != 7 {
		t.Errorf("session tracks after delete = %d entries (first id %v), want only track 7",
			len(a.Tracks), firstID(a.Tracks))
	}
	if len(tool.groups) != 0 {
		t.Errorf("groups after delete = %d, want 0", len(tool.groups))
	}
	if a.TrackCount != 1 {
		t.Errorf("TrackCount = %d, want 1", a.TrackCount)
	}
}

// TestDedupMissingSharedFile groups two entries whose shared file is missing.
func TestDedupMissingSharedFile(t *testing.T) {
	dbPath := buildTestLibrary(t)
	{
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`UPDATE Track SET path = ? WHERE id IN (5, 7)`, "Emotion.mp3"); err != nil {
			t.Fatal(err)
		}
	}

	a := NewApp(dbPath)
	defer a.Close() // release the DB handle (Windows file locks)
	tool := a.Tools[4].(*DedupTool)
	groups, _ := tool.scanSync(a)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	if !groups[0].missing {
		t.Error("shared file does not exist — group must be marked missing")
	}
}
