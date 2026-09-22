package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// m4aBuildFixture assembles a minimal but structurally valid M4A file:
//
//	ftyp | moov[ trak[mdia[minf[stbl[stco]]]], udta[meta[ilst[©nam]]] ] | mdat
//
// The stco chunk offsets point into the mdat payload, so a save that changes
// the moov size (moov precedes mdat) must shift them accordingly. The moov
// is built twice (placeholder offsets, then real ones) because its size —
// and therefore the mdat position — does not depend on the offset values.
func m4aBuildFixture(title string) ([]byte, []uint32) {
	box := func(typ string, payload []byte) []byte {
		b := make([]byte, 8+len(payload))
		binary.BigEndian.PutUint32(b[0:4], uint32(len(b)))
		copy(b[4:8], typ)
		copy(b[8:], payload)
		return b
	}

	buildMoov := func(chunkOffsets []uint32) []byte {
		// ilst with one standard ©nam text item.
		ilst := box("ilst", box(string(m4aTitleAtom[:]), m4aDataBox(1, []byte(title))))
		meta := append(m4aMetaShell(), ilst...)
		udta := box("udta", box("meta", meta))

		// stco: version/flags + entry count + offsets.
		stcoPayload := make([]byte, 8+4*len(chunkOffsets))
		binary.BigEndian.PutUint32(stcoPayload[4:8], uint32(len(chunkOffsets)))
		for i, off := range chunkOffsets {
			binary.BigEndian.PutUint32(stcoPayload[8+i*4:], off)
		}
		trak := box("trak", box("mdia", box("minf", box("stbl", box("stco", stcoPayload)))))
		return box("moov", append(append([]byte{}, trak...), udta...))
	}

	ftyp := box("ftyp", []byte("M4A \x00\x00\x00\x00M4A mp42isom"))
	moov := buildMoov([]uint32{0, 0, 0}) // placeholder pass fixes the sizes
	mdatStart := uint32(len(ftyp) + len(moov) + 8)
	chunkOffsets := []uint32{mdatStart + 100, mdatStart + 200, mdatStart + 300}
	moov = buildMoov(chunkOffsets)

	// mdat payload: every chunk offset points at a unique marker inside it.
	mdatPayload := make([]byte, 4096)
	for i := range mdatPayload {
		mdatPayload[i] = byte(i % 251)
	}
	for _, off := range chunkOffsets {
		copy(mdatPayload[off-mdatStart:], "CHUNKDATA")
	}
	mdat := box("mdat", mdatPayload)

	return bytes.Join([][]byte{ftyp, moov, mdat}, nil), chunkOffsets
}

// TestM4ARoundTripSynthetic exercises the full M4A read/write path on a
// fixture whose moov precedes the mdat: the save must rebuild the ilst,
// keep every other byte of the media data intact, and shift the stco chunk
// offsets by exactly the moov size delta.
func TestM4ARoundTripSynthetic(t *testing.T) {
	src, chunkOffsets := m4aBuildFixture("Fixture Song")

	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.m4a")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}

	tags, _, err := ReadMediaTagsWithArt(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if tags.Title != "Fixture Song" {
		t.Fatalf("title = %q, want %q", tags.Title, "Fixture Song")
	}

	tags.Title = "Renamed Song"
	tags.Comment = "#fixture"
	if err := SaveMediaTagsFull(path, tags, nil, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	tags2, _, err := ReadMediaTagsWithArt(path)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if tags2.Title != "Renamed Song" || tags2.Comment != "#fixture" {
		t.Fatalf("round-trip mismatch: %+v", tags2)
	}

	// The mdat payload must be byte-identical and the stco offsets shifted
	// by exactly the moov size change.
	mdatOf := func(b []byte) ([]byte, []uint32) {
		var mdat []byte
		var offsets []uint32
		off := 0
		for off+8 <= len(b) {
			size := int(binary.BigEndian.Uint32(b[off : off+4]))
			if size < 8 || off+size > len(b) {
				t.Fatalf("bad box at %d", off)
			}
			switch string(b[off+4 : off+8]) {
			case "mdat":
				mdat = b[off+8 : off+size]
			case "moov":
				m4aPatchOffsetsFind(b[off+8:off+size], &offsets)
			}
			off += size
		}
		return mdat, offsets
	}
	srcMdat, _ := mdatOf(src)
	dstBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dstMdat, dstOffsets := mdatOf(dstBytes)
	if !bytes.Equal(srcMdat, dstMdat) {
		t.Fatal("mdat payload changed")
	}
	if len(dstOffsets) != len(chunkOffsets) {
		t.Fatalf("stco entries = %d, want %d", len(dstOffsets), len(chunkOffsets))
	}
	newMoov := int64(len(dstBytes)) - int64(len(srcMdat)) - int64(8)
	oldMoov := int64(len(src)) - int64(len(srcMdat)) - int64(8)
	delta := newMoov - oldMoov
	for i := range chunkOffsets {
		if want := uint32(int64(chunkOffsets[i]) + delta); dstOffsets[i] != want {
			t.Errorf("stco[%d] = %d, want %d (delta %d)", i, dstOffsets[i], want, delta)
		}
	}
}

// m4aPatchOffsetsFind collects stco entry values from a moov payload.
func m4aPatchOffsetsFind(payload []byte, out *[]uint32) {
	off := 0
	for off+8 <= len(payload) {
		size := int(binary.BigEndian.Uint32(payload[off : off+4]))
		if size == 0 {
			size = len(payload) - off
		}
		if size < 8 || off+size > len(payload) {
			return
		}
		switch string(payload[off+4 : off+8]) {
		case "trak", "mdia", "minf", "stbl", "udta":
			m4aPatchOffsetsFind(payload[off+8:off+size], out)
		case "stco":
			n := int(binary.BigEndian.Uint32(payload[off+12 : off+16]))
			for i := 0; i < n; i++ {
				*out = append(*out, binary.BigEndian.Uint32(payload[off+16+i*4:]))
			}
		}
		off += size
	}
}

// TestM4AWithPrependedID3 verifies that M4A files which some tools prefix
// with an ID3v2 tag still parse and save correctly (the ID3 block is kept
// verbatim and the box walk starts behind it).
func TestM4AWithPrependedID3(t *testing.T) {
	src, _ := m4aBuildFixture("ID3 Prefixed")

	// Build a minimal ID3v2.3 tag: header + one TIT2 frame + padding.
	frame := append([]byte("TIT2"), 0, 0, 0, 12, 0, 0) // ID + size + flags
	frame = append(frame, 0)                           // encoding
	frame = append(frame, []byte("Bad")...)            // value that must be ignored
	tag := make([]byte, 10+16+128)                     // header + frame + padding
	copy(tag, "ID3\x03\x00")
	tagSize := uint32(len(tag) - 10)
	tag[6], tag[7], tag[8], tag[9] = byte(tagSize>>21)&0x7f, byte(tagSize>>14)&0x7f, byte(tagSize>>7)&0x7f, byte(tagSize)&0x7f
	copy(tag[10:], frame)

	dir := t.TempDir()
	path := filepath.Join(dir, "prefixed.m4a")
	if err := os.WriteFile(path, append(tag, src...), 0o644); err != nil {
		t.Fatal(err)
	}

	tags, _, err := ReadMediaTagsWithArt(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if tags.Title != "ID3 Prefixed" {
		t.Fatalf("title = %q, want the ilst value", tags.Title)
	}
	tags.Comment = "#ok"
	if err := SaveMediaTagsFull(path, tags, nil, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	tags2, _, err := ReadMediaTagsWithArt(path)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if tags2.Comment != "#ok" || tags2.Title != "ID3 Prefixed" {
		t.Fatalf("round-trip mismatch: %+v", tags2)
	}
}
