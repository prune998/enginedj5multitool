package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// TrackRecord is a row of the Track table (without performance blobs).
type TrackRecord struct {
	ID        int64
	Title     string
	Artist    string
	Album     string
	Filename  string
	Path      string
	FileType  string
	BPM       float64 // bpmAnalyzed
	BPMFile   int64   // bpm from the file metadata
	Year      int64
	PlayOrder int64 // track number
	Rating    int64 // 0..100 in steps of 20 (5-star scale)
	Key       int64 // Engine DJ key index 0..23 (Camelot); -1 = unset
	Length    int64 // seconds
}

// Library wraps an Engine DJ m.db database.
type Library struct {
	DB  *sql.DB
	Dir string // directory containing the database

	stmtDetail *sql.Stmt
}

// OpenLibrary opens the database. Read-only when readOnly is true.
func OpenLibrary(path string, readOnly bool) (*Library, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	var dsn string
	if readOnly {
		dsn = "file:" + abs + "?mode=ro&immutable=1"
	} else {
		dsn = "file:" + abs + "?mode=rw"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	lib := &Library{DB: db, Dir: filepath.Dir(abs)}
	lib.stmtDetail, err = db.Prepare(`
		SELECT IFNULL(p.trackData, x''), IFNULL(p.quickCues, x''), IFNULL(p.loops, x''), IFNULL(p.activeOnLoadLoops,0)
		FROM PerformanceData p WHERE p.trackId = ?`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return lib, nil
}

func (l *Library) Close() error {
	if l.stmtDetail != nil {
		l.stmtDetail.Close()
	}
	return l.DB.Close()
}

// Tracks returns all tracks matching the filter (substring on title, artist,
// album, filename). Empty filter returns everything.
func (l *Library) Tracks(filter string) ([]TrackRecord, error) {
	query := `
		SELECT id, IFNULL(title,''), IFNULL(artist,''), IFNULL(album,''), IFNULL(filename,''),
		       IFNULL(path,''), IFNULL(fileType,''), IFNULL(bpmAnalyzed,0), IFNULL(bpm,0),
		       IFNULL(year,0), IFNULL(playOrder,0), IFNULL(rating,0), IFNULL(key,-1), IFNULL(length,0)
		FROM Track`
	var args []any
	if s := strings.TrimSpace(filter); s != "" {
		like := "%" + s + "%"
		query += ` WHERE title LIKE ? OR artist LIKE ? OR album LIKE ? OR filename LIKE ?`
		args = append(args, like, like, like, like)
	}
	query += ` ORDER BY artist, title`

	rows, err := l.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TrackRecord
	for rows.Next() {
		var r TrackRecord
		if err := rows.Scan(&r.ID, &r.Title, &r.Artist, &r.Album, &r.Filename,
			&r.Path, &r.FileType, &r.BPM, &r.BPMFile, &r.Year, &r.PlayOrder, &r.Rating, &r.Key, &r.Length); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetTrackRating writes a track's rating (0..100, steps of 20). The DB's own
// trigger bumps Track.lastEditTime so Engine DJ picks up the change.
func (l *Library) SetTrackRating(id int64, rating int64) error {
	if rating < 0 {
		rating = 0
	}
	if rating > 100 {
		rating = 100
	}
	_, err := l.DB.Exec(`UPDATE Track SET rating=? WHERE id=?`, rating, id)
	return err
}

// LoadDetail loads and parses the performance data (cues, loops, sample rate)
// of a track.
func (l *Library) LoadDetail(rec TrackRecord) (*TrackDetail, error) {
	var trackData, quickCues, loops []byte
	var active int64
	if err := l.stmtDetail.QueryRow(rec.ID).Scan(&trackData, &quickCues, &loops, &active); err != nil {
		return nil, err
	}
	return parseTrackDetail(trackData, quickCues, loops, active)
}

// FixResult is the computed fix for one track, ready to inspect (dry run) or
// write.
type FixResult struct {
	ID      int64
	Compute *FixComputeResult
	detail  *TrackDetail

	qc    []byte
	loops []byte
}

// Write applies the computed fix to the database.
func (r *FixResult) Write(lib *Library) error {
	if r.Compute == nil || (!r.Compute.QCChanged && !r.Compute.LPChanged) {
		return nil
	}
	if r.qc == nil && r.loops == nil {
		r.qc, r.loops = r.Compute.Serialize(r.detail)
	}
	switch {
	case r.Compute.QCChanged && r.Compute.LPChanged:
		_, err := lib.DB.Exec(`UPDATE PerformanceData SET quickCues=?, loops=?, activeOnLoadLoops=? WHERE trackId=?`,
			r.qc, r.loops, r.Compute.Active, r.ID)
		return err
	case r.Compute.QCChanged:
		_, err := lib.DB.Exec(`UPDATE PerformanceData SET quickCues=? WHERE trackId=?`, r.qc, r.ID)
		return err
	default:
		_, err := lib.DB.Exec(`UPDATE PerformanceData SET loops=?, activeOnLoadLoops=? WHERE trackId=?`, r.loops, r.Compute.Active, r.ID)
		return err
	}
}

// FixTrack computes the cue/loop fix for a track. Returns nil when the track
// is already clean. When dryRun is false the caller must invoke res.Write(lib)
// to persist the changes.
func (l *Library) FixTrack(rec TrackRecord, dryRun bool) (*FixResult, error) {
	d, err := l.LoadDetail(rec)
	if err != nil {
		return nil, err
	}
	compute := ComputeFix(d)
	if !compute.QCChanged && !compute.LPChanged {
		return nil, nil
	}
	res := &FixResult{ID: rec.ID, Compute: compute, detail: d}
	if !dryRun {
		res.qc, res.loops = compute.Serialize(d)
		if err := res.Write(l); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// ResolveMediaPath turns the (often relative) path stored in the Track table
// into a usable filesystem path. Engine DJ databases frequently store paths
// with "../" chains relative to a volume root that no longer matches the DB
// location, so several candidates are tried.
func ResolveMediaPath(dbDir, musicRoot, relPath string) string {
	if relPath == "" {
		return ""
	}
	if filepath.IsAbs(relPath) {
		return relPath
	}
	// Normalize and strip leading dot-dot chains: ../../Volumes/Music/x.mp3 → Volumes/Music/x.mp3
	norm := filepath.ToSlash(relPath)
	for strings.HasPrefix(norm, "./") {
		norm = norm[2:]
	}
	trimmed := norm
	for strings.HasPrefix(trimmed, "../") {
		trimmed = trimmed[3:]
	}

	var candidates []string
	if musicRoot != "" {
		candidates = append(candidates, filepath.Join(musicRoot, trimmed))
	}
	candidates = append(candidates,
		filepath.Join(dbDir, relPath),
		string(filepath.Separator)+trimmed,
	)
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	// Nothing found: return the most plausible candidate for error reporting.
	if musicRoot != "" {
		return filepath.Join(musicRoot, trimmed)
	}
	return filepath.Join(dbDir, relPath)
}

// UpdateTrackMetadata syncs edited media tags back into the Track table so the
// Engine DJ library matches the file. Year is only written when it parses as
// an integer.
func (l *Library) UpdateTrackMetadata(id int64, t MediaTags) error {
	year := sql.NullInt64{}
	if y, ok := parseYear(t.Year); ok {
		year = sql.NullInt64{Int64: y, Valid: true}
	}
	_, err := l.DB.Exec(`UPDATE Track SET title=?, artist=?, album=?, genre=?, comment=?, composer=?, year=? WHERE id=?`,
		t.Title, t.Artist, t.Album, t.Genre, t.Comment, t.Composer, year, id)
	return err
}

func parseYear(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	var y int64
	if _, err := fmt.Sscanf(s, "%d", &y); err != nil || y < 0 || y > 3000 {
		return 0, false
	}
	return y, true
}
