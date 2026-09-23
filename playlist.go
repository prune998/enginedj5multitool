package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// playlist.go: read the Engine DJ playlist tree and smartlists, evaluate
// smartlist rules against the Track table, and materialize a smartlist into
// a static playlist at a chosen spot in the tree.
//
// Engine DJ stores playlists and folders in the Playlist table (tree via
// parentListId, siblings chained through nextListId, terminated by 0), the
// song membership in PlaylistEntity (nextEntityId points BACK at the
// previously inserted entity; the first entity has 0; play order is entity
// id order), and denormalized helper rows in PlaylistPath ("Title;Parent;
// Grandparent;" reversed names), PlaylistAllParent (all ancestors except
// self) and PlaylistAllChildren (all descendants except self). Smartlists
// live in their own table keyed by UUID with rules as JSON; their tracks
// are never materialized in this database.

// PlaylistNode is one row of the playlist tree (a folder or a playlist).
type PlaylistNode struct {
	ID       int64
	Title    string
	ParentID int64
	Children []*PlaylistNode // ordered by the sibling chain
	IsFolder bool            // has children in the tree
	depth    int             // nesting level, set by PlaylistTree
}

// SmartlistInfo is one dynamic playlist from the Smartlist table.
type SmartlistInfo struct {
	UUID  string
	Title string
	Path  string // display location, e.g. "dynamic;House;"
	Rules string // raw rules JSON
}

// smartlistRules is the parsed Smartlist.rules JSON.
type smartlistRules struct {
	Match string          `json:"match"`
	Rules []smartlistRule `json:"rules"`
}

type smartlistRule struct {
	Col   string `json:"col"`
	Con   string `json:"con"`
	Param string `json:"param"`
}

// smartlistColumns maps Engine smartlist rule columns to Track columns.
var smartlistColumns = map[string]string{
	"title":       "title",
	"artist":      "artist",
	"album":       "album",
	"genre":       "genre",
	"comment":     "comment",
	"label":       "label",
	"composer":    "composer",
	"publisher":   "publisher",
	"key":         "key",
	"bpm":         "bpm",
	"rating":      "rating",
	"length":      "length",
	"year":        "year",
	"trackNumber": "trackNumber",
	"dateAdded":   "dateAdded",
	"dateCreated": "dateCreated",
}

// SmartlistTrack carries the fields needed for the sort criteria.
type SmartlistTrack struct {
	ID        int64
	Title     string
	Artist    string
	BPM       float64
	Rating    int64
	Comment   string
	Key       int64
	DateAdded int64
}

// PlaylistTree loads the playlist/folder tree ordered by the sibling chain.
// The result is the list of root nodes (parentListId = 0).
func (l *Library) PlaylistTree() ([]*PlaylistNode, error) {
	rows, err := l.DB.Query(`SELECT id, title, parentListId FROM Playlist`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int64]*PlaylistNode{}
	var order []int64
	for rows.Next() {
		n := &PlaylistNode{}
		if err := rows.Scan(&n.ID, &n.Title, &n.ParentID); err != nil {
			return nil, err
		}
		byID[n.ID] = n
		order = append(order, n.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Link children by the sibling chain (nextListId, 0 terminates) so the
	// tree order matches what Engine shows; fall back to id order for
	// inconsistent chains.
	children := map[int64][]int64{}
	chainNext := map[int64]int64{}
	for _, id := range order {
		var next int64
		if err := l.DB.QueryRow(`SELECT nextListId FROM Playlist WHERE id = ?`, id).Scan(&next); err != nil {
			return nil, err
		}
		n := byID[id]
		if n.ParentID != 0 {
			children[n.ParentID] = append(children[n.ParentID], id)
		}
		if next != 0 {
			chainNext[id] = next
		}
	}
	for parent, ids := range children {
		// Follow the chain from the head (the child nobody points at).
		pointed := map[int64]bool{}
		for _, id := range ids {
			if nx, ok := chainNext[id]; ok {
				pointed[nx] = true
			}
		}
		var head int64
		for _, id := range ids {
			if !pointed[id] {
				head = id
				break
			}
		}
		var ordered []*PlaylistNode
		seen := map[int64]bool{}
		for id := head; id != 0 && byID[id] != nil && !seen[id]; id = chainNext[id] {
			seen[id] = true
			ordered = append(ordered, byID[id])
		}
		if len(ordered) < len(ids) { // broken chain: append the rest by id
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			for _, id := range ids {
				if !seen[id] {
					ordered = append(ordered, byID[id])
				}
			}
		}
		parent := byID[parent]
		parent.Children = ordered
		for _, c := range ordered {
			c.IsFolder = true // every node referenced as a parent is a folder
		}
	}
	// Mark nodes that have children as folders even when the child link
	// above already did it (belt and braces for empty-child maps).
	var mark func(nodes []*PlaylistNode, depth int)
	mark = func(nodes []*PlaylistNode, depth int) {
		for _, n := range nodes {
			n.depth = depth
			if len(n.Children) > 0 {
				n.IsFolder = true
				mark(n.Children, depth+1)
			}
		}
	}
	var roots []*PlaylistNode
	for _, id := range order {
		if byID[id].ParentID == 0 {
			roots = append(roots, byID[id])
		}
	}
	mark(roots, 0)
	sort.Slice(roots, func(i, j int) bool { return roots[i].ID < roots[j].ID })
	return roots, nil
}

// Smartlists returns every dynamic playlist from the Smartlist table.
func (l *Library) Smartlists() ([]SmartlistInfo, error) {
	rows, err := l.DB.Query(`SELECT listUuid, title, parentPlaylistPath, rules FROM Smartlist ORDER BY title COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SmartlistInfo
	for rows.Next() {
		var s SmartlistInfo
		if err := rows.Scan(&s.UUID, &s.Title, &s.Path, &s.Rules); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// EvaluateSmartlist applies the smartlist rules to the Track table and
// returns the matching tracks in database order.
func (l *Library) EvaluateSmartlist(rulesJSON string) ([]SmartlistTrack, error) {
	where, args, err := smartlistWhere(rulesJSON)
	if err != nil {
		return nil, err
	}
	query := `SELECT id, title, artist, COALESCE(bpm, 0), COALESCE(rating, 0),
		COALESCE(comment, ''), COALESCE(key, -1), COALESCE(dateAdded, 0)
		FROM Track WHERE ` + where + ` ORDER BY id`
	rows, err := l.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SmartlistTrack
	for rows.Next() {
		var t SmartlistTrack
		if err := rows.Scan(&t.ID, &t.Title, &t.Artist, &t.BPM, &t.Rating, &t.Comment, &t.Key, &t.DateAdded); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// smartlistWhere builds the WHERE clause for the smartlist rules. Unknown
// columns or conditions produce an error rather than being silently skipped.
func smartlistWhere(rulesJSON string) (string, []any, error) {
	var r smartlistRules
	if err := json.Unmarshal([]byte(rulesJSON), &r); err != nil {
		return "", nil, fmt.Errorf("parsing smartlist rules: %w", err)
	}
	joiner := " AND "
	if r.Match == "any" {
		joiner = " OR "
	}
	var parts []string
	var args []any
	for _, rule := range r.Rules {
		col, ok := smartlistColumns[rule.Col]
		if !ok {
			return "", nil, fmt.Errorf("unsupported smartlist column %q", rule.Col)
		}
		switch strings.ToUpper(rule.Con) {
		case "LIKE", "NOT LIKE":
			if len(rule.Param) < 2 || !strings.HasPrefix(rule.Param, "'") || !strings.HasSuffix(rule.Param, "'") {
				return "", nil, fmt.Errorf("malformed %s parameter %q", rule.Con, rule.Param)
			}
			parts = append(parts, fmt.Sprintf(`Track."%s" %s ?`, col, strings.ToUpper(rule.Con)))
			args = append(args, strings.Trim(rule.Param, "'"))
		case "EQ", "NE", "GT", "LT", "GTE", "LTE":
			op := map[string]string{
				"EQ": "=", "NE": "<>", "GT": ">", "LT": "<", "GTE": ">=", "LTE": "<=",
			}[strings.ToUpper(rule.Con)]
			parts = append(parts, fmt.Sprintf(`Track."%s" %s ?`, col, op))
			args = append(args, strings.Trim(rule.Param, "'"))
		default:
			return "", nil, fmt.Errorf("unsupported smartlist condition %q", rule.Con)
		}
	}
	if len(parts) == 0 {
		return "1", nil, nil
	}
	return "(" + strings.Join(parts, joiner) + ")", args, nil
}

// SortCriteria names the supported orderings for the playlist creator.
var SortCriteria = []string{"title", "bpm", "rating", "comment", "key", "date"}

// SortSmartlistTracks orders tracks by the chosen criteria (direction:
// ascending when asc is true). Key sorts by the Engine DJ key index
// (0-23, the circle-of-fifths order shown as 1d-8d/1m-8m); ties fall back
// to the track id so the result is stable.
func SortSmartlistTracks(tracks []SmartlistTrack, criteria string, asc bool) {
	less := func(a, b SmartlistTrack) bool { return a.ID < b.ID }
	switch criteria {
	case "title":
		less = func(a, b SmartlistTrack) bool {
			if c := strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)); c != 0 {
				return c < 0
			}
			return strings.ToLower(a.Artist) < strings.ToLower(b.Artist)
		}
	case "bpm":
		less = func(a, b SmartlistTrack) bool { return a.BPM < b.BPM }
	case "rating":
		less = func(a, b SmartlistTrack) bool { return a.Rating < b.Rating }
	case "comment":
		less = func(a, b SmartlistTrack) bool {
			if c := strings.Compare(strings.ToLower(a.Comment), strings.ToLower(b.Comment)); c != 0 {
				return c < 0
			}
			return a.ID < b.ID
		}
	case "key":
		less = func(a, b SmartlistTrack) bool {
			if a.Key != b.Key {
				return a.Key < b.Key
			}
			return a.ID < b.ID
		}
	case "date":
		less = func(a, b SmartlistTrack) bool { return a.DateAdded < b.DateAdded }
	}
	sort.SliceStable(tracks, func(i, j int) bool {
		if asc {
			return less(tracks[i], tracks[j])
		}
		return less(tracks[j], tracks[i])
	})
}

// CreatePlaylist materializes a static playlist under parentID with the
// given tracks in order. It maintains the sibling chain (nextListId) and
// the backward-linked PlaylistEntity chain exactly like Engine DJ does;
// PlaylistPath / PlaylistAllParent / PlaylistAllChildren are views that
// derive from the Playlist rows automatically.
func (l *Library) CreatePlaylist(title string, parentID int64, trackIDs []int64) (int64, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return 0, fmt.Errorf("playlist name is empty")
	}
	tx, err := l.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// The parent must exist (root id 0 is not a Playlist row).
	var parentTitle string
	if err := tx.QueryRow(`SELECT title FROM Playlist WHERE id = ?`, parentID).Scan(&parentTitle); err != nil {
		return 0, fmt.Errorf("destination playlist %d not found: %w", parentID, err)
	}

	// The new playlist becomes the last child: rewire the current last
	// sibling's nextListId to point at it.
	var lastSibling sql.NullInt64
	if err := tx.QueryRow(`SELECT id FROM Playlist WHERE parentListId = ? AND nextListId = 0`,
		parentID).Scan(&lastSibling); err != nil && err != sql.ErrNoRows {
		return 0, err
	}

	res, err := tx.Exec(`INSERT INTO Playlist (title, parentListId, isPersisted, nextListId, lastEditTime, isExplicitlyExported)
		VALUES (?, ?, 1, 0, datetime('now'), 0)`, title, parentID)
	if err != nil {
		return 0, err
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if lastSibling.Valid {
		if _, err := tx.Exec(`UPDATE Playlist SET nextListId = ? WHERE id = ?`, newID, lastSibling.Int64); err != nil {
			return 0, err
		}
	}

	// PlaylistPath, PlaylistAllParent and PlaylistAllChildren are VIEWS in
	// the Engine DJ schema — they derive from the Playlist rows above and
	// must not (and cannot) be written.

	// Entities: Engine plays the playlist by following nextEntityId from
	// the head — the entity nobody points at. Inserting the LAST song
	// first (nextEntityId = 0) and each earlier song pointing at the
	// previously inserted entity makes the head the FIRST song of the
	// playlist, so the play order matches trackIDs exactly. (Inserting in
	// play order produces a reversed playlist — Engine follows the chain,
	// not the entity id order.)
	// A prepared statement keeps large smartlists (1000+ tracks) fast —
	// per-row statement re-parsing dominates otherwise.
	var uuid string
	if err := tx.QueryRow(`SELECT uuid FROM Information LIMIT 1`).Scan(&uuid); err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(`INSERT INTO PlaylistEntity (listId, trackId, databaseUuid, nextEntityId, membershipReference)
		VALUES (?, ?, ?, ?, 0)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	prev := int64(0)
	for i := len(trackIDs) - 1; i >= 0; i-- {
		res, err := stmt.Exec(newID, trackIDs[i], uuid, prev)
		if err != nil {
			return 0, err
		}
		if prev, err = res.LastInsertId(); err != nil {
			return 0, err
		}
	}

	// Bump the parent folder's edit time like Engine does.
	if _, err := tx.Exec(`UPDATE Playlist SET lastEditTime = datetime('now') WHERE id = ?`, parentID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return newID, nil
}
