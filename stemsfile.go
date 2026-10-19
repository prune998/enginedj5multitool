package main

// stemsfile.go: Engine DJ .stems container support, ported from the
// standalone `stems` tool (pure Go, no cgo).
//
// A .stems file is an ISO-BMFF (MP4) container with a single AAC track whose
// packets are padded (PKCS#7) and encrypted with AES-128-ECB. Stems are
// stereo pairs in Vocals / Bass / Drums / Other order: stem 1 = ch 0/1
// (Vocals), stem 2 = ch 2/3 (Bass), ... The AudioSpecificConfig in the esds
// box carries a program config element (channel configuration 0) describing
// the 4 stereo pairs.
//
// The decryption key is a secret: it is never compiled into the binary nor
// committed to Git. It is provided at runtime through a gitignored
// `stems_key` file (working directory or user config directory) or the
// ENGINDJ5_STEMS_KEY environment variable, hex-encoded (see LoadStemsKey).
//
// Decoding to PCM is delegated to an ffmpeg subprocess (no cgo, works on
// macOS, Linux and Windows): the decrypted packets are remuxed into a clear
// MP4 streamed to ffmpeg's stdin, and 8-channel float PCM comes back on
// stdout.

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// stemsKey holds the Engine DJ stems payload key (16 bytes for AES-128).
// It is configured at startup via SetStemsKey and stays out of the source
// tree and the binary on purpose.
var stemsKey []byte

// StemsKeyEnvVar is the environment variable carrying the stems payload key
// (hex-encoded). It takes precedence over the stems_key files.
const StemsKeyEnvVar = "ENGINDJ5_STEMS_KEY"

// StemsKeyFile is the gitignored file holding the stems payload key (32
// hex digits). It is looked up in the working directory and in the user
// config directory (see StemsKeyPaths).
const StemsKeyFile = "stems_key"

// StemsKeyPaths lists the gitignored key file locations, most specific
// first.
func StemsKeyPaths() []string {
	paths := []string{StemsKeyFile}
	if dir, err := ConfigDir(); err == nil {
		paths = append(paths, filepath.Join(dir, StemsKeyFile))
	}
	return paths
}

// LoadStemsKey resolves and configures the stems payload key. Precedence:
// the ENGINDJ5_STEMS_KEY environment variable, the gitignored stems_key
// file in the working directory, the one in the user config directory, and
// finally the stems_key setting from config.yaml (configKey argument). It
// returns the source description ("env", or the file/config path) and a
// non-nil error when a configured key is malformed; ("", nil) means no
// source is configured, in which case stems playback reports the missing
// key when used.
func LoadStemsKey(configKey string) (string, error) {
	if v := strings.TrimSpace(os.Getenv(StemsKeyEnvVar)); v != "" {
		return "env", SetStemsKey(v)
	}
	for _, path := range StemsKeyPaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		v := strings.TrimSpace(string(data))
		if v == "" {
			return "", fmt.Errorf("%s is empty — put the 32 hex digits of the stems key in it", path)
		}
		if err := SetStemsKey(v); err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		return path, nil
	}
	if v := strings.TrimSpace(configKey); v != "" {
		return "config.yaml", SetStemsKey(v)
	}
	return "", nil
}

// SetStemsKey configures the stems payload key from its hex encoding
// (32 hex digits = 16 bytes).
func SetStemsKey(hexKey string) error {
	k, err := hex.DecodeString(strings.TrimSpace(hexKey))
	if err != nil {
		return fmt.Errorf("stems key is not valid hex: %w", err)
	}
	if len(k) != 16 {
		return fmt.Errorf("stems key is %d bytes, want 16 (32 hex digits)", len(k))
	}
	stemsKey = k
	return nil
}

// StemsKeyConfigured reports whether a decryption key has been set.
func StemsKeyConfigured() bool { return len(stemsKey) == 16 }

// StemsKeySource describes where the configured key came from (best-effort,
// for display): "env", a stems_key file path, "config.yaml", or "" when no
// key is configured.
func StemsKeySource() string {
	if !StemsKeyConfigured() {
		return ""
	}
	if os.Getenv(StemsKeyEnvVar) != "" {
		return "env"
	}
	for _, path := range StemsKeyPaths() {
		if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) != "" {
			return path
		}
	}
	return "config.yaml"
}

func stemsCipher() (cipher.Block, error) {
	if len(stemsKey) != 16 {
		return nil, errors.New("stems key not configured: put the 32 hex digits of the key in a gitignored stems_key file, the stems_key setting in config.yaml, or set " + StemsKeyEnvVar)
	}
	return aes.NewCipher(stemsKey)
}

// ---------------------------------------------------------------- little helpers

func pkcs7Pad(b []byte) []byte {
	p := 16 - len(b)%16
	out := make([]byte, len(b)+p)
	copy(out, b)
	for i := len(b); i < len(out); i++ {
		out[i] = byte(p)
	}
	return out
}

func pkcs7Unpad(b []byte) ([]byte, error) {
	if len(b) == 0 || len(b)%16 != 0 {
		return nil, errors.New("bad block length")
	}
	p := int(b[len(b)-1])
	if p < 1 || p > 16 || p > len(b) {
		return nil, errors.New("bad PKCS#7 padding")
	}
	for _, c := range b[len(b)-p:] {
		if int(c) != p {
			return nil, errors.New("bad PKCS#7 padding")
		}
	}
	return b[:len(b)-p], nil
}

func ecbCrypt(block cipher.Block, data []byte, enc bool) []byte {
	out := make([]byte, len(data))
	for off := 0; off+16 <= len(data); off += 16 {
		if enc {
			block.Encrypt(out[off:off+16], data[off:off+16])
		} else {
			block.Decrypt(out[off:off+16], data[off:off+16])
		}
	}
	return out
}

// ---------------------------------------------------------------- MP4 parsing

type mp4Box struct {
	typ    string
	start  int64
	size   int64
	hdrLen int64
	body   []byte
}

func parseBoxes(buf []byte, off, end int64, out *[]mp4Box, depth int) {
	for off+8 <= end {
		size := int64(binary.BigEndian.Uint32(buf[off:]))
		typ := string(buf[off+4 : off+8])
		hdr := int64(8)
		if size == 1 {
			if off+16 > end {
				return
			}
			size = int64(binary.BigEndian.Uint64(buf[off+8:]))
			hdr = 16
		} else if size == 0 {
			size = end - off
		}
		if size < 8 || off+size > end {
			return
		}
		*out = append(*out, mp4Box{typ: typ, start: off, size: size, hdrLen: hdr,
			body: buf[off+hdr : off+size]})
		if depth < 12 {
			switch typ {
			case "moov", "trak", "mdia", "minf", "stbl", "dinf", "edts":
				parseBoxes(buf, off+hdr, off+size, out, depth+1)
			case "stsd":
				if len(buf[off+hdr:off+size]) > 8 {
					parseBoxes(buf, off+hdr+8, off+size, out, depth+1)
				}
			case "mp4a", "wave", "enca":
				parseBoxes(buf, off+hdr+28, off+size, out, depth+1)
			}
		}
		off += size
	}
}

func firstBox(all []mp4Box, typ string) *mp4Box {
	for i := range all {
		if all[i].typ == typ {
			return &all[i]
		}
	}
	return nil
}

// esdsToDSI walks the MPEG-4 descriptor chain of an esds body (which starts
// with 4 version/flags bytes) and returns the decoder specific info.
func esdsToDSI(body []byte) []byte {
	if len(body) < 4 {
		return nil
	}
	b := body[4:]
	pos := 0
	var dsi []byte
	var walk func(end int)
	walk = func(end int) {
		for pos+2 <= end {
			tag := b[pos]
			pos++
			ln := 0
			for pos < end && b[pos]&0x80 != 0 {
				ln = ln<<7 | int(b[pos]&0x7f)
				pos++
			}
			if pos >= end {
				return
			}
			ln = ln<<7 | int(b[pos]&0x7f)
			pos++
			start := pos
			switch tag {
			case 3: // ES_Descriptor: ES_ID(2) + flags(1)
				pos += 3
				walk(end)
				continue
			case 4: // DecoderConfigDescriptor (13 bytes + optional children;
				// both FFmpeg and Apple nest the DSI inside it)
				pos += 13
				walk(start + ln)
				continue
			case 5: // DecoderSpecificInfo
				if start+ln <= len(b) {
					dsi = b[start : start+ln]
				}
			}
			pos = start + ln
		}
	}
	walk(len(b))
	return dsi
}

type bitReader struct {
	b   []byte
	pos int
	err bool
}

func (r *bitReader) bits(n int) uint32 {
	var v uint32
	for i := 0; i < n; i++ {
		bytePos := r.pos >> 3
		if bytePos >= len(r.b) {
			r.err = true
			return v
		}
		v = v<<1 | uint32((r.b[bytePos]>>(7-uint(r.pos&7)))&1)
		r.pos++
	}
	return v
}

// pceChannels returns the channel count of an AudioSpecificConfig: standard
// configurations 1..7 directly (7.1 counts 8 channels, the ffmpeg
// convention), otherwise by parsing the following PCE.
//
// The PCE bit layout follows ffmpeg's decoder (libavcodec aacdec
// decode_pce), which Engine's own stems files are made with and which this
// tool must agree with: num_assoc_data is 3 bits (the spec says 4) and the
// three mixdown flags are conditional 1-bit fields rather than a fixed
// 4-bit group. Getting these wrong shifts every following bit.
func pceChannels(dsi []byte) (int, bool) {
	if len(dsi) < 2 {
		return 0, false
	}
	cfg := int(dsi[1]>>3) & 0xF
	if cfg == 7 {
		return 8, true
	}
	if cfg != 0 {
		return cfg, true
	}
	if len(dsi) < 3 {
		return 0, false
	}
	br := &bitReader{b: dsi[2:]}
	br.bits(4) // element instance tag
	br.bits(2) // object type
	br.bits(4) // sampling frequency index
	front := int(br.bits(4))
	side := int(br.bits(4))
	back := int(br.bits(4))
	lfe := int(br.bits(2))
	br.bits(3) // assoc data elements
	br.bits(4) // cc elements
	if br.bits(1) == 1 {
		br.bits(4) // mono mixdown tag
	}
	if br.bits(1) == 1 {
		br.bits(4) // stereo mixdown tag
	}
	if br.bits(1) == 1 {
		br.bits(3) // matrix mixdown index + pseudo surround enable
	}
	ch := 0
	readElems := func(n int) {
		for i := 0; i < n && !br.err; i++ {
			cpe := br.bits(1) == 1
			br.bits(4) // tag
			if cpe {
				ch += 2
			} else {
				ch++
			}
		}
	}
	readElems(front)
	readElems(side)
	readElems(back)
	for i := 0; i < lfe && !br.err; i++ {
		br.bits(4)
		ch++
	}
	if br.err || ch == 0 {
		return 0, false
	}
	return ch, true
}

// ---------------------------------------------------------------- stems file

type stemsFile struct {
	Path           string
	Timescale      uint32
	DurationFrames uint64
	SamplesPerPkt  uint32
	Channels       int
	DSI            []byte
	EncSizes       []int
	Packets        [][]byte // decrypted, padding removed
	DSIIsFirstPkt  bool
	NumChunks      int
}

// OpenStems parses and decrypts a .stems file. It keeps the whole payload in
// memory (~the file size), which is fine for preview playback.
func OpenStems(path string) (*stemsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var boxes []mp4Box
	parseBoxes(data, 0, int64(len(data)), &boxes, 0)

	if firstBox(boxes, "moov") == nil {
		return nil, errors.New("no moov box: not a stems/MP4 file")
	}

	var dsi []byte
	var timescale, spp uint32 = 44100, 1024
	var duration uint64
	var sizes []int
	var chunkOffsets []int64
	var chunkRuns [][3]int64
	mdhdSeen := false

	for _, b := range boxes {
		if b.typ != "mdhd" || len(b.body) < 20 || b.body[0] != 0 {
			continue
		}
		timescale = binary.BigEndian.Uint32(b.body[12:])
		duration = uint64(binary.BigEndian.Uint32(b.body[16:]))
		mdhdSeen = true
	}
	for _, b := range boxes {
		if b.typ == "esds" {
			if d := esdsToDSI(b.body); len(d) > 0 {
				dsi = d
			}
		}
	}
	if b := firstBox(boxes, "stts"); b != nil && len(b.body) >= 16 {
		if n := int(binary.BigEndian.Uint32(b.body[4:])); n > 0 {
			spp = binary.BigEndian.Uint32(b.body[12:])
		}
	}
	if b := firstBox(boxes, "stsz"); b != nil && len(b.body) >= 12 {
		def := binary.BigEndian.Uint32(b.body[4:])
		n := int(binary.BigEndian.Uint32(b.body[8:]))
		if def != 0 {
			sizes = make([]int, n)
		} else if len(b.body) >= 12+4*n {
			sizes = make([]int, n)
			for i := 0; i < n; i++ {
				sizes[i] = int(binary.BigEndian.Uint32(b.body[12+4*i:]))
			}
		}
	}
	if b := firstBox(boxes, "stsc"); b != nil && len(b.body) >= 8 {
		n := int(binary.BigEndian.Uint32(b.body[4:]))
		for i := 0; i < n && len(b.body) >= 8+12*(i+1); i++ {
			a := binary.BigEndian.Uint32(b.body[8+12*i:])
			per := binary.BigEndian.Uint32(b.body[12+12*i:])
			chunkRuns = append(chunkRuns, [3]int64{int64(a), int64(per), 0})
		}
	}
	if b := firstBox(boxes, "stco"); b != nil && len(b.body) >= 8 {
		n := int(binary.BigEndian.Uint32(b.body[4:]))
		for i := 0; i < n && len(b.body) >= 8+4*(i+1); i++ {
			chunkOffsets = append(chunkOffsets, int64(binary.BigEndian.Uint32(b.body[8+4*i:])))
		}
	}
	if len(sizes) == 0 || len(chunkOffsets) == 0 || len(chunkRuns) == 0 || !mdhdSeen {
		return nil, errors.New("missing sample tables (mdhd/stsz/stsc/stco)")
	}

	var mdatStart int64 = -1
	if m := firstBox(boxes, "mdat"); m != nil {
		mdatStart = m.start + m.hdrLen
	}
	type pktRef struct{ off, size int64 }
	var refs []pktRef
	sample := 0
	for ci, run := range chunkRuns {
		last := int64(len(chunkOffsets))
		if ci+1 < len(chunkRuns) {
			last = chunkRuns[ci+1][0] - 1
		}
		for chunk := run[0]; chunk <= last && chunk <= int64(len(chunkOffsets)); chunk++ {
			off := chunkOffsets[chunk-1]
			for s := int64(0); s < run[1] && sample < len(sizes); s++ {
				refs = append(refs, pktRef{off, int64(sizes[sample])})
				off += int64(sizes[sample])
				sample++
			}
		}
	}
	if sample < len(sizes) {
		refs = refs[:sample]
	}
	if mdatStart >= 0 && len(refs) > 0 && refs[0].off < mdatStart {
		mdatStart = refs[0].off
	}

	block, err := stemsCipher()
	if err != nil {
		return nil, err
	}
	f := &stemsFile{Path: path, Timescale: timescale,
		DurationFrames: duration, SamplesPerPkt: spp, DSI: dsi,
		EncSizes: sizes, NumChunks: len(chunkOffsets)}

	for _, r := range refs {
		if r.off < 0 || r.off+r.size > int64(len(data)) {
			return nil, fmt.Errorf("packet out of range: %d..%d", r.off, r.off+r.size)
		}
		enc := data[r.off : r.off+r.size]
		if len(enc)%16 != 0 {
			return nil, fmt.Errorf("packet size %d not block aligned (wrong file?)", len(enc))
		}
		clear := ecbCrypt(block, enc, false)
		pkt, err := pkcs7Unpad(clear)
		if err != nil {
			return nil, fmt.Errorf("packet at %d: %w (wrong key or not a stems file?)", r.off, err)
		}
		f.Packets = append(f.Packets, pkt)
	}
	data = nil // the decrypted packets are copies; drop the raw file

	if len(f.Packets) > 0 && len(f.Packets[0]) >= 7 &&
		string(f.Packets[0][3:7]) == "Lavc" {
		f.DSIIsFirstPkt = true
	}
	if ch, ok := pceChannels(dsi); ok {
		f.Channels = ch
	} else {
		f.Channels = 2
	}
	return f, nil
}

// ---------------------------------------------------------------- box writer

func mkBox(typ string, parts ...[]byte) []byte {
	n := 8
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 8, n)
	binary.BigEndian.PutUint32(out, uint32(n))
	copy(out[4:], typ)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func mkFullBox(typ string, flags byte, parts ...[]byte) []byte {
	body := []byte{0, 0, 0, flags}
	for _, p := range parts {
		body = append(body, p...)
	}
	return mkBox(typ, body)
}

func u16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func u32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }

func writeDescriptor(t byte, payload []byte) []byte {
	out := []byte{t}
	ln := len(payload)
	out = append(out, byte(0x80|((ln>>21)&0x7f)), byte(0x80|((ln>>14)&0x7f)), byte(0x80|((ln>>7)&0x7f)), byte(ln&0x7f))
	return append(out, payload...)
}

// buildEsds builds an esds body in the style both Apple and FFmpeg write
// (DSI nested inside the DecoderConfigDescriptor).
func buildEsds(dsi []byte) []byte {
	dsiDesc := writeDescriptor(5, dsi)
	dcd := append([]byte{0x40, 0x15, 0, 0, 0}, make([]byte, 8)...)
	dcd = append(dcd, dsiDesc...)
	es := append([]byte{0x00, 0x01, 0x00}, writeDescriptor(4, dcd)...)
	es = append(es, writeDescriptor(6, []byte{0x02})...)
	return append([]byte{0, 0, 0, 0}, writeDescriptor(3, es)...)
}

// writeStemsMP4 remuxes decrypted AAC packets into a clear (unencrypted)
// faststart MP4 (ftyp + moov + mdat) whose esds carries dsi. Streaming that
// to ffmpeg is how the packets get decoded without cgo. moov comes first so
// ffmpeg can read the stream from a pipe.
func writeStemsMP4(w io.Writer, dsi []byte, timescale, spp uint32, channels int, packets [][]byte) error {
	ftyp := mkBox("ftyp", []byte("isom"), u32(0x200), []byte("isom"), []byte("iso2"), []byte("mp41"))
	esds := mkBox("esds", buildEsds(dsi))
	mp4a := mkBox("mp4a",
		[]byte{0, 0, 0, 0, 0, 0}, u16(1),
		[]byte{0, 0, 0, 0, 0, 0, 0, 0},
		u16(uint16(channels)), u16(16), u16(0), u16(0),
		u32(timescale<<16), esds)
	stsd := mkFullBox("stsd", 0, u32(1), mp4a)
	durPkts := uint32(len(packets))
	stts := mkFullBox("stts", 0, u32(1), u32(durPkts), u32(spp))
	stsc := mkFullBox("stsc", 0, u32(1), u32(1), u32(durPkts), u32(1))
	dinf := mkBox("dinf", mkFullBox("dref", 0, u32(1), mkFullBox("url ", 1)))
	smhd := mkFullBox("smhd", 0, u16(0), u16(0))
	hdlr := mkFullBox("hdlr", 0, make([]byte, 4), []byte("soun"), make([]byte, 12), append([]byte("SoundHandler"), 0))
	mdhd := mkFullBox("mdhd", 0, u32(0), u32(0), u32(timescale), u32(uint32(uint64(spp)*uint64(durPkts))), u16(0x55c4), u16(0))

	durationTicks := uint64(spp) * uint64(durPkts)
	buildMoov := func(chunkOffset uint32) []byte {
		stco := mkFullBox("stco", 0, u32(1), u32(chunkOffset))
		body := []byte{0, 0, 0, 0} // stsz: version/flags
		body = append(body, u32(0)...)
		body = append(body, u32(durPkts)...)
		for _, p := range packets {
			body = append(body, u32(uint32(len(p)))...)
		}
		stsz := mkBox("stsz", body)
		stbl := mkBox("stbl", stsd, stts, stsc, stsz, stco)
		minf := mkBox("minf", smhd, dinf, stbl)
		mdia := mkBox("mdia", mdhd, hdlr, minf)
		tkhd := mkFullBox("tkhd", 7,
			u32(0), u32(0), u32(1), u32(0), u32(uint32(durationTicks)),
			make([]byte, 8), u16(0), u16(0), u16(0x0100), u16(0),
			u32(0x10000), u32(0), u32(0),
			u32(0), u32(0x10000), u32(0),
			u32(0), u32(0), u32(0x40000000),
			u32(0), u32(0))
		trak := mkBox("trak", tkhd, mdia)
		mvhd := mkFullBox("mvhd", 0,
			u32(0), u32(0), u32(1000), u32(uint32(durationTicks*1000/uint64(timescale))),
			u32(0x00010000), u16(0x0100), make([]byte, 10),
			u32(0x10000), u32(0), u32(0),
			u32(0), u32(0x10000), u32(0),
			u32(0), u32(0), u32(0x40000000),
			make([]byte, 24), u32(2))
		return mkBox("moov", mvhd, trak)
	}

	moov := buildMoov(0)
	mdatPayloadOffset := uint32(len(ftyp) + len(moov) + 8)
	moov = buildMoov(mdatPayloadOffset)

	payloadSize := 0
	for _, p := range packets {
		payloadSize += len(p)
	}
	mdat := make([]byte, 8)
	binary.BigEndian.PutUint32(mdat, uint32(8+payloadSize))
	copy(mdat[4:], "mdat")

	if _, err := w.Write(ftyp); err != nil {
		return err
	}
	if _, err := w.Write(moov); err != nil {
		return err
	}
	if _, err := w.Write(mdat); err != nil {
		return err
	}
	for _, p := range packets {
		if _, err := w.Write(p); err != nil {
			return err
		}
	}
	return nil
}

// writeStems remuxes decrypted AAC packets into an Engine-style encrypted
// .stems file: ftyp + free + mdat + moov (used by tests to build synthetic
// stems containers).
func writeStems(path string, dsi []byte, timescale, spp uint32, channels int, packets [][]byte) error {
	block, err := stemsCipher()
	if err != nil {
		return err
	}
	var payload []byte
	for _, p := range packets {
		payload = append(payload, ecbCrypt(block, pkcs7Pad(p), true)...)
	}

	ftyp := mkBox("ftyp", []byte("isom"), u32(0x200), []byte("isom"), []byte("iso2"), []byte("mp41"))
	free := mkBox("free")
	esds := mkBox("esds", buildEsds(dsi))
	mp4a := mkBox("mp4a",
		[]byte{0, 0, 0, 0, 0, 0}, u16(1),
		[]byte{0, 0, 0, 0, 0, 0, 0, 0},
		u16(uint16(channels)), u16(16), u16(0), u16(0),
		u32(timescale<<16), esds)
	stsd := mkFullBox("stsd", 0, u32(1), mp4a)
	durPkts := uint32(len(packets))
	stts := mkFullBox("stts", 0, u32(1), u32(durPkts), u32(spp))
	stsc := mkFullBox("stsc", 0, u32(1), u32(1), u32(durPkts), u32(1))
	sz := []byte{0, 0, 0, 0}
	sz = append(sz, u32(0)...)
	sz = append(sz, u32(durPkts)...)
	for _, p := range packets {
		sz = append(sz, u32(uint32(len(pkcs7Pad(p))))...)
	}
	stsz := mkBox("stsz", sz)
	dinf := mkBox("dinf", mkFullBox("dref", 0, u32(1), mkFullBox("url ", 1)))
	smhd := mkFullBox("smhd", 0, u16(0), u16(0))
	hdlr := mkFullBox("hdlr", 0, make([]byte, 4), []byte("soun"), make([]byte, 12), append([]byte("SoundHandler"), 0))
	mdhd := mkFullBox("mdhd", 0, u32(0), u32(0), u32(timescale), u32(uint32(uint64(spp)*uint64(durPkts))), u16(0x55c4), u16(0))

	durationTicks := uint64(spp) * uint64(durPkts)
	// Edit list skipping the AAC encoder priming (one frame of 1024
	// samples), like Engine's own stems files: without it the decoded
	// audio starts ~23 ms late relative to the track.
	segmentMs := uint32(uint64(spp) * uint64(durPkts) * 1000 / uint64(timescale))
	elst := mkFullBox("elst", 0, u32(1), u32(segmentMs), u32(1024), u32(0x10000))
	edts := mkBox("edts", elst)
	buildMoov := func(chunkOffset uint32) []byte {
		stco := mkFullBox("stco", 0, u32(1), u32(chunkOffset))
		stbl := mkBox("stbl", stsd, stts, stsc, stsz, stco)
		minf := mkBox("minf", smhd, dinf, stbl)
		mdia := mkBox("mdia", mdhd, hdlr, minf)
		tkhd := mkFullBox("tkhd", 7,
			u32(0), u32(0), u32(1), u32(0), u32(uint32(durationTicks)),
			make([]byte, 8), u16(0), u16(0), u16(0x0100), u16(0),
			u32(0x10000), u32(0), u32(0),
			u32(0), u32(0x10000), u32(0),
			u32(0), u32(0), u32(0x40000000),
			u32(0), u32(0))
		trak := mkBox("trak", tkhd, edts, mdia)
		mvhd := mkFullBox("mvhd", 0,
			u32(0), u32(0), u32(1000), u32(uint32(durationTicks*1000/uint64(timescale))),
			u32(0x00010000), u16(0x0100), make([]byte, 10),
			u32(0x10000), u32(0), u32(0),
			u32(0), u32(0x10000), u32(0),
			u32(0), u32(0), u32(0x40000000),
			make([]byte, 24), u32(2))
		return mkBox("moov", mvhd, trak)
	}

	moov := buildMoov(0)
	mdatPayloadOffset := uint32(len(ftyp) + len(free) + 8)
	moov = buildMoov(mdatPayloadOffset)
	mdat := mkBox("mdat", payload)

	w, err := os.Create(path)
	if err != nil {
		return err
	}
	defer w.Close()
	for _, part := range [][]byte{ftyp, free, mdat, moov} {
		if _, err := w.Write(part); err != nil {
			return err
		}
	}
	return w.Sync()
}

// ---------------------------------------------------------------- ffmpeg AAC decode

// stemsDecoder decodes decrypted AAC packets to interleaved float32 PCM by
// remuxing them into a clear MP4 that is streamed into an ffmpeg subprocess.
// The output is channels-channel f32le at outRate (ffmpeg resamples).
type stemsDecoder struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	wg     sync.WaitGroup // the stdin feed goroutine
	closed atomic.Bool
	err    error // write-side error, surfaced by Close
}

// newStemsDecoder starts decoding packets[from:] (from may be 0; the
// extradata carrier packet, when present, must be excluded by the caller).
func newStemsDecoder(f *stemsFile, from int, outRate int) (*stemsDecoder, error) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, errors.New("stems playback needs ffmpeg on the PATH")
	}
	if from < 0 || from >= len(f.Packets) {
		return nil, errors.New("stems: decode start out of range")
	}
	pkts := f.Packets[from:]

	cmd := exec.Command(ffmpeg,
		"-v", "error", "-nostdin",
		"-i", "pipe:0",
		"-f", "f32le", "-ac", fmt.Sprint(f.Channels), "-ar", fmt.Sprint(outRate),
		"-c:a", "pcm_f32le",
		"pipe:1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("ffmpeg: %w", err)
	}
	d := &stemsDecoder{cmd: cmd, stdin: stdin, stdout: stdout}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		werr := writeStemsMP4(stdin, f.DSI, f.Timescale, f.SamplesPerPkt, f.Channels, pkts)
		stdin.Close() // EOF: ffmpeg decodes what it got and exits
		d.err = werr
	}()
	return d, nil
}

// Read pulls the next chunk of decoded interleaved float32 PCM.
func (d *stemsDecoder) Read(pcm []float32) (int, error) {
	// f32le: 4 bytes per sample
	b := make([]byte, len(pcm)*4)
	n, err := io.ReadFull(d.stdout, b)
	if n == 0 {
		return 0, err
	}
	samples := n / 4
	for i := 0; i < samples; i++ {
		pcm[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return samples, err
}

// Close kills the decoder process.
func (d *stemsDecoder) Close() {
	if d.closed.CompareAndSwap(false, true) {
		d.stdin.Close()
		d.cmd.Process.Kill()
	}
	d.wg.Wait()
	d.cmd.Wait()
}
