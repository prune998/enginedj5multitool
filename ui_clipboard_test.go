package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "go.hasen.dev/shirei"
	"go.hasen.dev/shirei/drive"
)

// driveClipboard runs the app under the drive harness; the frame loop
// records the clipboard requests the UI makes (out.Copy carries the text
// the backend would write; out.Paste the read requests).
func driveClipboard(t *testing.T, a *App) (port int, stop func()) {
	t.Helper()
	if raceEnabled {
		t.Skip("drive harness races under -race (global shirei state)")
	}
	InitFontSubsystem()
	ResetInputSession()
	GetHost().WindowSize = Vec2{1180, 740}

	p, err := drive.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	AcceptInputCommands(p)

	clipCopyMu.Lock()
	lastCopy = ""
	clipCopyMu.Unlock()
	pasteSeen.Store(false)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
				out := RunFrameFn(a.RootView)
				if out.Copy != "" {
					clipCopyMu.Lock()
					lastCopy = out.Copy
					clipCopyMu.Unlock()
				}
				if out.Paste {
					pasteSeen.Store(true)
				}
				time.Sleep(8 * time.Millisecond)
			}
		}
	}()
	time.Sleep(60 * time.Millisecond) // let a few frames render
	return p, func() {
		close(done)
		wg.Wait()
	}
}

var (
	clipCopyMu sync.Mutex // guards lastCopy (frame loop vs test)
	lastCopy   string     // most recent clipboard write the UI requested
	pasteSeen  atomic.Bool
)

// lastClipboardCopy returns the most recent clipboard write the UI requested.
func lastClipboardCopy() string {
	clipCopyMu.Lock()
	defer clipCopyMu.Unlock()
	return lastCopy
}

// rightClickAt secondary-clicks a point.
func rightClickAt(t *testing.T, port int, x, y float32) {
	t.Helper()
	if err := drive.Move(port, x, y); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if err := drive.Down(port, "secondary"); err != nil {
		t.Fatal(err)
	}
	if err := drive.Up(port, "secondary"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
}

// TestDriveCopyFromLabel right-clicks a browser cell: the context menu must
// offer Copy of the cell text, put it on the clipboard when clicked, and
// close. An outside click must close the menu without copying.
func TestDriveCopyFromLabel(t *testing.T) {
	a := NewApp(buildTestLibrary(t))
	defer a.Close() // release the DB handle (Windows file locks)

	port, stop := driveClipboard(t, a)
	defer stop()
	defer a.lib.Close()

	// Right-click the Artist cell text of track 5 (columns: ID 60 + Stems
	// 48, then Artist — its text starts 6px into the column and the demo
	// artist is wide, so +160 lands on the glyphs).
	n, err := drive.Show(port, "track-5")
	if err != nil {
		t.Fatalf("show track-5: %v", err)
	}
	rightClickAt(t, port, n.Rect.Origin[0]+160, n.Rect.Origin[1]+n.Rect.Size[1]/2)

	if !clipMenu.open {
		t.Fatal("right-click on a label did not open the copy menu")
	}
	if len(clipMenu.items) != 1 || clipMenu.items[0].label != "Copy" {
		t.Fatalf("menu items = %+v, want a single Copy row", clipMenu.items)
	}

	// An outside click (the filter input) closes the menu, copies nothing.
	if _, err := drive.ClickOne(port, "filter-input"); err != nil {
		t.Fatalf("click filter-input: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if clipMenu.open {
		t.Fatal("outside click did not close the context menu")
	}
	if got := lastClipboardCopy(); got != "" {
		t.Fatalf("outside click copied %q", got)
	}

	// Re-open on the same label and click the Copy row.
	if _, err := drive.Show(port, "track-5"); err != nil {
		t.Fatalf("show track-5: %v", err)
	}
	rightClickAt(t, port, n.Rect.Origin[0]+160, n.Rect.Origin[1]+n.Rect.Size[1]/2)
	if !clipMenu.open {
		t.Fatal("second right-click did not re-open the copy menu")
	}
	if _, err := drive.ClickOne(port, "clip-item-copy"); err != nil {
		t.Fatalf("click clip-item-copy: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if clipMenu.open {
		t.Error("choosing Copy did not close the menu")
	}
	if got := lastClipboardCopy(); got != "Purple Disco Machine" {
		t.Errorf("clipboard copy = %q, want the Artist cell text", got)
	}
}

// TestDrivePasteMenuOnField right-clicks the filter box: the menu offers
// Paste; choosing it requests the clipboard read and focuses the field, so
// arriving text lands in the filter draft (without running the search).
func TestDrivePasteMenuOnField(t *testing.T) {
	a := NewApp(buildTestLibrary(t))
	defer a.Close() // release the DB handle (Windows file locks)

	port, stop := driveClipboard(t, a)
	defer stop()
	defer a.lib.Close()

	// Right-click the filter field itself (its rect center is inside the
	// input, whose hover region spans the whole field).
	n, err := drive.Show(port, "filter-input")
	if err != nil {
		t.Fatalf("show filter-input: %v", err)
	}
	rightClickAt(t, port, n.Rect.Origin[0]+n.Rect.Size[0]/2, n.Rect.Origin[1]+n.Rect.Size[1]/2)

	if !clipMenu.open {
		t.Fatal("right-click on a field did not open the context menu")
	}
	if len(clipMenu.items) != 1 || clipMenu.items[0].label != "Paste" {
		t.Fatalf("menu items = %+v, want a single Paste row (field is empty)", clipMenu.items)
	}

	if _, err := drive.ClickOne(port, "clip-item-paste"); err != nil {
		t.Fatalf("click clip-item-paste: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if !pasteSeen.Load() {
		t.Error("choosing Paste did not request the clipboard read")
	}
	if clipMenu.open {
		t.Error("choosing Paste did not close the menu")
	}

	// The field must be focused: typed text lands in the draft (emulating
	// the clipboard text the backend delivers after a paste request).
	if err := drive.Text(port, "Emotion"); err != nil {
		t.Fatalf("type text: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if a.FilterDraft != "Emotion" {
		t.Errorf("FilterDraft = %q, want \"Emotion\" (paste target field not focused)", a.FilterDraft)
	}
	if a.Filter != "" {
		t.Errorf("Filter = %q, want \"\" (paste must not run the search)", a.Filter)
	}
}
