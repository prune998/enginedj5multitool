package main

import (
	"fmt"

	. "go.hasen.dev/shirei"
)

// keyInfo describes an Engine DJ key index (0..23) in Camelot notation.
// The Engine DJ key table pairs each Camelot hour with a major (B) and a
// minor (A) root: 0=8B/C maj, 1=8A/A min, 2=9B/G maj, ... 23=7A/D min.
type keyInfo struct {
	Code  string // e.g. "8B"
	Root  string // e.g. "C" or "A"
	Minor bool
	Hue   float32 // Camelot wheel hue: hours 1..12 spread over the wheel
	Live  bool    // false when the track has no key set (index < 0)
}

// keyRoots holds the MAJOR root note per Camelot hour (1..12). The minor
// (A) root of an hour is the major root of the hour three positions later
// (relative minor), e.g. 8B = C major ↔ 8A = A minor.
var keyRoots = [13]string{"", "B", "F#", "Db", "Ab", "Eb", "Bb", "F", "C", "G", "D", "A", "E"}

// keyInfoFor maps an Engine DJ key index to its Camelot info. Index < 0 means
// "no key set".
func keyInfoFor(v int64) keyInfo {
	if v < 0 || v > 23 {
		return keyInfo{Live: false}
	}
	hour := int(8+v/2-1)%12 + 1
	minor := v%2 == 1
	letter := "B"
	root := keyRoots[hour]
	if minor {
		letter = "A"
		root = keyRoots[(hour+2)%12+1]
	}
	hue := float32(hour-1) * 30
	return keyInfo{
		Code:  fmt.Sprintf("%d%s", hour, letter),
		Root:  root,
		Minor: minor,
		Hue:   hue,
		Live:  true,
	}
}

// keyBadge renders the Camelot key code as a colored chip using the wheel
// color of its hour (majors vivid, minors a deeper shade of the same hue).
func keyBadge(a *App, v int64) {
	p := a.pal()
	ki := keyInfoFor(v)
	if !ki.Live {
		Container(Attrs(FixWidth(64), Pad2(1, 6)), func() {
			a.L("—", FontSize(11), TextColorVec(p.textDim))
		})
		return
	}
	h, s, l := ki.Hue, float32(62), float32(46)
	if ki.Minor {
		s, l = 48, 36
	}
	Container(Attrs(FixWidth(64), Pad2(1, 0)), func() {
		Container(Attrs(Row, CrossMid, Corners(4), Pad2(1, 0), Background(h, s, l, 1)), func() {
			Container(Attrs(FixWidth(6), Pad2(3, 0)), func() {})
			Label(ki.Code, FontSize(11), FontWeight(WeightBold), TextColor(0, 0, 100, 1))
		})
	})
}
