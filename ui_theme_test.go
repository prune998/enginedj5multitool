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

func TestNaturalLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		// The user's example: numeric runs compare numerically.
		{"9aaa", "92test", true},
		{"92test", "9aaa", false},
		{"9", "10", true},
		{"10", "9", false},
		{"2", "10", true},
		{"a2", "a10", true},
		{"a1b", "a1c", true},
		// leading zeros: equal numeric value, no strict order either way
		{"a02", "a2", false},
		{"a2", "a02", false},
		{"a02", "a3", true},
		{"pop", "rock", true},
		{"Rock", "apple", false}, // case-insensitive: apple < rock
		{"prefix", "prefixx", true},
	}
	for _, tc := range cases {
		if got := naturalLess(tc.a, tc.b); got != tc.want {
			t.Errorf("naturalLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSortCommentTags(t *testing.T) {
	// The user's example: #92test must come after #9aaa.
	got := sortCommentTags("#92test #9aaa #10final #2start")
	want := "#2start #9aaa #10final #92test"
	if got != want {
		t.Errorf("sortCommentTags = %q, want %q", got, want)
	}

	// Non-tag words keep their relative order after the sorted tags.
	got = sortCommentTags("#b #a live edit")
	if got != "#a #b live edit" {
		t.Errorf("sortCommentTags with words = %q", got)
	}

	// #cued / #looped always land at the very end, after the sorted tags.
	got = sortCommentTags("#looped #techno #cued #house")
	if got != "#house #techno #cued #looped" {
		t.Errorf("sortCommentTags cued/looped = %q, want %q", got, "#house #techno #cued #looped")
	}
	got = sortCommentTags("#cued #a #looped")
	if got != "#a #cued #looped" {
		t.Errorf("sortCommentTags only cued = %q, want %q", got, "#a #cued #looped")
	}

	// Single tag / empty comment: unchanged.
	if got := sortCommentTags("#only"); got != "#only" {
		t.Errorf("single tag = %q", got)
	}
	if got := sortCommentTags(""); got != "" {
		t.Errorf("empty comment = %q", got)
	}

	// Case-insensitive ordering, original casing preserved.
	got = sortCommentTags("#Pop #apple")
	if got != "#apple #Pop" {
		t.Errorf("case-insensitive sort = %q, want %q", got, "#apple #Pop")
	}
}

func TestKeyInfoFor(t *testing.T) {
	// Engine DJ key table (Mixxx wiki): 0=8B/C maj, 1=8A/A min, 10=1B/B maj,
	// 23=7A/D min. Hours wrap 8..12 then 1..7.
	cases := []struct {
		v    int64
		code string
		root string
		minr bool
	}{
		{0, "8B", "C", false},
		{1, "8A", "A", true},
		{2, "9B", "G", false},
		{3, "9A", "E", true},
		{8, "12B", "E", false},
		{10, "1B", "B", false},
		{11, "1A", "Ab", true},
		{17, "4A", "F", true},
		{22, "7B", "F", false},
		{23, "7A", "D", true},
	}
	for _, tc := range cases {
		ki := keyInfoFor(tc.v)
		if !ki.Live || ki.Code != tc.code || ki.Root != tc.root || ki.Minor != tc.minr {
			t.Errorf("keyInfoFor(%d) = %+v, want %s/%s minor=%v", tc.v, ki, tc.code, tc.root, tc.minr)
		}
	}
	// Unset key.
	if ki := keyInfoFor(-1); ki.Live {
		t.Errorf("keyInfoFor(-1) should not be live: %+v", ki)
	}
	// Relative major/minor share the wheel hue; adjacent hours differ.
	if keyInfoFor(0).Hue != keyInfoFor(1).Hue {
		t.Error("8B and 8A must share the same wheel hue")
	}
	if keyInfoFor(0).Hue == keyInfoFor(2).Hue {
		t.Error("8B and 9B must have different hues")
	}
}

// TestDriveSplitterDrag drags the splitter between the track list and the
// detail panel and verifies the browser width follows the mouse (with
// clamping).
func TestDriveSplitterDrag(t *testing.T) {
	if raceEnabled {
		t.Skip("drive harness races under -race (global shirei state)")
	}
	InitFontSubsystem()
	ResetInputSession()
	GetHost().WindowSize = Vec2{1600, 740}

	a := NewApp(buildTestLibrary(t))
	defer a.Close() // release the DB handle (Windows file locks)
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
