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
		{"/Volumes/Macintosh HD/Users/you/Music/x.mp3", "/Users/you/Music/x.mp3"},
		{"/Users/you/Music/x.mp3", "/Users/you/Music/x.mp3"},
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

// TestTagDupKey pins the same-song key normalization: case and whitespace
// are folded, and entries without a real artist or title never match.
func TestTagDupKey(t *testing.T) {
	cases := []struct {
		a, b, want string
	}{
		{"Purple Disco Machine", "Emotion", "purple disco machine emotion"},
		{"purple  disco MACHINE ", " emotion", "purple disco machine emotion"}, // same key
		{"", "Emotion", ""},  // no artist: never matches
		{"Artist", "  ", ""}, // no title: never matches
		{" ", "Emotion", ""}, // whitespace-only artist: never matches
	}
	for _, tc := range cases {
		key := tagDupKey(tc.a, tc.b)
		if tc.want == "" {
			if key != "" {
				t.Errorf("tagDupKey(%q, %q) = %q, want empty", tc.a, tc.b, key)
			}
			continue
		}
		if key != tc.want {
			t.Errorf("tagDupKey(%q, %q) = %q, want %q", tc.a, tc.b, key, tc.want)
		}
	}
}

// TestDedupGroupsSameTagsDifferentFiles: two entries with the same
// artist & title tags but different audio files group as a same-tags
// duplicate (and NOT as a file duplicate).
func TestDedupGroupsSameTagsDifferentFiles(t *testing.T) {
	dbPath := buildTestLibrary(t)
	dir := filepath.Dir(dbPath)

	// Two real files, same tags, different paths. Track 5 keeps its
	// Emotion/Purple Disco Machine tags; track 7 is rewritten to match.
	first := filepath.Join(dir, "Emotion.mp3")
	second := filepath.Join(dir, "Emotion (copy).mp3")
	for _, p := range []string{first, second} {
		if err := os.WriteFile(p, bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x00}, 256), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	{
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`UPDATE Track SET path = ? WHERE id = 5`, first); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE Track SET artist = 'Purple Disco Machine', title = 'Emotion', path = ? WHERE id = 7`, second); err != nil {
			t.Fatal(err)
		}
	}

	a := NewApp(dbPath)
	defer a.Close() // release the DB handle (Windows file locks)
	tool := a.Tools[4].(*DedupTool)
	groups, _ := tool.scanSync(a)

	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1 same-tags group", len(groups))
	}
	g := groups[0]
	if !g.byTags {
		t.Fatalf("group kind = file duplicate, want same-tags")
	}
	if g.path != "" || g.missing {
		t.Errorf("same-tags group must not carry a shared path/missing flag (path=%q missing=%v)", g.path, g.missing)
	}
	if len(g.entries) != 2 {
		t.Fatalf("group entries = %d, want 2", len(g.entries))
	}
	// Entries display their own file paths, sorted by path; the first is
	// the keeper. Both files of the library must be covered.
	paths := map[string]bool{g.entries[0].path: true, g.entries[1].path: true}
	if !paths[first] || !paths[second] || len(paths) != 2 {
		t.Errorf("entry paths = %q, %q — want %q and %q", g.entries[0].path, g.entries[1].path, first, second)
	}
	if !(g.entries[0].path < g.entries[1].path) {
		t.Errorf("entries not sorted by path: %q, %q", g.entries[0].path, g.entries[1].path)
	}
	if !g.entries[0].keep || g.entries[1].keep {
		t.Error("first entry must be the keeper, extras not keepers")
	}
	for _, e := range g.entries {
		if e.missing {
			t.Errorf("entry %d flagged missing, but its file exists", e.rec.ID)
		}
	}

	// Deleting the checked extra removes it from the DB and the group.
	tool.groups = groups
	tool.scanDone = true
	tool.groups[0].entries[1].selected = true
	tool.deleteSelected(a)
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tracks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM Track`).Scan(&tracks); err != nil {
		t.Fatal(err)
	}
	if tracks != 1 {
		t.Errorf("tracks after delete = %d, want 1", tracks)
	}
	if len(tool.groups) != 0 {
		t.Errorf("groups after delete = %d, want 0", len(tool.groups))
	}
}

// TestDedupSameFileAndSameTags: two entries sharing one file AND the same
// tags are fully reported by the file-duplicate group — the same-tags view
// must not repeat them (it collapses to one representative per file).
func TestDedupSameFileAndSameTags(t *testing.T) {
	dbPath := buildTestLibrary(t)
	dir := filepath.Dir(dbPath)
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
		// Same file, same tags for both entries.
		if _, err := db.Exec(`UPDATE Track SET path = ?, artist = 'Same Band', title = 'Same Song' WHERE id IN (5, 7)`, shared); err != nil {
			t.Fatal(err)
		}
	}

	a := NewApp(dbPath)
	defer a.Close() // release the DB handle (Windows file locks)
	tool := a.Tools[4].(*DedupTool)
	groups, _ := tool.scanSync(a)

	if len(groups) != 1 {
		t.Fatalf("groups = %d, want only the file-duplicate group", len(groups))
	}
	if groups[0].byTags {
		t.Error("group kind = same-tags, want the file-duplicate group (the pair shares one file)")
	}
	if len(groups[0].entries) != 2 {
		t.Errorf("file group entries = %d, want 2", len(groups[0].entries))
	}
}

// TestDedupSameTagsAcrossFiles: the same song exists as two entries on one
// file plus one entry on a second file. The file group reports the doubled
// row; the same-tags group reports the two distinct files, represented by
// the entries the file group keeps. Checking the extra in the file group
// and the same-tags extras deletes each track exactly once.
func TestDedupSameTagsAcrossFiles(t *testing.T) {
	dbPath := buildTestLibrary(t)
	dir := filepath.Dir(dbPath)
	file1 := filepath.Join(dir, "Same Song.mp3")
	file2 := filepath.Join(dir, "Same Song (other).mp3")
	for _, p := range []string{file1, file2} {
		if err := os.WriteFile(p, bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x00}, 256), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	{
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		// Tracks 5 and 7: same song, same file. Track 9: same song, other file.
		if _, err := db.Exec(`UPDATE Track SET path = ?, artist = 'Same Band', title = 'Same Song' WHERE id IN (5, 7)`, file1); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO Track (id, title, artist, filename, path, fileType, bpmAnalyzed, length, bpm, year, playOrder, rating, key, comment, fileBytes)
			VALUES (9, 'Same Song', 'Same Band', 'c.mp3', ?, 'mp3', 124.0, 200, 124, 2024, 3, 0, -1, '', 1024)`, file2); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO PerformanceData (trackId, trackData, quickCues, loops, activeOnLoadLoops)
			VALUES (9, x'', x'', x'', 0)`); err != nil {
			t.Fatal(err)
		}
	}

	a := NewApp(dbPath)
	defer a.Close() // release the DB handle (Windows file locks)
	if a.TrackCount != 3 {
		t.Fatalf("session tracks = %d, want 3", a.TrackCount)
	}
	tool := a.Tools[4].(*DedupTool)
	groups, _ := tool.scanSync(a)

	var fileG, tagsG *dupGroup
	for i := range groups {
		if groups[i].byTags {
			tagsG = &groups[i]
		} else {
			fileG = &groups[i]
		}
	}
	if fileG == nil || tagsG == nil {
		t.Fatalf("groups = %d, want one file group and one same-tags group", len(groups))
	}
	if len(fileG.entries) != 2 {
		t.Fatalf("file group entries = %d, want 2 (tracks 5 and 7 on the same file)", len(fileG.entries))
	}
	if len(tagsG.entries) != 2 {
		t.Fatalf("same-tags group entries = %d, want 2 (one representative per file)", len(tagsG.entries))
	}
	// The same-tags representative of file1 is the same entry the file
	// group keeps, and it carries the "+1 on this file" count; the other
	// representative is file2's single entry.
	keeperID := fileG.entries[0].rec.ID
	var rep1, rep2 *dupEntry
	for i := range tagsG.entries {
		switch tagsG.entries[i].path {
		case fileG.path:
			rep1 = &tagsG.entries[i]
		case file2:
			rep2 = &tagsG.entries[i]
		}
	}
	if rep1 == nil || rep2 == nil {
		t.Fatalf("same-tags entries = %q, %q — want one per file", tagsG.entries[0].path, tagsG.entries[1].path)
	}
	if rep1.rec.ID != keeperID {
		t.Errorf("file1 representative = #%d, want the file group keeper #%d", rep1.rec.ID, keeperID)
	}
	if rep1.fileEntries != 1 || rep2.fileEntries != 0 {
		t.Errorf("fileEntries = %d/%d, want 1/0 (two rows share file1)",
			rep1.fileEntries, rep2.fileEntries)
	}

	// Check every non-keeper everywhere it appears (file extra + same-tags
	// extras — all different tracks here) and delete once each.
	for i := range groups {
		for j := range groups[i].entries {
			if !groups[i].entries[j].keep {
				groups[i].entries[j].selected = true
			}
		}
	}
	tool.groups = groups
	tool.scanDone = true
	tool.deleteSelected(a)

	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tracks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM Track`).Scan(&tracks); err != nil {
		t.Fatal(err)
	}
	if tracks != 1 {
		t.Errorf("tracks after delete = %d, want 1 (only the keeper remains)", tracks)
	}
	if len(tool.groups) != 0 {
		t.Errorf("groups after delete = %d, want 0", len(tool.groups))
	}
	if len(a.Tracks) != 1 || a.TrackCount != 1 {
		t.Errorf("session tracks after delete = %d (count %d), want 1", len(a.Tracks), a.TrackCount)
	}
}
