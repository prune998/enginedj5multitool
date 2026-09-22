// Command genicon renders the app icon (skull-and-crossbones glyph) and
// writes it as PNG and as a macOS .icns file with all modern sizes.
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
)

const (
	master      = 1024 // icon master size
	supersample = 4    // supersampling factor for antialiasing
)

type vec struct{ x, y float64 }

// --- geometry primitives (1024-space, screen coords: y grows down) ---

func inEllipse(p, c vec, rx, ry, rotDeg float64) bool {
	s, co := math.Sincos(rotDeg * math.Pi / 180)
	dx, dy := p.x-c.x, p.y-c.y
	u := dx*co + dy*s
	v := -dx*s + dy*co
	return u*u/(rx*rx)+v*v/(ry*ry) <= 1
}

func inRoundedRect(p vec, x0, y0, x1, y1, r float64) bool {
	if p.x < x0 || p.x > x1 || p.y < y0 || p.y > y1 {
		return false
	}
	qx := math.Min(math.Max(p.x, x0+r), x1-r)
	qy := math.Min(math.Max(p.y, y0+r), y1-r)
	return (p.x-qx)*(p.x-qx)+(p.y-qy)*(p.y-qy) <= r*r
}

func cross(p, a, b vec) float64 {
	return (b.x-a.x)*(p.y-a.y) - (b.y-a.y)*(p.x-a.x)
}

// bone is a capsule bar with two knob circles at each end.
func inBone(p, c vec, angleDeg, halfLen, halfW, knobR, knobU, knobOff float64) bool {
	s, co := math.Sincos(angleDeg * math.Pi / 180)
	dx, dy := p.x-c.x, p.y-c.y
	u := dx*co + dy*s
	v := -dx*s + dy*co
	if math.Abs(u) <= halfLen && math.Abs(v) <= halfW {
		return true
	}
	for _, su := range [2]float64{-1, 1} {
		for _, sv := range [2]float64{-1, 1} {
			du := u - su*knobU
			dv := v - sv*knobOff
			if du*du+dv*dv <= knobR*knobR {
				return true
			}
		}
	}
	return false
}

// --- glyph shapes (d param dilates the silhouette by d points) ---
// Proportions mapped from the reference artwork (256px → 1024px, ×4).

func skullOuter(p vec, d float64) bool {
	if inEllipse(p, vec{512, 228}, 252+d, 188+d, 0) {
		return true
	}
	return inRoundedRect(p, 320-d, 380-d, 704+d, 552+d, 60)
}

func inEyeL(p vec) bool { return inEllipse(p, vec{396, 278}, 66, 74, 0) }
func inEyeR(p vec) bool { return inEllipse(p, vec{628, 278}, 66, 74, 0) }

func inNose(p vec) bool {
	return convexDist(p, vec{512, 368}, vec{480, 416}, vec{544, 416}) <= 10
}

// convexDist returns the signed distance to a convex polygon (positive
// outside, negative inside) — the max over edge half-planes, exact for
// convex shapes. Rounding comes from comparing against a positive radius.
func convexDist(p vec, verts ...vec) float64 {
	var cx, cy float64
	for _, v := range verts {
		cx += v.x
		cy += v.y
	}
	cx /= float64(len(verts))
	cy /= float64(len(verts))
	d := -1e18
	n := len(verts)
	for i := 0; i < n; i++ {
		v := verts[i]
		w := verts[(i+1)%n]
		ex, ey := w.x-v.x, w.y-v.y
		l := math.Hypot(ex, ey)
		nx, ny := ey/l, -ex/l
		if nx*((v.x+w.x)/2-cx)+ny*((v.y+w.y)/2-cy) < 0 {
			nx, ny = -nx, -ny // ensure the normal points outward
		}
		if dd := (p.x-v.x)*nx + (p.y-v.y)*ny; dd > d {
			d = dd
		}
	}
	return d
}

func inToothSlot(p vec) bool {
	for _, cx := range [3]float64{432, 512, 592} {
		if p.x >= cx-5 && p.x <= cx+5 && p.y >= 448 && p.y <= 540 {
			return true
		}
	}
	return false
}

func inSkull(p vec) bool {
	if !skullOuter(p, 0) {
		return false
	}
	return !(inEyeL(p) || inEyeR(p) || inNose(p) || inToothSlot(p))
}

func inBoneBack(p vec, d float64) bool {
	return inBone(p, vec{512, 700}, -21.5, 326+d, 44+d, 68+d, 304, 56)
}

func inBoneFront(p vec, d float64) bool {
	return inBone(p, vec{512, 700}, 21.5, 326+d, 44+d, 68+d, 304, 56)
}

// renderMask evaluates the glyph coverage mask at supersampled resolution.
// Paint order: back bone, front bone (with outline gap), skull on top
// (with outline gap) — matching the classic glyph layering.
func renderMask() []float64 {
	n := master * supersample
	mask := make([]float64, n*n)
	for py := 0; py < n; py++ {
		y := (float64(py) + 0.5) / supersample
		for px := 0; px < n; px++ {
			x := (float64(px) + 0.5) / supersample
			p := vec{x, y}
			var m float64
			if inBoneBack(p, 0) {
				m = 1
			}
			if inBoneFront(p, 10) {
				m = 0
			}
			if inBoneFront(p, 0) {
				m = 1
			}
			if skullOuter(p, 12) {
				m = 0
			}
			if inSkull(p) {
				m = 1
			}
			mask[py*n+px] = m
		}
	}
	return mask
}

// downsampleBox2 averages a square image by 2 in each dimension.
func downsampleBox2(src []float64, size int) []float64 {
	half := size / 2
	dst := make([]float64, half*half)
	for y := 0; y < half; y++ {
		for x := 0; x < half; x++ {
			sx, sy := 2*x, 2*y
			dst[y*half+x] = (src[sy*size+sx] + src[sy*size+sx+1] +
				src[(sy+1)*size+sx] + src[(sy+1)*size+sx+1]) / 4
		}
	}
	return dst
}

func maskToImage(mask []float64, size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			g := uint8(255*mask[y*size+x] + 0.5)
			o := (y*size + x) * 4
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = g, g, g, 255
		}
	}
	return img
}

// icnsEntry is one icon size inside the .icns container.
type icnsEntry struct {
	code string
	size int
}

// icnsEntries are the modern PNG-based sizes (macOS 10.7+).
var icnsEntries = []icnsEntry{
	{"ic07", 128},
	{"ic08", 256},
	{"ic09", 512},
	{"ic10", 1024}, // 512x512@2x
	{"ic11", 32},   // 16x16@2x
	{"ic12", 64},   // 32x32@2x
	{"ic13", 256},  // 128x128@2x
	{"ic14", 512},  // 256x256@2x
}

// writeICNS packs the mipmap chain into an .icns container.
func writeICNS(path string, master1024 []float64) error {
	// Build the power-of-two mip chain: 1024 → 512 → ... → 32.
	mips := map[int][]float64{1024: master1024}
	for size := 1024; size > 32; size /= 2 {
		mips[size/2] = downsampleBox2(mips[size], size)
	}
	var buf bytes.Buffer
	buf.WriteString("icns")
	sizePos := buf.Len()
	buf.Write(make([]byte, 4)) // total size, filled at the end
	for _, e := range icnsEntries {
		img := maskToImage(mips[e.size], e.size)
		var pngBuf bytes.Buffer
		if err := png.Encode(&pngBuf, img); err != nil {
			return err
		}
		buf.WriteString(e.code)
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], uint32(8+len(pngBuf.Bytes())))
		buf.Write(hdr[:])
		buf.Write(pngBuf.Bytes())
	}
	binary.BigEndian.PutUint32(buf.Bytes()[sizePos:sizePos+4], uint32(buf.Len()))
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func main() {
	pngPath := flag.String("png", "", "write the 1024px master PNG here")
	icnsPath := flag.String("icns", "", "write the .icns file here")
	flag.Parse()

	mask := renderMask()
	// Collapse the supersampled mask to the 1024 master used everywhere.
	m1024 := downsampleBox2(downsampleBox2(mask, master*supersample), master*supersample/2)
	if *pngPath != "" {
		if err := pngWrite(*pngPath, maskToImage(m1024, master)); err != nil {
			fatal(err)
		}
	}
	if *icnsPath != "" {
		if err := writeICNS(*icnsPath, m1024); err != nil {
			fatal(err)
		}
	}
}

func pngWrite(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "genicon: %v\n", err)
	os.Exit(1)
}
