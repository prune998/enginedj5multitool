package main

import (
	"encoding/binary"
	"os"
	"testing"
)

func findStco(t *testing.T, path string) (chunkOffs []uint64, mdatStart, mdatEnd int64) {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fs := int64(len(b))
	var walk func(off, end int64, depth int)
	walk = func(off, end int64, depth int) {
		for off+8 <= end {
			size := int64(binary.BigEndian.Uint32(b[off : off+4]))
			if size == 0 {
				size = end - off
			}
			if size < 8 || off+size > end {
				return
			}
			typ := string(b[off+4 : off+8])
			switch typ {
			case "moov", "trak", "mdia", "minf", "stbl", "udta":
				if depth < 8 {
					walk(off+8, off+size, depth+1)
				}
			case "stco":
				n := int64(binary.BigEndian.Uint32(b[off+12 : off+16]))
				for i := int64(0); i < n; i++ {
					p := off + 16 + i*4
					chunkOffs = append(chunkOffs, uint64(binary.BigEndian.Uint32(b[p:p+4])))
				}
			case "co64":
				n := int64(binary.BigEndian.Uint32(b[off+12 : off+16]))
				for i := int64(0); i < n; i++ {
					p := off + 16 + i*8
					chunkOffs = append(chunkOffs, binary.BigEndian.Uint64(b[p:p+8]))
				}
			case "mdat":
				mdatStart, mdatEnd = off+8, off+size
			}
			off += size
		}
	}
	walk(0, fs, 0)
	return
}

func TestZZStco(t *testing.T) {
	src := "/Users/prune/Music/Music/Media/Music/Compilations/!K7 Tapes/12 Flowerz.m4a"
	dst := "/tmp/m4a_stco_check.m4a"
	b, _ := os.ReadFile(src)
	os.WriteFile(dst, b, 0o644)
	tags, _, err := ReadMediaTagsWithArt(dst)
	if err != nil {
		t.Fatal(err)
	}
	tags.Comment = "#stco"
	if err := SaveMediaTagsFull(dst, tags, nil, nil); err != nil {
		t.Fatal(err)
	}
	o1, m1s, m1e := findStco(t, src)
	o2, m2s, m2e := findStco(t, dst)
	t.Logf("orig: %d chunks, first3=%v mdat=[%d,%d]", len(o1), o1[:min3(3, len(o1))], m1s, m1e)
	t.Logf("new : %d chunks, first3=%v mdat=[%d,%d]", len(o2), o2[:min3(3, len(o2))], m2s, m2e)
	if len(o1) > 0 && len(o2) > 0 {
		t.Logf("delta(first chunk) = %d", int64(o2[0])-int64(o1[0]))
		t.Logf("mdat shift = %d", m2s-m1s)
		inRange := o2[0] >= uint64(m2s) && o2[0] < uint64(m2e)
		t.Logf("new chunk0 in new mdat range: %v", inRange)
	}
	os.Remove(dst)
}

func min3(a, b int) int {
	if a < b {
		return a
	}
	return b
}
