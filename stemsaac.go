package main

// stemsaac.go: turn four stereo stem WAVs into Engine-compatible 8-channel
// AAC packets for a .stems container.
//
// Each stem is encoded independently as stereo AAC (ffmpeg, ADTS output).
// Every ADTS frame holds one channel-pair element (CPE); the four stems'
// elements are concatenated bit-exactly into a single 8-channel packet per
// frame, with the element tags rewritten to 0..3. A program-config-element
// AudioSpecificConfig (see buildStemsDSI) declares exactly that element
// sequence, so any decoder — Engine's, ffmpeg's — maps the decoded channels
// back in element order: stem i lands on channel pair 2i/2i+1, i.e. decoded
// pairs (0,1), (2,3), (4,5), (6,7) = Vocals, Bass, Drums, Other, which is the
// layout of Engine's own stems files.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// sumStems mixes the listed stem WAVs (same folder) additively into
// <folder>/<out>.wav — a plain sum, no normalization. Used to build the
// "Other" stem as the remaining part of the original mix.
func sumStems(folder string, names []string, out string) error {
	args := []string{"-v", "error", "-y"}
	for _, n := range names {
		args = append(args, "-i", filepath.Join(folder, n+".wav"))
	}
	inputs := ""
	for i := range names {
		inputs += fmt.Sprintf("[%d:a]", i)
	}
	args = append(args,
		"-filter_complex", inputs+fmt.Sprintf("amix=inputs=%d:normalize=0[a]", len(names)),
		"-map", "[a]", "-c:a", "pcm_f32le",
		filepath.Join(folder, out+".wav"))
	if outBytes, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg amix: %v: %s", err, outBytes)
	}
	return nil
}

// encodeStemAAC encodes one stereo stem WAV into stereo AAC-LC ADTS at
// 44100 Hz (Engine's stems rate). 160 kbps per pair matches the per-stem
// bitrate of Engine's own stems files.
func encodeStemAAC(wavPath, adtsPath string) error {
	cmd := exec.Command("ffmpeg", "-v", "error", "-y",
		"-i", wavPath,
		"-ac", "2", "-ar", "44100",
		"-c:a", "aac", "-b:a", "160k",
		"-f", "adts", adtsPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %v: %s", err, out)
	}
	return nil
}

// parseADTSFrames splits an ADTS stream into raw AAC frames (the bytes after
// each ADTS header, CRC absent).
func parseADTSFrames(data []byte) ([][]byte, error) {
	var frames [][]byte
	off := 0
	for off < len(data) {
		if off+7 > len(data) {
			break // trailing bytes
		}
		if data[off] != 0xFF || data[off+1]&0xF0 != 0xF0 {
			return nil, errors.New("not an ADTS stream")
		}
		length := int(data[off+3]&0x03)<<11 | int(data[off+4])<<3 | int(data[off+5])>>5
		if length < 7 {
			return nil, errors.New("bad ADTS frame length")
		}
		hdr := 7
		if data[off+1]&0x01 == 0 { // protection_absent clear: 2-byte CRC present
			hdr = 9
		}
		if off+length > len(data) {
			return nil, errors.New("truncated ADTS frame")
		}
		frames = append(frames, data[off+hdr:off+length])
		off += length
	}
	if len(frames) == 0 {
		return nil, errors.New("no AAC frames in ADTS stream")
	}
	return frames, nil
}

// cpeElementBits extracts the channel-pair element of one stereo AAC frame
// as a bit slice, skipping FIL (fill) elements, and rewrites the element
// instance tag. The trailing END element and byte padding are dropped; the
// caller concatenates several elements and appends a single END.
func cpeElementBits(raw []byte, tag int) ([]byte, error) {
	bits := make([]byte, 0, len(raw)*8)
	for _, b := range raw {
		for i := 7; i >= 0; i-- {
			bits = append(bits, (b>>uint(i))&1)
		}
	}
	// The END element (id 7 = '111') is the last element of the block; its
	// final bit is the last set bit in the frame.
	last := -1
	for i := len(bits) - 1; i >= 0; i-- {
		if bits[i] == 1 {
			last = i
			break
		}
	}
	if last < 3 {
		return nil, errors.New("empty AAC frame")
	}
	audioEnd := last - 2 // bits [audioEnd:] are the END element
	pos := 0
	rd := func(n int) int {
		v := 0
		for k := 0; k < n; k++ {
			if pos >= len(bits) {
				return v
			}
			v = v<<1 | int(bits[pos])
			pos++
		}
		return v
	}
	for {
		if pos > audioEnd {
			return nil, errors.New("no audio element in AAC frame")
		}
		el := rd(3)
		switch el {
		case 6: // FIL extension: skip its payload
			cnt := rd(4)
			if cnt == 15 {
				cnt += rd(8) - 1
			}
			pos += cnt * 8
		case 1: // CPE: the frame's audio element
			start := pos - 3
			elem := append([]byte{}, bits[start:audioEnd]...)
			if len(elem) < 7 {
				return nil, errors.New("AAC CPE element too short")
			}
			for k := 0; k < 4; k++ {
				elem[3+k] = byte((tag >> uint(3-k)) & 1)
			}
			return elem, nil
		case 0:
			return nil, errors.New("unexpected SCE element in a stereo stream")
		default:
			return nil, fmt.Errorf("unexpected AAC element id %d", el)
		}
	}
}

// assembleStemsPackets reads the per-stem ADTS files (in Engine stem order:
// vocals, bass, drums, other) and merges them into one 8-channel packet per
// frame: four CPE elements with tags 0..3 plus one END element.
func assembleStemsPackets(folder string, names []string) ([][]byte, error) {
	var streams [][][]byte
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(folder, name+".adts"))
		if err != nil {
			return nil, err
		}
		frames, err := parseADTSFrames(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		streams = append(streams, frames)
	}
	n := len(streams[0])
	for _, s := range streams[1:] {
		if len(s) < n {
			n = len(s) // keep all stems frame-aligned
		}
	}
	var packets [][]byte
	bits := make([]byte, 0, 8*4*1600)
	for f := 0; f < n; f++ {
		bits = bits[:0]
		for tag, s := range streams {
			el, err := cpeElementBits(s[f], tag)
			if err != nil {
				return nil, fmt.Errorf("%s frame %d: %w", names[tag], f, err)
			}
			bits = append(bits, el...)
		}
		bits = append(bits, 1, 1, 1) // END element
		packets = append(packets, packBits(bits))
	}
	return packets, nil
}

// packBits packs a 0/1-per-byte bit slice into bytes (zero padded).
func packBits(bits []byte) []byte {
	out := make([]byte, (len(bits)+7)/8)
	for i, b := range bits {
		if b != 0 {
			out[i/8] |= 1 << uint(7-i%8)
		}
	}
	return out
}

// aacSampleRateIndex maps a sample rate to its AAC sampling frequency index.
func aacSampleRateIndex(rate int) (int, error) {
	switch rate {
	case 96000:
		return 0, nil
	case 88200:
		return 1, nil
	case 64000:
		return 2, nil
	case 48000:
		return 3, nil
	case 44100:
		return 4, nil
	case 32000:
		return 5, nil
	case 24000:
		return 6, nil
	case 22050:
		return 7, nil
	case 16000:
		return 8, nil
	case 12000:
		return 9, nil
	case 11025:
		return 10, nil
	case 8000:
		return 11, nil
	case 7350:
		return 12, nil
	}
	return 0, fmt.Errorf("unsupported AAC sample rate %d", rate)
}

// buildStemsDSI builds the AudioSpecificConfig carried in the stems file's
// esds box: AAC-LC, the given sample rate, channel configuration 0 with a
// program config element declaring four stereo (CPE) pairs with tags 0..3,
// and the usual sync extension marking the stream as plain LC (no SBR).
//
// The PCE bit layout follows ffmpeg's AAC decoder (decode_pce), which
// Engine's own stems files are written with: num_assoc_data occupies 3 bits
// (the spec's table says 4) and the mixdown flags are three conditional
// 1-bit fields. Every bit after them shifts otherwise.
func buildStemsDSI(sampleRate int) ([]byte, error) {
	idx, err := aacSampleRateIndex(sampleRate)
	if err != nil {
		return nil, err
	}
	w := &bitWriter{}
	w.put(2, 5)   // audio object type: AAC-LC
	w.put(idx, 4) // sampling frequency index
	w.put(0, 4)   // channel configuration 0 → PCE follows
	w.put(0, 3)   // GASpecificConfig: frameLengthFlag, dependsOnCoreCoder, extensionFlag
	// program_config_element
	w.put(0, 4)   // element instance tag
	w.put(0, 2)   // object type
	w.put(idx, 4) // sampling frequency index
	w.put(4, 4)   // 4 front channel elements (the four stems as CPEs)
	w.put(0, 4)   // side channel elements
	w.put(0, 4)   // back channel elements
	w.put(0, 2)   // lfe elements
	w.put(0, 3)   // assoc data elements
	w.put(0, 4)   // valid cc elements
	w.put(0, 3)   // mono/stereo/matrix mixdown flags (all absent)
	for tag := 0; tag < 4; tag++ {
		w.put(1, 1) // front_element_is_cpe
		w.put(tag, 4)
	}
	w.align()        // byte_align() before the comment field
	w.put(0, 8)      // comment_field_bytes = 0
	w.put(0x2b7, 11) // syncExtension
	w.put(5, 5)      // extension audio object type: SBR
	w.put(0, 1)      // sbr = 0: plain AAC-LC
	w.align()
	return w.pack(), nil
}

// bitWriter accumulates bits (0/1 per byte) — clarity over speed, these are
// a few hundred bits per file.
type bitWriter struct{ bits []byte }

func (w *bitWriter) put(v, n int) {
	for i := n - 1; i >= 0; i-- {
		w.bits = append(w.bits, byte((v>>uint(i))&1))
	}
}

func (w *bitWriter) align() {
	for len(w.bits)%8 != 0 {
		w.bits = append(w.bits, 0)
	}
}

func (w *bitWriter) pack() []byte { return packBits(w.bits) }
