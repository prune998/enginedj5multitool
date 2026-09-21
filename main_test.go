package main

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"reflect"
	"testing"

	_ "modernc.org/sqlite"
)

// Real blobs extracted from an Engine DJ v5 database (m.db), used as fixtures.
const (
	fixtTrack5QuickCues = "0000009A789C636000030E56E7D254054387957CB6F177EE67FD975D7F9D61FF070638004B1B3B860B1C99C5FBF703481A2C62E298E75C36FD5B5E3B760D668E497B96EA1DCD8D856B3077CC68E07D1CFBF6138606381BC60000C73930A5"
	fixtTrack5Loops     = "080000000000000000000000000000F0BF000000000000F0BF000000000000064C6F6F702032A2AF20AD7D504141438B6C07323244410101FF1CC60800000000000000F0BF000000000000F0BF00000000000000000000000000F0BF000000000000F0BF000000000000064C6F6F7020358CF9B961E61347412CD505BC9AF549410101FF1CC60800000000000000F0BF000000000000F0BF00000000000000000000000000F0BF000000000000F0BF000000000000064C6F6F702038FBE93F23AD9F6E416E6589AEE3FB6E410101FF1CC608"
	fixtTrack5TrackData = "00000044789C73785EC000038C7CA79AFE0301038900007C9E06EF"
	fixtTrack3QuickCues = "00000086789C636000030E56E7D254054387D827FC2B0FEA2BFC975D7F9D61FF070604A02A07CE8631009D0F168E"
)

var (
	defCueColor  = [4]byte{0xFF, 0x1D, 0xAF, 0xD7}
	defLoopColor = [4]byte{0xFF, 0x1C, 0xC6, 0x08}
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex fixture: %v", err)
	}
	return b
}

func emptyCues() []Cue {
	cues := make([]Cue, 8)
	for i := range cues {
		cues[i] = Cue{Num: i + 1, Sample: -1}
	}
	return cues
}

func emptyLoops() []Loop {
	loops := make([]Loop, 8)
	for i := range loops {
		loops[i] = Loop{Num: i + 1, Start: -1, End: -1}
	}
	return loops
}

func TestQCompressRoundTrip(t *testing.T) {
	payloads := [][]byte{
		{},
		[]byte("hello world"),
		bytes.Repeat([]byte{0xAB, 0x00, 0x13}, 1000),
	}
	for i, p := range payloads {
		got, err := qUncompress(qCompress(p))
		if err != nil {
			t.Fatalf("payload %d: %v", i, err)
		}
		if !bytes.Equal(got, p) {
			t.Errorf("payload %d: round-trip mismatch", i)
		}
	}
}

func TestQUncompressRealFixture(t *testing.T) {
	data, err := qUncompress(mustHex(t, fixtTrack5QuickCues))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 154 {
		t.Fatalf("uncompressed length = %d, want 154", len(data))
	}
	if got := binary.BigEndian.Uint64(data[:8]); got != 8 {
		t.Fatalf("count = %d, want 8", got)
	}
}

func TestParseQuickCuesTrack5(t *testing.T) {
	qc, err := parseQuickCues(mustHex(t, fixtTrack5QuickCues))
	if err != nil {
		t.Fatal(err)
	}
	if len(qc.Cues) != 8 {
		t.Fatalf("got %d cues, want 8", len(qc.Cues))
	}
	wantPos := map[int]float64{1: 3207.1, 3: 6046482.4, 4: 15866804.7, 6: 9823529.5, 7: 12845167.1}
	for _, c := range qc.Cues {
		if want, ok := wantPos[c.Num]; ok {
			if math.Abs(c.Sample-want) > 0.5 {
				t.Errorf("cue %d: sample = %.1f, want ~%.1f", c.Num, c.Sample, want)
			}
			if c.Label != fmt.Sprintf("Cue %d", c.Num) {
				t.Errorf("cue %d: label = %q", c.Num, c.Label)
			}
			if c.RGBA != defCueColor {
				t.Errorf("cue %d: rgba = %v, want %v", c.Num, c.RGBA, defCueColor)
			}
		} else {
			if c.Sample != -1 {
				t.Errorf("cue %d: expected empty, sample = %.1f", c.Num, c.Sample)
			}
			if c.RGBA != ([4]byte{}) {
				t.Errorf("cue %d: expected zero rgba, got %v", c.Num, c.RGBA)
			}
			if c.Label != "" {
				t.Errorf("cue %d: expected empty label, got %q", c.Num, c.Label)
			}
		}
	}
	if qc.MainPos != -1 || qc.Overridden || qc.DefaultPos != -1 {
		t.Errorf("trailer = main:%v override:%v default:%v, want -1/false/-1", qc.MainPos, qc.Overridden, qc.DefaultPos)
	}
}

func TestParseQuickCuesTrack3SingleCue(t *testing.T) {
	qc, err := parseQuickCues(mustHex(t, fixtTrack3QuickCues))
	if err != nil {
		t.Fatal(err)
	}
	if len(qc.Cues) != 8 {
		t.Fatalf("got %d cues, want 8", len(qc.Cues))
	}
	c1 := qc.Cues[0]
	if math.Abs(c1.Sample-119.6) > 0.5 || c1.Label != "Cue 1" || c1.RGBA != defCueColor {
		t.Errorf("cue 1 = %+v, want single default-colored cue at ~119.6", c1)
	}
	for _, c := range qc.Cues[1:] {
		if c.Sample != -1 || c.Label != "" || c.RGBA != ([4]byte{}) {
			t.Errorf("cue %d: expected empty slot, got %+v", c.Num, c)
		}
	}
}

func TestParseLoopsTrack5(t *testing.T) {
	loops, err := parseLoops(mustHex(t, fixtTrack5Loops))
	if err != nil {
		t.Fatal(err)
	}
	if len(loops) != 8 {
		t.Fatalf("got %d loops, want 8", len(loops))
	}
	const sr = 48000.0
	want := map[int][2]string{
		2: {"0:47.280", "0:55.149"},
		5: {"1:03.018", "1:10.886"},
		8: {"5:34.493", "5:38.427"},
	}
	for _, l := range loops {
		if w, ok := want[l.Num]; ok {
			if got := fmtTime(l.Start / sr); got != w[0] {
				t.Errorf("loop %d: start = %s, want %s", l.Num, got, w[0])
			}
			if got := fmtTime(l.End / sr); got != w[1] {
				t.Errorf("loop %d: end = %s, want %s", l.Num, got, w[1])
			}
			if !l.StartSet || !l.EndSet {
				t.Errorf("loop %d: expected start/end set", l.Num)
			}
			if l.Label != fmt.Sprintf("Loop %d", l.Num) {
				t.Errorf("loop %d: label = %q", l.Num, l.Label)
			}
			if l.RGBA != defLoopColor {
				t.Errorf("loop %d: rgba = %v, want %v", l.Num, l.RGBA, defLoopColor)
			}
		} else {
			if l.Start != -1 || l.End != -1 || l.StartSet || l.EndSet || l.RGBA != ([4]byte{}) {
				t.Errorf("loop %d: expected empty slot, got %+v", l.Num, l)
			}
		}
	}
}

func TestParseTrackData(t *testing.T) {
	// Real (qCompressed) blob
	sr, ln := parseTrackData(mustHex(t, fixtTrack5TrackData))
	if sr != 48000 {
		t.Errorf("sampleRate = %v, want 48000", sr)
	}
	if ln != 17746562 {
		t.Errorf("length = %d, want 17746562", ln)
	}

	// Raw (uncompressed) blob passthrough
	raw := make([]byte, 16)
	binary.BigEndian.PutUint64(raw[:8], math.Float64bits(44100))
	binary.BigEndian.PutUint64(raw[8:], 12345678)
	if sr, ln := parseTrackData(raw); sr != 44100 || ln != 12345678 {
		t.Errorf("raw blob: sampleRate = %v, length = %d, want 44100/12345678", sr, ln)
	}

	// NaN sample rate in compressed payload
	nan := make([]byte, 16)
	binary.BigEndian.PutUint64(nan[:8], math.Float64bits(math.NaN()))
	binary.BigEndian.PutUint64(nan[8:], 99)
	if sr, _ := parseTrackData(qCompress(nan)); sr != 0 {
		t.Errorf("sampleRate for NaN = %v, want 0", sr)
	}
	if sr, _ := parseTrackData([]byte{1, 2, 3}); sr != 0 {
		t.Errorf("sampleRate for short blob = %v, want 0", sr)
	}
}

func TestSerializeQuickCuesRoundTrip(t *testing.T) {
	orig := mustHex(t, fixtTrack5QuickCues)
	qc1, err := parseQuickCues(orig)
	if err != nil {
		t.Fatal(err)
	}
	ser := serializeQuickCues(qc1)
	qc2, err := parseQuickCues(ser)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(qc1, qc2) {
		t.Error("quickCues parse→serialize→parse not stable")
	}
	a, _ := qUncompress(orig)
	b, _ := qUncompress(ser)
	if !bytes.Equal(a, b) {
		t.Error("uncompressed payload changed after round-trip")
	}
	if n := binary.BigEndian.Uint32(ser[:4]); n != uint32(len(b)) {
		t.Errorf("length prefix = %d, want %d", n, len(b))
	}
}

func TestSerializeLoopsRoundTrip(t *testing.T) {
	orig := mustHex(t, fixtTrack5Loops)
	loops1, err := parseLoops(orig)
	if err != nil {
		t.Fatal(err)
	}
	ser := serializeLoops(loops1)
	if !bytes.Equal(ser, orig) {
		t.Error("loops serialize(parse(x)) != x byte-for-byte")
	}
	loops2, err := parseLoops(ser)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loops1, loops2) {
		t.Error("loops parse→serialize→parse not stable")
	}
}

func TestSlotAssignment(t *testing.T) {
	cases := []struct {
		name    string
		samples []float64
		want    []int
	}{
		{"ascending fills 1,2 then latest at 8", []float64{10, 20, 30}, []int{0, 1, 7}},
		{"single item goes to slot 8", []float64{42}, []int{7}},
		{"descending input", []float64{30, 20, 10}, []int{7, 1, 0}},
		{"shuffled", []float64{30, 10, 20}, []int{7, 0, 1}},
		{"eight items sequential", []float64{1, 2, 3, 4, 5, 6, 7, 8}, []int{0, 1, 2, 3, 4, 5, 6, 7}},
		{"ties keep stable order", []float64{10, 10}, []int{0, 7}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := slotAssignment(tc.samples)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("slotAssignment(%v) = %v, want %v", tc.samples, got, tc.want)
			}
		})
	}
}

func cueAt(slot int, label string, sample float64) Cue {
	return Cue{Num: slot, Label: label, Sample: sample}
}

func TestFixCues(t *testing.T) {
	cues := emptyCues()
	cues[0] = cueAt(1, "Cue 1", 3000)    // → slot 2
	cues[2] = cueAt(3, "My Label", 5000) // → slot 3, custom label kept
	cues[3] = cueAt(4, "Cue 4", 1000)    // → slot 1, "intro"
	cues[5] = cueAt(6, "Cue 6", 7000)    // latest → slot 8, "outro"

	out, changes, changed := fixCues(cues)
	if !changed {
		t.Fatal("expected changed = true")
	}
	if len(changes) != 4 {
		t.Fatalf("got %d changes, want 4", len(changes))
	}
	want := []Cue{
		{Num: 1, Label: "intro", Sample: 1000, RGBA: standardColors[0]},
		{Num: 2, Label: "Cue 2", Sample: 3000, RGBA: standardColors[1]},
		{Num: 3, Label: "My Label", Sample: 5000, RGBA: standardColors[2]},
		{Num: 8, Label: "outro", Sample: 7000, RGBA: standardColors[7]},
	}
	for _, w := range want {
		got := out[w.Num-1]
		if got != w {
			t.Errorf("slot %d = %+v, want %+v", w.Num, got, w)
		}
	}
	for i := 3; i <= 6; i++ {
		if out[i].Sample != -1 || out[i].Label != "" || out[i].RGBA != ([4]byte{}) {
			t.Errorf("slot %d: expected empty, got %+v", i+1, out[i])
		}
	}
}

func TestFixCuesAlreadyClean(t *testing.T) {
	cues := emptyCues()
	cues[0] = Cue{Num: 1, Label: "intro", Sample: 10, RGBA: standardColors[0]}
	cues[1] = Cue{Num: 2, Label: "Cue 2", Sample: 20, RGBA: standardColors[1]}
	cues[7] = Cue{Num: 8, Label: "outro", Sample: 90, RGBA: standardColors[7]}

	_, _, changed := fixCues(cues)
	if changed {
		t.Error("expected changed = false for already clean layout")
	}
}

func TestFixCuesNormalizesMovedIntroOutro(t *testing.T) {
	cues := emptyCues()
	cues[0] = cueAt(1, "Cue 1", 100)
	cues[1] = cueAt(2, "Cue 2", 300)
	cues[2] = cueAt(3, "intro", 200) // "intro" drifting to a middle slot

	out, _, _ := fixCues(cues)
	if out[0].Label != "intro" || out[0].Sample != 100 {
		t.Errorf("slot 1 = %+v, want intro @100", out[0])
	}
	if out[1].Label != "Cue 2" || out[1].Sample != 200 {
		t.Errorf("slot 2 = %+v, want 'Cue 2' @200 (normalized)", out[1])
	}
	if out[7].Label != "outro" || out[7].Sample != 300 {
		t.Errorf("slot 8 = %+v, want outro @300", out[7])
	}
}

func loopAt(slot int, label string, start, end float64) Loop {
	return Loop{Num: slot, Label: label, Start: start, End: end, StartSet: true, EndSet: true}
}

func TestFixLoops(t *testing.T) {
	loops := emptyLoops()
	loops[1] = loopAt(2, "Loop 2", 2000, 3000)
	loops[1].RGBA = defLoopColor
	loops[4] = loopAt(5, "Loop 5", 1000, 1500)
	loops[4].RGBA = [4]byte{0xFF, 0x01, 0x02, 0x03}
	loops[7] = loopAt(8, "Loop 8", 5000, 6000)
	loops[7].RGBA = defLoopColor

	out, changes, perm, changed := fixLoops(loops)
	if !changed {
		t.Fatal("expected changed = true")
	}
	if len(changes) != 3 {
		t.Fatalf("got %d changes, want 3", len(changes))
	}
	wantPerm := map[int]int{2: 2, 5: 1, 8: 8}
	if !reflect.DeepEqual(perm, wantPerm) {
		t.Errorf("perm = %v, want %v", perm, wantPerm)
	}
	want := []Loop{
		{Num: 1, Label: "intro", Start: 1000, End: 1500, StartSet: true, EndSet: true, RGBA: standardColors[0]},
		{Num: 2, Label: "Loop 2", Start: 2000, End: 3000, StartSet: true, EndSet: true, RGBA: standardColors[1]},
		{Num: 8, Label: "outro", Start: 5000, End: 6000, StartSet: true, EndSet: true, RGBA: standardColors[7]},
	}
	for _, w := range want {
		got := out[w.Num-1]
		if got != w {
			t.Errorf("slot %d = %+v, want %+v", w.Num, got, w)
		}
	}
	for i := 2; i <= 6; i++ {
		if out[i].Start != -1 || out[i].End != -1 || out[i].StartSet || out[i].EndSet || out[i].RGBA != ([4]byte{}) {
			t.Errorf("slot %d: expected empty, got %+v", i+1, out[i])
		}
	}
}

func TestFixLoopsNoSetLoops(t *testing.T) {
	loops := emptyLoops()
	loops[0].Label = "Loop 1" // label without positions: not set
	out, changes, perm, changed := fixLoops(loops)
	if changed {
		t.Error("expected changed = false")
	}
	if changes != nil || perm != nil {
		t.Errorf("expected nil changes/perm, got %v %v", changes, perm)
	}
	if !reflect.DeepEqual(out, loops) {
		t.Error("layout should be unchanged")
	}
}

func TestRemapActiveOnLoad(t *testing.T) {
	cases := []struct {
		name   string
		active int64
		perm   map[int]int
		want   int64
	}{
		{"zero stays zero", 0, map[int]int{1: 8}, 0},
		{"bit follows its loop", 32, map[int]int{6: 2}, 2},
		{"identity", 32, map[int]int{6: 6}, 32},
		{"unrelated perm leaves bit", 32, map[int]int{2: 1}, 32},
		{"high slot", 1 << 7, map[int]int{8: 3}, 1 << 2},
		{"multi-bit", 0b10000001, map[int]int{1: 5, 8: 2}, 0b00010010},
		{"out of byte range untouched", 0x1FF, map[int]int{1: 8}, 0x1FF},
		{"empty perm untouched", 32, map[int]int{}, 32},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := remapActiveOnLoad(tc.active, tc.perm); got != tc.want {
				t.Errorf("remapActiveOnLoad(%d, %v) = %d, want %d", tc.active, tc.perm, got, tc.want)
			}
		})
	}
}

func TestInOrderSamples(t *testing.T) {
	cases := []struct {
		name string
		vals []float64
		want bool
	}{
		{"empty", nil, true},
		{"ascending with gaps", []float64{-1, 3, -1, 5}, true},
		{"descending", []float64{9, 3}, false},
		{"descending across gaps", []float64{-1, 5, -1, 3}, false},
		{"equal values allowed", []float64{3, 3}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inOrderSamples(tc.vals); got != tc.want {
				t.Errorf("inOrderSamples(%v) = %v, want %v", tc.vals, got, tc.want)
			}
		})
	}
}

func TestSlotLabel(t *testing.T) {
	cases := []struct {
		kind, label string
		old, new    int
		want        string
	}{
		{"Cue", "Cue 1", 1, 0, "intro"},
		{"Loop", "Loop 2", 2, 0, "intro"},
		{"Cue", "Cue 4", 4, 7, "outro"},
		{"Loop", "Loop 8", 8, 7, "outro"},
		{"Cue", "Cue 1", 1, 1, "Cue 2"},
		{"Loop", "Loop 8", 8, 3, "Loop 4"},
		{"Cue", "intro", 3, 1, "Cue 2"},
		{"Loop", "outro", 8, 3, "Loop 4"},
		{"Cue", "My Label", 3, 4, "My Label"},
		{"Loop", "Breakdown", 5, 6, "Breakdown"},
		{"Cue", "Cue 2", 2, 0, "intro"},
	}
	for _, tc := range cases {
		got := slotLabel(tc.kind, tc.label, tc.old, tc.new)
		if got != tc.want {
			t.Errorf("slotLabel(%q, %q, old=%d, new=%d) = %q, want %q",
				tc.kind, tc.label, tc.old, tc.new, got, tc.want)
		}
	}
}

func TestFmtTime(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0:00.000"},
		{75.5, "1:15.500"},
		{369.7, "6:09.700"},
		{-1, "-"},
	}
	for _, tc := range cases {
		if got := fmtTime(tc.in); got != tc.want {
			t.Errorf("fmtTime(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHexColorAndColorName(t *testing.T) {
	if got := hexColor([4]byte{}); got != "-" {
		t.Errorf("hexColor(empty) = %q, want \"-\"", got)
	}
	if got := hexColor(standardColors[0]); got != "#EAC532" {
		t.Errorf("hexColor(slot1) = %q, want #EAC532", got)
	}
	if got := colorName(standardColors[0]); got != "yellow" {
		t.Errorf("colorName(slot1) = %q, want yellow", got)
	}
	if got := colorName(defCueColor); got != "blue (default cue)" {
		t.Errorf("colorName(default cue) = %q", got)
	}
	if got := colorName([4]byte{}); got != "-" {
		t.Errorf("colorName(empty) = %q, want \"-\"", got)
	}
}

func TestGoldenFixTrack5(t *testing.T) {
	qc, err := parseQuickCues(mustHex(t, fixtTrack5QuickCues))
	if err != nil {
		t.Fatal(err)
	}
	loops, err := parseLoops(mustHex(t, fixtTrack5Loops))
	if err != nil {
		t.Fatal(err)
	}

	newCues, cueChanges, cuesChanged := fixCues(qc.Cues)
	newLoops, loopChanges, _, loopsChanged := fixLoops(loops)
	if !cuesChanged || !loopsChanged {
		t.Fatal("expected both cues and loops to change")
	}
	if len(cueChanges) != 5 || len(loopChanges) != 3 {
		t.Fatalf("changes: %d cues, %d loops; want 5 and 3", len(cueChanges), len(loopChanges))
	}

	const sr = 48000.0
	wantCues := map[int]struct {
		when  string
		label string
		color [4]byte
	}{
		1: {"0:00.067", "intro", standardColors[0]},
		2: {"2:05.968", "Cue 2", standardColors[1]},
		3: {"3:24.657", "Cue 3", standardColors[2]},
		4: {"4:27.608", "Cue 4", standardColors[3]},
		8: {"5:30.558", "outro", standardColors[7]},
	}
	for slot, w := range wantCues {
		c := newCues[slot-1]
		if got := fmtTime(c.Sample / sr); got != w.when {
			t.Errorf("cue slot %d: time = %s, want %s", slot, got, w.when)
		}
		if c.Label != w.label {
			t.Errorf("cue slot %d: label = %q, want %q", slot, c.Label, w.label)
		}
		if c.RGBA != w.color {
			t.Errorf("cue slot %d: rgba = %v, want %v", slot, c.RGBA, w.color)
		}
	}

	wantLoops := map[int]struct {
		start, end string
		label      string
		color      [4]byte
	}{
		1: {"0:47.280", "0:55.149", "intro", standardColors[0]},
		2: {"1:03.018", "1:10.886", "Loop 2", standardColors[1]},
		8: {"5:34.493", "5:38.427", "outro", standardColors[7]},
	}
	for slot, w := range wantLoops {
		l := newLoops[slot-1]
		if got := fmtTime(l.Start / sr); got != w.start {
			t.Errorf("loop slot %d: start = %s, want %s", slot, got, w.start)
		}
		if got := fmtTime(l.End / sr); got != w.end {
			t.Errorf("loop slot %d: end = %s, want %s", slot, got, w.end)
		}
		if l.Label != w.label {
			t.Errorf("loop slot %d: label = %q, want %q", slot, l.Label, w.label)
		}
		if l.RGBA != w.color {
			t.Errorf("loop slot %d: rgba = %v, want %v", slot, l.RGBA, w.color)
		}
	}

	// Serialized results must parse back identically.
	qc.Cues = newCues
	backQC, err := parseQuickCues(serializeQuickCues(qc))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backQC.Cues, newCues) {
		t.Error("fixed cues do not survive serialization")
	}
	backLoops, err := parseLoops(serializeLoops(newLoops))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backLoops, newLoops) {
		t.Error("fixed loops do not survive serialization")
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := parseQuickCues(nil); err == nil {
		t.Error("parseQuickCues(nil): expected error")
	}
	if _, err := parseLoops(nil); err == nil {
		t.Error("parseLoops(nil): expected error")
	}
	if _, err := parseQuickCues([]byte{0, 0, 0, 1, 0x78, 0x9C, 0x00}); err == nil {
		t.Error("parseQuickCues(garbage): expected error")
	}
	full := mustHex(t, fixtTrack5Loops)
	if _, err := parseLoops(full[:20]); err == nil {
		t.Error("parseLoops(truncated): expected error")
	}
	// quickCues claiming more cues than the payload holds
	bad := qCompress([]byte{0, 0, 0, 0, 0, 0, 0, 9, 5, 'x'})
	if _, err := parseQuickCues(bad); err == nil {
		t.Error("parseQuickCues(truncated frames): expected error")
	}
}

func TestInMemoryDBRoundTrip(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE PerformanceData (
		trackId INTEGER PRIMARY KEY, quickCues BLOB, loops BLOB)`); err != nil {
		t.Fatal(err)
	}

	qc, err := parseQuickCues(mustHex(t, fixtTrack5QuickCues))
	if err != nil {
		t.Fatal(err)
	}
	loops, err := parseLoops(mustHex(t, fixtTrack5Loops))
	if err != nil {
		t.Fatal(err)
	}
	qcBytes, loopsBytes := serializeQuickCues(qc), serializeLoops(loops)
	if _, err := db.Exec(`INSERT INTO PerformanceData VALUES (5, ?, ?)`, qcBytes, loopsBytes); err != nil {
		t.Fatal(err)
	}

	var gotQC, gotLoops []byte
	if err := db.QueryRow(`SELECT quickCues, loops FROM PerformanceData WHERE trackId = 5`).Scan(&gotQC, &gotLoops); err != nil {
		t.Fatal(err)
	}
	backQC, err := parseQuickCues(gotQC)
	if err != nil {
		t.Fatal(err)
	}
	backLoops, err := parseLoops(gotLoops)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backQC, qc) || !reflect.DeepEqual(backLoops, loops) {
		t.Error("stored blobs do not parse back identically")
	}
}
