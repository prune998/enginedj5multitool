package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

const defaultCueColor = 0x1dafd7
const defaultLoopColor = 0x1cc608

// standardColors holds the standard cue/loop colours from the Engine Library
// format wiki, indexed by slot (0-based): 1=EAC532 ... 8=158EE2.
var standardColors = [8][4]byte{
	{0xFF, 0xEA, 0xC5, 0x32},
	{0xFF, 0xEA, 0x8F, 0x32},
	{0xFF, 0xB8, 0x55, 0xBF},
	{0xFF, 0xBA, 0x2A, 0x41},
	{0xFF, 0x86, 0xC6, 0x4B},
	{0xFF, 0x20, 0xC6, 0x7C},
	{0xFF, 0x00, 0xA8, 0xB1},
	{0xFF, 0x15, 0x8E, 0xE2},
}

// namedColors is the approximate Engine DJ colour palette used for labelling.
var namedColors = []struct {
	hex  uint32
	name string
}{
	{0xeac532, "yellow"},
	{0xea8f32, "orange"},
	{0xb855bf, "purple"},
	{0xba2a41, "red"},
	{0x86c64b, "green"},
	{0x20c67c, "mint"},
	{0x00a8b1, "teal"},
	{0x158ee2, "blue"},
	{0x1dafd7, "blue (default cue)"},
	{0x1cc608, "green (default loop)"},
	{0xecb000, "amber"},
	{0xf56800, "orange"},
	{0xf4d338, "yellow"},
	{0x1571e2, "blue"},
	{0xff3860, "pink"},
	{0xffffff, "white"},
}

type Cue struct {
	Num    int
	Label  string
	Sample float64 // -1 if not set
	RGBA   [4]byte
}

type Loop struct {
	Num      int
	Label    string
	Start    float64 // samples, -1 if not set
	End      float64 // samples, -1 if not set
	StartSet bool
	EndSet   bool
	RGBA     [4]byte
}

type QuickCues struct {
	Cues       []Cue
	MainPos    float64
	Overridden bool
	DefaultPos float64
}

// TrackDetail is the parsed performance data of a single track.
type TrackDetail struct {
	QC         QuickCues
	Loops      []Loop
	SampleRate float64
	Active     int64 // activeOnLoadLoops bitmask
}

// qUncompress decodes Qt's qCompress payload: 4-byte big-endian length + zlib.
func qUncompress(b []byte) ([]byte, error) {
	if len(b) < 4 {
		return nil, fmt.Errorf("blob too short (%d bytes)", len(b))
	}
	zr, err := zlib.NewReader(bytes.NewReader(b[4:]))
	if err != nil {
		return nil, fmt.Errorf("zlib: %w", err)
	}
	defer zr.Close()
	data, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("inflate: %w", err)
	}
	return data, nil
}

// qCompress encodes into Qt's qCompress payload: 4-byte big-endian length + zlib.
func qCompress(data []byte) []byte {
	var zbuf bytes.Buffer
	zw := zlib.NewWriter(&zbuf)
	zw.Write(data)
	zw.Close()
	out := make([]byte, 4, 4+zbuf.Len())
	binary.BigEndian.PutUint32(out[:4], uint32(len(data)))
	return append(out, zbuf.Bytes()...)
}

// parseQuickCues decodes the quickCues blob (Engine DJ v5):
// [u64 BE count][8 x {u8 labelLen, label, f64 BE pos, RGBA}][f64 BE main][u8 override][f64 BE default]
func parseQuickCues(blob []byte) (QuickCues, error) {
	var qc QuickCues
	data, err := qUncompress(blob)
	if err != nil {
		return qc, err
	}
	if len(data) < 8 {
		return qc, fmt.Errorf("quickCues too short")
	}
	count := int(binary.BigEndian.Uint64(data[:8]))
	off := 8
	cues := make([]Cue, 0, 8)
	for i := 0; i < count; i++ {
		if off >= len(data) {
			return qc, fmt.Errorf("truncated quickCues at cue %d", i+1)
		}
		labelLen := int(data[off])
		off++
		if off+labelLen+8+4 > len(data) {
			return qc, fmt.Errorf("truncated quickCues frame %d", i+1)
		}
		c := Cue{Num: i + 1, Label: string(data[off : off+labelLen])}
		off += labelLen
		c.Sample = math.Float64frombits(binary.BigEndian.Uint64(data[off:]))
		off += 8
		copy(c.RGBA[:], data[off:off+4])
		off += 4
		cues = append(cues, c)
	}
	qc.Cues = cues
	if off+8+1+8 <= len(data) {
		qc.MainPos = math.Float64frombits(binary.BigEndian.Uint64(data[off:]))
		off += 8
		qc.Overridden = data[off] == 1
		off++
		qc.DefaultPos = math.Float64frombits(binary.BigEndian.Uint64(data[off:]))
	}
	return qc, nil
}

// serializeQuickCues re-encodes QuickCues in the Engine DJ v5 layout.
func serializeQuickCues(qc QuickCues) []byte {
	var buf bytes.Buffer
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], uint64(len(qc.Cues)))
	buf.Write(tmp[:])
	for _, c := range qc.Cues {
		buf.WriteByte(byte(len(c.Label)))
		buf.WriteString(c.Label)
		binary.BigEndian.PutUint64(tmp[:], math.Float64bits(c.Sample))
		buf.Write(tmp[:])
		buf.Write(c.RGBA[:])
	}
	binary.BigEndian.PutUint64(tmp[:], math.Float64bits(qc.MainPos))
	buf.Write(tmp[:])
	if qc.Overridden {
		buf.WriteByte(1)
	} else {
		buf.WriteByte(0)
	}
	binary.BigEndian.PutUint64(tmp[:], math.Float64bits(qc.DefaultPos))
	buf.Write(tmp[:])
	return qCompress(buf.Bytes())
}

// parseLoops decodes the loops blob (Engine DJ v5, uncompressed):
// [u8 count][7 pad][8 x {u8 labelLen, label, f64 LE start, f64 LE end, u8 startSet, u8 endSet, RGBA}]
func parseLoops(blob []byte) ([]Loop, error) {
	if len(blob) < 8 {
		return nil, fmt.Errorf("loops blob too short")
	}
	count := int(blob[0])
	off := 8
	loops := make([]Loop, 0, 8)
	for i := 0; i < count; i++ {
		if off >= len(blob) {
			return nil, fmt.Errorf("truncated loops at loop %d", i+1)
		}
		labelLen := int(blob[off])
		off++
		if off+labelLen+8+8+2+4 > len(blob) {
			return nil, fmt.Errorf("truncated loops frame %d", i+1)
		}
		l := Loop{Num: i + 1, Label: string(blob[off : off+labelLen])}
		off += labelLen
		l.Start = math.Float64frombits(binary.LittleEndian.Uint64(blob[off:]))
		off += 8
		l.End = math.Float64frombits(binary.LittleEndian.Uint64(blob[off:]))
		off += 8
		l.StartSet = blob[off] == 1
		off++
		l.EndSet = blob[off] == 1
		off++
		copy(l.RGBA[:], blob[off:off+4])
		off += 4
		loops = append(loops, l)
	}
	return loops, nil
}

// serializeLoops re-encodes loops in the Engine DJ v5 layout.
func serializeLoops(loops []Loop) []byte {
	var buf bytes.Buffer
	buf.WriteByte(byte(len(loops)))
	buf.Write(make([]byte, 7))
	var tmp [8]byte
	for _, l := range loops {
		buf.WriteByte(byte(len(l.Label)))
		buf.WriteString(l.Label)
		binary.LittleEndian.PutUint64(tmp[:], math.Float64bits(l.Start))
		buf.Write(tmp[:])
		binary.LittleEndian.PutUint64(tmp[:], math.Float64bits(l.End))
		buf.Write(tmp[:])
		if l.StartSet {
			buf.WriteByte(1)
		} else {
			buf.WriteByte(0)
		}
		if l.EndSet {
			buf.WriteByte(1)
		} else {
			buf.WriteByte(0)
		}
		buf.Write(l.RGBA[:])
	}
	return buf.Bytes()
}

// parseTrackData extracts sample rate (f64 BE @0) and length in samples (u64 BE @8).
// The blob is qCompressed; raw (uncompressed) blobs are handled as a fallback.
func parseTrackData(blob []byte) (sampleRate float64, trackLen int64) {
	if len(blob) >= 4 {
		if data, err := qUncompress(blob); err == nil {
			blob = data
		}
	}
	if len(blob) >= 8 {
		sampleRate = math.Float64frombits(binary.BigEndian.Uint64(blob[:8]))
		if math.IsNaN(sampleRate) || sampleRate < 8000 || sampleRate > 192000 {
			sampleRate = 0
		}
	}
	if len(blob) >= 16 {
		trackLen = int64(binary.BigEndian.Uint64(blob[8:16]))
	}
	return
}

func colorName(rgba [4]byte) string {
	rgb := uint32(rgba[1])<<16 | uint32(rgba[2])<<8 | uint32(rgba[3])
	if rgb == 0 {
		return "-"
	}
	best, bestDist := "unknown", uint32(math.MaxUint32)
	for _, c := range namedColors {
		d := dist(rgb, c.hex)
		if d < bestDist {
			bestDist, best = d, c.name
		}
	}
	return best
}

func dist(a, b uint32) uint32 {
	d := uint32(0)
	for shift := uint(0); shift < 24; shift += 8 {
		av := int((a >> shift) & 0xff)
		bv := int((b >> shift) & 0xff)
		diff := av - bv
		if diff < 0 {
			diff = -diff
		}
		d += uint32(diff * diff)
	}
	return d
}

func hexColor(rgba [4]byte) string {
	if rgba[0] == 0 && rgba[1] == 0 && rgba[2] == 0 && rgba[3] == 0 {
		return "-"
	}
	return fmt.Sprintf("#%02X%02X%02X", rgba[1], rgba[2], rgba[3])
}

// rgbToHSL converts 0-255 RGB (alpha ignored) to shirei's HSL ranges
// (h in degrees, s/l in percent).
func rgbToHSL(rgba [4]byte) (h, s, l float32) {
	r := float64(rgba[1]) / 255
	g := float64(rgba[2]) / 255
	b := float64(rgba[3]) / 255
	max := math.Max(r, math.Max(g, b))
	min := math.Min(r, math.Min(g, b))
	l = float32((max + min) / 2)
	if max == min {
		return 0, 0, l
	}
	d := max - min
	if l > 0.5 {
		s = float32(d / (2 - max - min))
	} else {
		s = float32(d / (max + min))
	}
	var hh float64
	switch max {
	case r:
		hh = (g - b) / d
		if g < b {
			hh += 6
		}
	case g:
		hh = (b-r)/d + 2
	default:
		hh = (r-g)/d + 4
	}
	return float32(hh * 60), s * 100, l * 100
}

func fmtTime(seconds float64) string {
	if seconds < 0 {
		return "-"
	}
	m := int(seconds) / 60
	s := seconds - float64(m*60)
	return fmt.Sprintf("%d:%06.3f", m, s)
}

// inOrder reports whether set entries are monotonically increasing in time.
func inOrderSamples(vals []float64) bool {
	prev := math.Inf(-1)
	for _, v := range vals {
		if v < 0 {
			continue
		}
		if v < prev {
			return false
		}
		prev = v
	}
	return true
}

func swatch(rgba [4]byte, useColor bool) string {
	if !useColor {
		return ""
	}
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm  \x1b[0m", rgba[1], rgba[2], rgba[3])
}

// slotAssignment returns, for the given chronological sample values (one per
// set item, in original slot order), the new 0-based slot for each: items
// sorted ascending fill slots 0..N-2 and the latest always lands on slot 7
// (position 8).
func slotAssignment(samples []float64) []int {
	idx := make([]int, len(samples))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return samples[idx[a]] < samples[idx[b]] })
	slots := make([]int, len(samples))
	for pos, orig := range idx {
		if pos == len(idx)-1 {
			slots[orig] = 7
		} else {
			slots[orig] = pos
		}
	}
	return slots
}

type itemChange struct {
	oldSlot   int
	newSlot   int
	when      string // display timestamp
	end       string // display end (loops)
	label     string
	recolored bool
	relabeled bool
	moved     bool
}

func isDefaultLabel(kind string, label string, slot int) bool {
	return label == fmt.Sprintf("%s %d", kind, slot)
}

// slotLabel applies the labelling convention: slot 1 is "intro" and slot 8 is
// "outro"; elsewhere default labels (and position-bound "intro"/"outro" that
// moved to a middle slot) are renamed to "Cue N"/"Loop N" while genuinely
// custom labels are preserved.
func slotLabel(kind, label string, oldSlot, newSlot int) string {
	switch newSlot {
	case 0:
		return "intro"
	case 7:
		return "outro"
	}
	if isDefaultLabel(kind, label, oldSlot) || label == "intro" || label == "outro" {
		return fmt.Sprintf("%s %d", kind, newSlot+1)
	}
	return label
}

// fixCues reassigns set cues chronologically (latest to slot 8), applies the
// standard slot colour and renames labels. Returns the new 8-slot layout and
// the list of changes.
func fixCues(cues []Cue) ([]Cue, []itemChange, bool) {
	var setIdx []int
	for i, c := range cues {
		if c.Sample >= 0 {
			setIdx = append(setIdx, i)
		}
	}
	if len(setIdx) == 0 {
		return cues, nil, false
	}
	samples := make([]float64, len(setIdx))
	for k, i := range setIdx {
		samples[k] = cues[i].Sample
	}
	slots := slotAssignment(samples)

	out := make([]Cue, 8)
	for i := range out {
		out[i] = Cue{Num: i + 1, Sample: -1}
	}
	var changes []itemChange
	for k, orig := range setIdx {
		c := cues[orig]
		newSlot := slots[k]
		moved := newSlot+1 != c.Num
		recolored := c.RGBA != standardColors[newSlot]
		newLabel := slotLabel("Cue", c.Label, c.Num, newSlot)
		relabeled := newLabel != c.Label
		c.Label = newLabel
		c.Num = newSlot + 1
		c.RGBA = standardColors[newSlot]
		out[newSlot] = c
		changes = append(changes, itemChange{
			oldSlot: cues[orig].Num, newSlot: newSlot + 1,
			when: fmtTime(c.Sample / 44100), label: c.Label,
			moved: moved, recolored: recolored, relabeled: relabeled,
		})
	}
	changed := false
	for _, ch := range changes {
		if ch.moved || ch.recolored || ch.relabeled {
			changed = true
			break
		}
	}
	return out, changes, changed
}

// fixLoops reassigns set loops chronologically (latest to slot 8), applies the
// standard slot colour and renames labels. Returns the new 8-slot layout, the
// changes and the old→new slot permutation (1-based).
func fixLoops(loops []Loop) ([]Loop, []itemChange, map[int]int, bool) {
	var setIdx []int
	for i, l := range loops {
		if l.StartSet && l.Start >= 0 {
			setIdx = append(setIdx, i)
		}
	}
	if len(setIdx) == 0 {
		return loops, nil, nil, false
	}
	samples := make([]float64, len(setIdx))
	for k, i := range setIdx {
		samples[k] = loops[i].Start
	}
	slots := slotAssignment(samples)

	out := make([]Loop, 8)
	for i := range out {
		out[i] = Loop{Num: i + 1, Start: -1, End: -1}
	}
	perm := map[int]int{}
	var changes []itemChange
	for k, orig := range setIdx {
		l := loops[orig]
		newSlot := slots[k]
		moved := newSlot+1 != l.Num
		recolored := l.RGBA != standardColors[newSlot]
		newLabel := slotLabel("Loop", l.Label, l.Num, newSlot)
		relabeled := newLabel != l.Label
		l.Label = newLabel
		perm[l.Num] = newSlot + 1
		l.Num = newSlot + 1
		l.RGBA = standardColors[newSlot]
		out[newSlot] = l
		changes = append(changes, itemChange{
			oldSlot: loops[orig].Num, newSlot: newSlot + 1,
			when: fmtTime(l.Start / 44100), end: fmtTime(l.End / 44100), label: l.Label,
			moved: moved, recolored: recolored, relabeled: relabeled,
		})
	}
	changed := false
	for _, ch := range changes {
		if ch.moved || ch.recolored || ch.relabeled {
			changed = true
			break
		}
	}
	return out, changes, perm, changed
}

// remapActiveOnLoad moves the per-slot bits of activeOnLoadLoops (bit i =
// slot i+1) according to the loop permutation.
func remapActiveOnLoad(active int64, perm map[int]int) int64 {
	if active <= 0 || active > 0xff || len(perm) == 0 {
		return active
	}
	newVal := int64(0)
	for i := 0; i < 8; i++ {
		if active&(1<<i) != 0 {
			if ns, ok := perm[i+1]; ok {
				newVal |= 1 << (ns - 1)
			} else {
				newVal |= 1 << i
			}
		}
	}
	return newVal
}

// ParseTrackDetail loads and parses the performance blobs of one track from
// the given DB handle. Used by the UI tool views.
func parseTrackDetail(trackData, quickCues, loops []byte, active int64) (*TrackDetail, error) {
	d := &TrackDetail{Active: active}
	d.SampleRate, _ = parseTrackData(trackData)
	if d.SampleRate == 0 {
		d.SampleRate = 44100
	}
	var cueErr, loopErr error
	if len(quickCues) > 0 {
		d.QC, cueErr = parseQuickCues(quickCues)
	}
	if len(loops) > 0 {
		d.Loops, loopErr = parseLoops(loops)
	}
	if cueErr != nil {
		return nil, cueErr
	}
	if loopErr != nil {
		return nil, loopErr
	}
	return d, nil
}

// FixComputeResult is the outcome of computing a fix for one track.
type FixComputeResult struct {
	NewCues  []Cue
	NewLoops []Loop

	CueChanges  []itemChange
	LoopChanges []itemChange

	QCChanged bool
	LPChanged bool

	Active   int64 // new activeOnLoadLoops value
	ActiveCh bool
}

// ComputeFix computes the cue/loop fix for a parsed track detail.
func ComputeFix(d *TrackDetail) *FixComputeResult {
	newCues, cueChanges, qcChanged := fixCues(d.QC.Cues)
	newLoops, loopChanges, perm, lpChanged := fixLoops(d.Loops)
	newActive := remapActiveOnLoad(d.Active, perm)
	activeChanged := newActive != d.Active
	if activeChanged {
		lpChanged = true
	}
	// stamp display timestamps with the track's real sample rate
	for i := range cueChanges {
		cueChanges[i].when = fmtTime(newCues[cueChanges[i].newSlot-1].Sample / d.SampleRate)
	}
	for i := range loopChanges {
		l := newLoops[loopChanges[i].newSlot-1]
		loopChanges[i].when = fmtTime(l.Start / d.SampleRate)
		loopChanges[i].end = fmtTime(l.End / d.SampleRate)
	}
	return &FixComputeResult{
		NewCues: newCues, NewLoops: newLoops,
		CueChanges: cueChanges, LoopChanges: loopChanges,
		QCChanged: qcChanged, LPChanged: lpChanged,
		Active: newActive, ActiveCh: activeChanged,
	}
}

// Serialize produces the updated blobs for a computed fix. Only the blobs that
// changed are non-nil.
func (r *FixComputeResult) Serialize(d *TrackDetail) (qc []byte, loops []byte) {
	if r.QCChanged {
		qcCopy := d.QC
		qcCopy.Cues = r.NewCues
		qc = serializeQuickCues(qcCopy)
	}
	if r.LPChanged {
		loops = serializeLoops(r.NewLoops)
	}
	return
}

func changeTags(ch itemChange) string {
	var tags []string
	if ch.moved {
		tags = append(tags, "move")
	}
	if ch.recolored {
		tags = append(tags, "recolor")
	}
	if ch.relabeled {
		tags = append(tags, "relabel")
	}
	if len(tags) == 0 {
		return ""
	}
	return strings.Join(tags, ", ")
}
