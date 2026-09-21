package main

import (
	"sync"
	"testing"
	"time"

	. "go.hasen.dev/shirei"
	"go.hasen.dev/shirei/drive"
)

func TestParseHashTags(t *testing.T) {
	got := parseHashTags("#rock and #pop, then #rock again #Rock no-tag ##double")
	if len(got) != 4 || got[0] != "rock" || got[1] != "pop" || got[2] != "Rock" || got[3] != "double" {
		t.Errorf("parseHashTags = %v, want [rock pop Rock double]", got)
	}
	if got := parseHashTags("no tags here"); len(got) != 0 {
		t.Errorf("parseHashTags(no tags) = %v, want empty", got)
	}
}

func TestRemoveTag(t *testing.T) {
	c := "#rock #pop live"
	if got := removeTag(c, "rock"); got != "#pop live" {
		t.Errorf("removeTag = %q, want %q", got, "#pop live")
	}
	if got := removeTag(c, "pop"); got != "#rock live" {
		t.Errorf("removeTag = %q, want %q", got, "#rock live")
	}
	if got := removeTag(c, "nope"); got != c {
		t.Errorf("removeTag missing tag = %q, want unchanged", got)
	}
}

func TestAddTag(t *testing.T) {
	if got := addTag("#rock", "pop"); got != "#rock #pop" {
		t.Errorf("addTag = %q", got)
	}
	if got := addTag("#rock #pop", "pop"); got != "#rock #pop" {
		t.Errorf("addTag duplicate = %q", got)
	}
	if got := addTag("", "rock"); got != "#rock" {
		t.Errorf("addTag empty comment = %q", got)
	}
	if got := addTag("#rock", "two words"); got != "#rock" {
		t.Errorf("addTag with space = %q, want unchanged", got)
	}
}

func TestResolveDark(t *testing.T) {
	if !resolveDark("dark", false) {
		t.Error("dark theme must be dark")
	}
	if resolveDark("light", true) {
		t.Error("light theme must not be dark")
	}
	if !resolveDark("auto", true) {
		t.Error("auto with dark OS must be dark")
	}
	if resolveDark("auto", false) {
		t.Error("auto with light OS must not be dark")
	}
	if resolveDark("", false) {
		t.Error("empty theme falls back to auto")
	}
}

func TestTagHue(t *testing.T) {
	if tagHue("rock") != tagHue("rock") {
		t.Error("tagHue must be stable")
	}
	if tagHue("rock") == tagHue("pop") {
		t.Error("different tags should usually differ")
	}
	if tagHue("rock") < 0 || tagHue("rock") >= 360 {
		t.Errorf("hue %v out of range", tagHue("rock"))
	}
}

// TestDriveSplitterDrag drags the splitter between the track list and the
// detail panel and verifies the browser width follows the mouse (with
// clamping).
func TestDriveSplitterDrag(t *testing.T) {
	InitFontSubsystem()
	ResetInputSession()
	GetHost().WindowSize = Vec2{1600, 740}

	a := NewApp(buildTestLibrary(t))
	if a.splitW != 560 {
		t.Fatalf("initial splitW = %v, want 560", a.splitW)
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

	splitterCenter := func() (float32, float32) {
		res, err := drive.Query(port, "splitter")
		if err != nil || res.Count != 1 || len(res.Nodes) != 1 {
			t.Fatalf("query splitter: count=%d err=%v", res.Count, err)
		}
		rect := res.Nodes[0].Rect
		return rect.Origin[0] + rect.Size[0]/2, rect.Origin[1] + rect.Size[1]/2
	}

	dragBy := func(dx float32) {
		cx, cy := splitterCenter()
		if err := drive.Move(port, cx, cy); err != nil {
			t.Fatal(err)
		}
		drive.Tick()
		if err := drive.Down(port); err != nil {
			t.Fatal(err)
		}
		drive.Tick()
		if err := drive.Move(port, cx+dx, cy); err != nil {
			t.Fatal(err)
		}
		drive.Tick()
		if err := drive.Up(port); err != nil {
			t.Fatal(err)
		}
		drive.Tick()
	}

	// Drag the splitter 120px to the right.
	dragBy(120)
	time.Sleep(40 * time.Millisecond)
	if d := a.splitW - 560; d < 110 || d > 130 {
		t.Errorf("after +120px drag: splitW = %v, want ~680", a.splitW)
	}

	// Drag far left: clamped to the minimum browser width.
	dragBy(-1200)
	time.Sleep(40 * time.Millisecond)
	if a.splitW != minBrowserWidth {
		t.Errorf("after far-left drag: splitW = %v, want %v", a.splitW, minBrowserWidth)
	}
}
