package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	. "go.hasen.dev/shirei"
	"go.hasen.dev/shirei/drive"
)

// TestTagsFormFieldsFit renders the tags form and asserts the small fields
// (Year / Track / Disc / BPM) are laid out left-to-right inside the panel
// without overflowing past its right edge.
func TestTagsFormFieldsFit(t *testing.T) {
	InitFontSubsystem()
	ResetInputSession()
	GetHost().WindowSize = Vec2{1180, 740}

	dbPath := buildTestLibrary(t)
	dir := filepath.Dir(dbPath)
	mp3 := filepath.Join(dir, "Emotion.mp3")
	if err := os.WriteFile(mp3, bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x00}, 256), 0o644); err != nil {
		t.Fatal(err)
	}
	{
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE Track SET path = ? WHERE id = 5`, mp3); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}

	a := NewApp(dbPath)

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
	a.ActiveTool = 1
	a.Selected = 5
	time.Sleep(100 * time.Millisecond)

	panelRight := float32(1180) - 10 // detail panel padding
	prevRight := float32(0)
	for _, name := range []string{"field-Year", "field-Track", "field-Disc", "field-BPM"} {
		res, err := drive.Query(port, name)
		if err != nil || res.Count != 1 || len(res.Nodes) != 1 {
			t.Fatalf("query %s: count=%d len=%d err=%v", name, res.Count, len(res.Nodes), err)
		}
		r := res.Nodes[0].Rect
		if r.Size[0] <= 0 || r.Size[1] <= 0 {
			t.Errorf("%s has zero size (squashed by the row): %v", name, r)
		}
		if right := r.Origin[0] + r.Size[0]; right > panelRight+1 {
			t.Errorf("%s overflows the panel: right=%.1f > %.1f", name, right, panelRight)
		}
		if r.Origin[0] < prevRight {
			t.Errorf("%s is not placed after the previous field (x=%.1f, prev right=%.1f)", name, r.Origin[0], prevRight)
		}
		prevRight = r.Origin[0] + r.Size[0]
	}
}
