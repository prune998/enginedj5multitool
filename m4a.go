package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

// m4a.go: read/write the iTunes-style metadata atoms (moov/udta/meta/ilst)
// of M4A (MP4) audio files with a self-contained box walker/rewriter.
//
// The rewrite preserves every byte of the file except the rebuilt ilst atom;
// stco/co64 chunk offsets are adjusted when the moov size changes (only
// relevant when the moov sits before the media data).

const m4aHeaderSize = 8

var (
	m4aMoovType    = [4]byte{'m', 'o', 'o', 'v'}
	m4aMdatType    = [4]byte{'m', 'd', 'a', 't'}
	m4aTitleAtom   = [4]byte{0xA9, 'n', 'a', 'm'} // ©nam
	m4aArtistAtom  = [4]byte{0xA9, 'A', 'R', 'T'} // ©ART
	m4aAlbumAtom   = [4]byte{0xA9, 'a', 'l', 'b'} // ©alb
	m4aAlbumArtist = [4]byte{'a', 'A', 'R', 'T'}  // aART
	m4aGenreAtom   = [4]byte{0xA9, 'g', 'e', 'n'} // ©gen
	m4aYearAtom    = [4]byte{0xA9, 'd', 'a', 'y'} // ©day
	m4aTrackAtom   = [4]byte{'t', 'r', 'k', 'n'}  // trkn
	m4aDiscAtom    = [4]byte{'d', 'i', 's', 'k'}  // disk
	m4aComposer    = [4]byte{0xA9, 'w', 'r', 't'} // ©wrt
	m4aBPMAtom     = [4]byte{'t', 'm', 'p', 'o'}  // tmpo
	m4aCommentAtom = [4]byte{0xA9, 'c', 'm', 't'} // ©cmt
	m4aCoverAtom   = [4]byte{'c', 'o', 'v', 'r'}  // covr
)

type m4aBox struct {
	typ     [4]byte
	offset  int64  // absolute offset of the box start (original file)
	header  int64  // header size: 8, or 16 for largesize boxes
	size    int64  // total size incl. header (0 = extends to EOF)
	payload []byte // in-memory payload (loaded for moov)
}

// m4aReadHeader reads one box header at off (8 or 16 bytes).
func m4aReadHeader(f *os.File, off, fileSize int64) (typ [4]byte, header, size int64, err error) {
	var hdr [16]byte
	if _, err := f.ReadAt(hdr[:8], off); err != nil {
		return typ, 0, 0, err
	}
	size = int64(binary.BigEndian.Uint32(hdr[:4]))
	header = 8
	if size == 1 {
		if fileSize-off < 16 {
			return typ, 0, 0, errors.New("truncated largesize header")
		}
		if _, err := f.ReadAt(hdr[:], off); err != nil {
			return typ, 0, 0, err
		}
		size = int64(binary.BigEndian.Uint64(hdr[:8]))
		header = 16
	} else if size == 0 {
		size = fileSize - off // extends to EOF
	}
	copy(typ[:], hdr[4:8])
	return typ, header, size, nil
}

// m4aTopBoxes scans the top-level boxes of the file (headers only).
func m4aTopBoxes(f *os.File) ([]*m4aBox, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	fileSize := st.Size()
	var boxes []*m4aBox
	off := int64(0)
	// Some tools prepend an ID3v2 tag to MP4 files; expose it as a verbatim
	// pseudo-box so the walk — and any file rewrite — keeps it intact.
	if fileSize >= 10 {
		var h [10]byte
		if _, rerr := f.ReadAt(h[:], 0); rerr == nil && h[0] == 'I' && h[1] == 'D' && h[2] == '3' {
			sz := int64(h[6]&0x7f)<<21 | int64(h[7]&0x7f)<<14 | int64(h[8]&0x7f)<<7 | int64(h[9]&0x7f)
			if 10+sz <= fileSize {
				boxes = append(boxes, &m4aBox{
					typ: [4]byte{'I', 'D', '3', ' '}, offset: 0, header: 10, size: 10 + sz,
				})
				off = 10 + sz
			}
		}
	}
	for off+m4aHeaderSize <= fileSize {
		typ, header, size, err := m4aReadHeader(f, off, fileSize)
		if err != nil {
			break // trailing garbage / EOF
		}
		if size < header || off+size > fileSize {
			return nil, fmt.Errorf("invalid top-level box size %d at offset %d", size, off)
		}
		boxes = append(boxes, &m4aBox{typ: typ, offset: off, header: header, size: size})
		off += size
	}
	return boxes, nil
}

// m4aWalkBoxes walks child boxes of a container payload, calling fn for
// each. Returns an error on malformed headers.
func m4aWalkBoxes(payload []byte, fn func(typ [4]byte, payload []byte) error) error {
	off := 0
	for off+m4aHeaderSize <= len(payload) {
		size := int(binary.BigEndian.Uint32(payload[off : off+4]))
		if size == 0 {
			size = len(payload) - off // extends to the container end
		}
		if size < m4aHeaderSize || off+size > len(payload) {
			return errors.New("invalid child box size")
		}
		if err := fn([4]byte(payload[off+4:off+8]), payload[off+8:off+size]); err != nil {
			return err
		}
		off += size
	}
	return nil
}

// m4aChildPayload finds a child box by 4CC in a container payload and
// returns its payload (bytes after the 8-byte header).
func m4aChildPayload(payload []byte, name string) ([]byte, bool) {
	off := 0
	for off+m4aHeaderSize <= len(payload) {
		size := int(binary.BigEndian.Uint32(payload[off : off+4]))
		if size < m4aHeaderSize || off+size > len(payload) {
			return nil, false
		}
		if string(payload[off+4:off+8]) == name {
			return payload[off+8 : off+size], true
		}
		off += size
	}
	return nil, false
}

// m4aIlstSpan locates the ilst box inside a meta box payload. The meta box
// payload may start with 4 version/flags bytes; both layouts are tried.
func m4aIlstSpan(meta []byte) (off, size int, ok bool) {
	for start := 0; start <= 4; start += 4 {
		o := start
		for o+m4aHeaderSize <= len(meta) {
			sz := int(binary.BigEndian.Uint32(meta[o : o+4]))
			if sz < m4aHeaderSize || o+sz > len(meta) {
				break
			}
			if string(meta[o+4:o+8]) == "ilst" {
				return o, sz, true
			}
			o += sz
		}
	}
	return 0, 0, false
}

// readM4ATags reads the iTunes-style metadata atoms of an M4A file.
func readM4ATags(path string) (MediaTags, *MediaArt, error) {
	var t MediaTags
	f, err := os.Open(path)
	if err != nil {
		return t, nil, err
	}
	defer f.Close()

	ilst, err := m4aReadIlst(f)
	if err != nil {
		return t, nil, err
	}
	atoms := map[[4]byte][]byte{}
	if ilst != nil {
		if err := m4aWalkBoxes(ilst, func(typ [4]byte, payload []byte) error {
			atoms[typ] = payload // complete data box (header + value)
			return nil
		}); err != nil {
			return t, nil, fmt.Errorf("reading ilst: %w", err)
		}
	}
	text := func(t [4]byte) string { return m4aText(atoms[t]) }
	t.Title = text(m4aTitleAtom)
	t.Artist = text(m4aArtistAtom)
	t.Album = text(m4aAlbumAtom)
	t.AlbumArtist = text(m4aAlbumArtist)
	t.Genre = text(m4aGenreAtom)
	t.Year = text(m4aYearAtom)
	t.Track = m4aTrackString(atoms)
	t.Disc = m4aDiscString(atoms)
	t.Composer = text(m4aComposer)
	t.BPM = m4aBPMString(atoms)
	t.Comment = text(m4aCommentAtom)
	art := m4aArt(atoms)
	return t, art, nil
}

// m4aReadIlst returns the ilst payload (moov → udta → meta), or nil when
// the file has no metadata.
func m4aReadIlst(f *os.File) ([]byte, error) {
	top, err := m4aTopBoxes(f)
	if err != nil {
		return nil, err
	}
	var moovPayload []byte
	for _, b := range top {
		if b.typ == m4aMoovType {
			payload := make([]byte, b.size-b.header)
			if _, err := f.ReadAt(payload, b.offset+b.header); err != nil {
				return nil, err
			}
			moovPayload = payload
			break
		}
	}
	if moovPayload == nil {
		return nil, nil
	}
	udta, ok := m4aChildPayload(moovPayload, "udta")
	if !ok {
		return nil, nil
	}
	meta, ok := m4aChildPayload(udta, "meta")
	if !ok {
		return nil, nil
	}
	o, s, ok := m4aIlstSpan(meta)
	if !ok {
		return nil, nil
	}
	return meta[o+8 : o+s], nil
}

// m4aAtomValue splits an atom's raw payload into its data type and value
// bytes. Two layouts occur in the wild:
//   - standard: complete data box [size]["data"][type][locale][value]
//   - direct:   some writers omit the data box header: [type][locale][value]
func m4aAtomValue(raw []byte) (dataType uint32, value []byte) {
	if len(raw) >= 16 && string(raw[4:8]) == "data" {
		return binary.BigEndian.Uint32(raw[8:12]), raw[16:]
	}
	if len(raw) >= 8 {
		return binary.BigEndian.Uint32(raw[0:4]), raw[8:]
	}
	return 0, nil
}

// m4aText returns the text of an atom (UTF-8 or UTF-16), or "" for
// non-text atoms.
func m4aText(raw []byte) string {
	dt, value := m4aAtomValue(raw)
	switch dt {
	case 1: // UTF-8
		return string(value)
	case 2: // UTF-16
		return m4aDecodeUTF16(value)
	}
	return ""
}

// m4aDecodeUTF16 decodes a UTF-16 byte string honouring the BOM.
func m4aDecodeUTF16(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	var bo binary.ByteOrder = binary.LittleEndian
	switch {
	case b[0] == 0xFE && b[1] == 0xFF:
		bo = binary.BigEndian
		b = b[2:]
	case b[0] == 0xFF && b[1] == 0xFE:
		b = b[2:]
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, bo.Uint16(b[i:i+2]))
	}
	return string(utf16.Decode(u))
}

// m4aTrackString formats the trkn atom as "n" or "n/total".
func m4aTrackString(atoms map[[4]byte][]byte) string {
	_, v := m4aAtomValue(atoms[m4aTrackAtom])
	if len(v) < 4 {
		return ""
	}
	s := strconv.Itoa(int(binary.BigEndian.Uint16(v[2:4])))
	if len(v) >= 6 {
		if total := int(binary.BigEndian.Uint16(v[4:6])); total > 0 {
			s += "/" + strconv.Itoa(total)
		}
	}
	return s
}

// m4aDiscString formats the disk atom as a decimal string.
func m4aDiscString(atoms map[[4]byte][]byte) string {
	_, v := m4aAtomValue(atoms[m4aDiscAtom])
	if len(v) < 4 {
		return ""
	}
	return strconv.Itoa(int(binary.BigEndian.Uint16(v[2:4])))
}

// m4aBPMString formats the tmpo atom as a decimal string.
func m4aBPMString(atoms map[[4]byte][]byte) string {
	_, v := m4aAtomValue(atoms[m4aBPMAtom])
	if len(v) < 2 {
		return ""
	}
	return strconv.Itoa(int(binary.BigEndian.Uint16(v[:2])))
}

// m4aArt extracts the embedded cover art (covr atom).
func m4aArt(atoms map[[4]byte][]byte) *MediaArt {
	_, v := m4aAtomValue(atoms[m4aCoverAtom])
	if len(v) == 0 {
		return nil
	}
	mime := sniffImageMIME(v, "")
	if mime == "" {
		mime = "image/jpeg"
	}
	return &MediaArt{MIME: mime, Data: v}
}

// saveM4ATags rewrites the ilst metadata of an M4A file.
func saveM4ATags(path string, t MediaTags, art *MediaArt) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	top, err := m4aTopBoxes(f)
	if err != nil {
		f.Close()
		return err
	}
	var moov *m4aBox
	var moovPayload []byte
	for _, b := range top {
		if b.typ == m4aMoovType {
			moov = b
			moovPayload = make([]byte, b.size-b.header)
			if _, err := f.ReadAt(moovPayload, b.offset+b.header); err != nil {
				f.Close()
				return err
			}
			break
		}
	}
	f.Close()
	if moov == nil {
		return errors.New("no moov box found")
	}

	// Existing ilst payload (nil when the file has no metadata yet).
	var oldIlst []byte
	if udta, ok := m4aChildPayload(moovPayload, "udta"); ok {
		if meta, ok := m4aChildPayload(udta, "meta"); ok {
			if o, s, ok := m4aIlstSpan(meta); ok {
				oldIlst = meta[o+8 : o+s]
			}
		}
	}
	atoms := map[[4]byte][]byte{}
	if oldIlst != nil {
		if err := m4aWalkBoxes(oldIlst, func(typ [4]byte, payload []byte) error {
			atoms[typ] = payload
			return nil
		}); err != nil {
			return fmt.Errorf("reading ilst: %w", err)
		}
	}

	// Apply the editable tags. Empty values remove the atom.
	m4aSetText(atoms, m4aTitleAtom, t.Title)
	m4aSetText(atoms, m4aArtistAtom, t.Artist)
	m4aSetText(atoms, m4aAlbumAtom, t.Album)
	m4aSetText(atoms, m4aAlbumArtist, t.AlbumArtist)
	m4aSetText(atoms, m4aGenreAtom, t.Genre)
	m4aSetText(atoms, m4aYearAtom, t.Year)
	m4aSetTrack(atoms, t.Track)
	m4aSetDisc(atoms, t.Disc)
	m4aSetText(atoms, m4aComposer, t.Composer)
	m4aSetBPM(atoms, t.BPM)
	m4aSetText(atoms, m4aCommentAtom, t.Comment)
	if art != nil && len(art.Data) > 0 {
		dt := uint32(14) // PNG
		if art.MIME == "image/jpeg" {
			dt = 13
		}
		atoms[m4aCoverAtom] = m4aDataBox(dt, art.Data)
	}

	newIlst := m4aBuildIlst(oldIlst, atoms)
	newMoov, err := m4aReplaceIlst(moovPayload, newIlst)
	if err != nil {
		return err
	}
	return m4aWriteFile(path, moov, newMoov)
}

// m4aSetText stores a UTF-8 text atom, removing it when empty.
func m4aSetText(atoms map[[4]byte][]byte, typ [4]byte, text string) {
	if strings.TrimSpace(text) == "" {
		delete(atoms, typ)
		return
	}
	atoms[typ] = m4aDataBox(1, []byte(text))
}

// m4aSetTrack stores the trkn atom from a "n" or "n/total" string.
func m4aSetTrack(atoms map[[4]byte][]byte, s string) {
	if strings.TrimSpace(s) == "" {
		delete(atoms, m4aTrackAtom)
		return
	}
	track, total := m4aParseNumTotal(s)
	v := make([]byte, 8)
	binary.BigEndian.PutUint16(v[2:4], uint16(track))
	binary.BigEndian.PutUint16(v[4:6], uint16(total))
	atoms[m4aTrackAtom] = m4aDataBox(0, v)
}

// m4aSetDisc stores the disk atom from a "n" or "n/total" string.
func m4aSetDisc(atoms map[[4]byte][]byte, s string) {
	if strings.TrimSpace(s) == "" {
		delete(atoms, m4aDiscAtom)
		return
	}
	disc, _ := m4aParseNumTotal(s)
	v := make([]byte, 6)
	binary.BigEndian.PutUint16(v[2:4], uint16(disc))
	atoms[m4aDiscAtom] = m4aDataBox(0, v)
}

// m4aSetBPM stores the tmpo atom from a decimal string.
func m4aSetBPM(atoms map[[4]byte][]byte, s string) {
	if strings.TrimSpace(s) == "" {
		delete(atoms, m4aBPMAtom)
		return
	}
	bpm, _ := m4aParseNumTotal(s)
	v := make([]byte, 2)
	binary.BigEndian.PutUint16(v, uint16(bpm))
	atoms[m4aBPMAtom] = m4aDataBox(0, v)
}

// m4aParseNumTotal parses "n", "n/total" or "n/tot" (truncating parts),
// clamped to the uint16 range.
func m4aParseNumTotal(s string) (num, total int) {
	num, total = 0, 0
	if i := strings.IndexByte(s, '/'); i >= 0 {
		total, _ = strconv.Atoi(strings.TrimSpace(s[i+1:]))
		s = s[:i]
	}
	num, _ = strconv.Atoi(strings.TrimSpace(s))
	if num < 0 {
		num = 0
	}
	if num > 65535 {
		num = 65535
	}
	if total < 0 {
		total = 0
	}
	if total > 65535 {
		total = 65535
	}
	return num, total
}

// m4aDataBox builds a complete data box (header + type + locale + value).
func m4aDataBox(dataType uint32, value []byte) []byte {
	size := 8 + 8 + len(value)
	buf := make([]byte, size)
	binary.BigEndian.PutUint32(buf[0:4], uint32(size))
	copy(buf[4:8], "data")
	binary.BigEndian.PutUint32(buf[8:12], dataType)
	binary.BigEndian.PutUint32(buf[12:16], 0) // locale
	copy(buf[16:], value)
	return buf
}

// m4aBuildIlst rewrites the managed atoms inside the ilst payload while
// preserving unmanaged items verbatim (---- atoms, free space, ...).
func m4aBuildIlst(oldIlst []byte, atoms map[[4]byte][]byte) []byte {
	managed := map[[4]byte]bool{
		m4aTitleAtom: true, m4aArtistAtom: true, m4aAlbumAtom: true,
		m4aAlbumArtist: true, m4aGenreAtom: true, m4aYearAtom: true,
		m4aTrackAtom: true, m4aDiscAtom: true, m4aComposer: true,
		m4aBPMAtom: true, m4aCommentAtom: true, m4aCoverAtom: true,
	}
	var out []byte
	emit := func(typ [4]byte, payload []byte) {
		size := 8 + len(payload)
		box := make([]byte, size)
		binary.BigEndian.PutUint32(box[0:4], uint32(size))
		copy(box[4:8], typ[:])
		copy(box[8:], payload)
		out = append(out, box...)
	}
	if oldIlst != nil {
		_ = m4aWalkBoxes(oldIlst, func(typ [4]byte, payload []byte) error {
			if !managed[typ] {
				emit(typ, payload)
			}
			return nil
		})
	}
	// Deterministic order: known atoms first, in tag-form order.
	for _, typ := range [][4]byte{
		m4aTitleAtom, m4aArtistAtom, m4aAlbumAtom, m4aAlbumArtist,
		m4aGenreAtom, m4aYearAtom, m4aTrackAtom, m4aDiscAtom,
		m4aComposer, m4aBPMAtom, m4aCommentAtom, m4aCoverAtom,
	} {
		if payload, ok := atoms[typ]; ok {
			emit(typ, payload)
		}
	}
	return out
}

// m4aReplaceIlst replaces (or appends) the ilst box inside the moov
// payload, rebuilding udta and meta along the way.
func m4aReplaceIlst(moovPayload, newIlst []byte) ([]byte, error) {
	ilstBox := m4aBoxBytes("ilst", newIlst)

	udta, hasUdta := m4aChildPayload(moovPayload, "udta")
	if !hasUdta {
		// Create udta/meta/ilst from scratch.
		meta := append(m4aMetaShell(), ilstBox...)
		udtaBox := m4aBoxBytes("udta", meta)
		return append(append([]byte{}, moovPayload...), udtaBox...), nil
	}
	meta, hasMeta := m4aChildPayload(udta, "meta")
	if !hasMeta {
		meta = append(m4aMetaShell(), ilstBox...)
		newUdta, ok := m4aReplaceChild(udta, "meta", m4aBoxBytes("meta", meta))
		if !ok {
			newUdta = append(append([]byte{}, udta...), m4aBoxBytes("meta", meta)...)
		}
		newMoov, ok := m4aReplaceChild(moovPayload, "udta", m4aBoxBytes("udta", newUdta))
		if !ok {
			return nil, errors.New("cannot rewrite udta box")
		}
		return newMoov, nil
	}
	o, s, ok := m4aIlstSpan(meta)
	if !ok {
		// No ilst inside meta: append one.
		meta = append(append([]byte{}, meta...), ilstBox...)
	} else {
		newMeta := make([]byte, 0, len(meta)-s+len(ilstBox))
		newMeta = append(newMeta, meta[:o]...)
		newMeta = append(newMeta, ilstBox...)
		newMeta = append(newMeta, meta[o+s:]...)
		meta = newMeta
	}
	newUdta, ok := m4aReplaceChild(udta, "meta", m4aBoxBytes("meta", meta))
	if !ok {
		return nil, errors.New("cannot rewrite udta box")
	}
	newMoov, ok := m4aReplaceChild(moovPayload, "udta", m4aBoxBytes("udta", newUdta))
	if !ok {
		return nil, errors.New("cannot rewrite moov box")
	}
	return newMoov, nil
}

// m4aMetaShell builds an empty meta payload: version/flags + mdir hdlr.
func m4aMetaShell() []byte {
	hdlr := make([]byte, 33)
	binary.BigEndian.PutUint32(hdlr[0:4], 33)
	copy(hdlr[4:8], "hdlr")
	copy(hdlr[16:20], "mdir")
	hdlr[32] = 0 // name terminator
	meta := make([]byte, 4)
	return append(meta, hdlr...)
}

// m4aBoxBytes wraps a payload in a box of the given type.
func m4aBoxBytes(typ string, payload []byte) []byte {
	box := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(box[0:4], uint32(len(box)))
	copy(box[4:8], typ)
	copy(box[8:], payload)
	return box
}

// m4aReplaceChild replaces a complete child box (header + payload) inside a
// container payload.
func m4aReplaceChild(payload []byte, name string, newBox []byte) ([]byte, bool) {
	off := 0
	for off+m4aHeaderSize <= len(payload) {
		size := int(binary.BigEndian.Uint32(payload[off : off+4]))
		if size < m4aHeaderSize || off+size > len(payload) {
			return nil, false
		}
		if string(payload[off+4:off+8]) == name {
			out := make([]byte, 0, len(payload)-size+len(newBox))
			out = append(out, payload[:off]...)
			out = append(out, newBox...)
			out = append(out, payload[off+size:]...)
			return out, true
		}
		off += size
	}
	return nil, false
}

// m4aPatchOffsets adjusts stco/co64 chunk offsets by delta, recursing
// through trak/mdia/minf/stbl containers.
func m4aPatchOffsets(payload []byte, delta int64) {
	off := 0
	for off+m4aHeaderSize <= len(payload) {
		size := int(binary.BigEndian.Uint32(payload[off : off+4]))
		if size == 0 {
			size = len(payload) - off
		}
		if size < m4aHeaderSize || off+size > len(payload) {
			return
		}
		switch string(payload[off+4 : off+8]) {
		case "trak", "mdia", "minf", "stbl":
			m4aPatchOffsets(payload[off+8:off+size], delta)
		case "stco":
			n := int(binary.BigEndian.Uint32(payload[off+12 : off+16]))
			for i := 0; i < n; i++ {
				p := off + 16 + i*4
				if p+4 > off+size {
					break
				}
				v := int64(binary.BigEndian.Uint32(payload[p:p+4])) + delta
				binary.BigEndian.PutUint32(payload[p:p+4], uint32(v))
			}
		case "co64":
			n := int(binary.BigEndian.Uint32(payload[off+12 : off+16]))
			for i := 0; i < n; i++ {
				p := off + 16 + i*8
				if p+8 > off+size {
					break
				}
				v := int64(binary.BigEndian.Uint64(payload[p:p+8])) + delta
				binary.BigEndian.PutUint64(payload[p:p+8], uint64(v))
			}
		}
		off += size
	}
}

// m4aWriteFile reassembles the file: the new moov in place of the old one,
// every other top-level box copied verbatim; then atomically replaces the
// original (temp file + rename, mode preserved). The original handle is
// closed BEFORE the rename — Windows refuses to replace open files.
func m4aWriteFile(path string, moov *m4aBox, newMoovPayload []byte) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}

	// Chunk offsets shift only when the moov sits before the media data.
	top, err := m4aTopBoxes(f)
	if err != nil {
		f.Close()
		return err
	}
	delta := (int64(8 + len(newMoovPayload))) - moov.size
	if delta != 0 {
		mdatFirst := false
		for _, b := range top {
			if b.typ == m4aMoovType {
				break
			}
			if b.typ == m4aMdatType {
				mdatFirst = true
				break
			}
		}
		if mdatFirst {
			delta = 0
		} else {
			m4aPatchOffsets(newMoovPayload, delta)
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".m4atmp*")
	if err != nil {
		f.Close()
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	// Write all top-level boxes in order, substituting the moov (matched by
	// offset: the passed-in pointer comes from a different scan).
	writeErr := func() error {
		for _, b := range top {
			if b.typ == m4aMoovType && b.offset == moov.offset {
				head := make([]byte, 8)
				binary.BigEndian.PutUint32(head[0:4], uint32(8+len(newMoovPayload)))
				copy(head[4:8], m4aMoovType[:])
				if _, err := tmp.Write(head); err != nil {
					return err
				}
				if _, err := tmp.Write(newMoovPayload); err != nil {
					return err
				}
				continue
			}
			// Verbatim copy of the original bytes (header + payload).
			if _, err := f.Seek(b.offset, 0); err != nil {
				return err
			}
			if _, err := io.CopyN(tmp, f, b.size); err != nil {
				return err
			}
		}
		return tmp.Close()
	}()
	f.Close() // BEFORE the rename: Windows denies replacing open files
	if writeErr != nil {
		return writeErr
	}
	if err := os.Chmod(tmpName, st.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
