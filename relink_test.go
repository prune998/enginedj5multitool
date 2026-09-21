package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// TestRelinkFindsMovedFile moves a track's audio file into a folder tree and
// verifies the Relink tool finds it (by name + artist folder + file size) and
// updates Track.path in the database.
func TestRelinkFindsMovedFile(t *testing.T) {
	dbPath := buildTestLibrary(t)

	// Track 5's original location stays empty (the file "moved"); track 7's
	// audio file is gone entirely (no match anywhere).
	content := bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x00}, 256) // 1024 bytes
	newDir := filepath.Join(t.TempDir(), "moved", "Purple Disco Machine")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(newDir, "Emotion.mp3")
	if err := os.WriteFile(newPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	a := NewApp(dbPath)
	tool := a.Tools[3].(*RelinkTool)

	// Stage 1: the scan proposes a new path for track 5 and marks track 7 as
	// no-match.
	rep, missing := tool.scanSync(a)
	// Point the search at the folder containing the moved file and re-scan.
	tool.root = filepath.Dir(newDir)
	rep, missing = tool.scanSync(a)
	tool.missingList = missing // the real flow retains this in startScan
	if rep.found != 0 || rep.missing != 2 || len(missing) != 2 {
		t.Fatalf("scan: found=%d missing=%d list=%d, want 0/2/2", rep.found, rep.missing, len(missing))
	}
	byID := map[int64]*missingTrack{}
	for i := range missing {
		byID[missing[i].rec.ID] = &missing[i]
	}
	p5 := byID[5]
	if p5.propose == nil || p5.propose.path != newPath {
		t.Fatalf("proposal for track 5 = %+v, want %q", p5.propose, newPath)
	}
	if !p5.selected {
		t.Error("proposal should be selected by default")
	}
	if !byID[7].noMatch || byID[7].rec.ID != 7 {
		t.Fatalf("track 7 should be no-match: %+v", byID[7])
	}

	// Unchecked proposals are skipped.
	p5.selected = false
	rep2 := tool.relinkSync(a, missing)
	if rep2.relinked != 0 || rep2.skipped != 1 || rep2.failed != 0 {
		t.Fatalf("relink with unchecked proposal: relinked=%d skipped=%d failed=%d, want 0/1/0",
			rep2.relinked, rep2.skipped, rep2.failed)
	}

	// Checked proposal → relinked, DB points at the new file.
	p5.selected = true
	rep3 := tool.relinkSync(a, missing)
	if rep3.relinked != 1 || rep3.failed != 0 {
		t.Fatalf("relink run: %+v", rep3)
	}

	// Delete flow: check the no-match track for deletion and remove it from
	// the Engine DJ database.
	byID[7].proposeDelete = true
	tool.deleteSelected(a)

	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM Track`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("tracks in DB after delete = %d, want 1 (track 7 removed)", n)
	}
	if len(a.Tracks) != 1 || a.Tracks[0].ID != 5 {
		t.Errorf("session track list after delete: %d entries, want only track 5", len(a.Tracks))
	}
	if len(tool.missingList) != 1 || tool.missingList[0].rec.ID != 5 {
		t.Errorf("missing list after delete: %d entries, want only track 5", len(tool.missingList))
	}

	// The DB must now point at the new file.
	lib2, err := OpenLibrary(dbPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lib2.Close()
	tracks, err := lib2.Tracks("")
	if err != nil {
		t.Fatal(err)
	}
	var dbPath5 string
	for _, r := range tracks {
		if r.ID == 5 {
			dbPath5 = r.Path
		}
	}
	if dbPath5 != newPath {
		t.Fatalf("Track.path = %q, want %q", dbPath5, newPath)
	}
	if _, err := os.Stat(dbPath5); err != nil {
		t.Fatal(err)
	}
}
