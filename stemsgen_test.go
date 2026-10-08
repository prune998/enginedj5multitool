package main

// stemsgen_test.go: tests for the stems generation audio pipeline
// (stemsaac.go): ADTS parsing, CPE element assembly, and the crafted
// program-config-element DSI. The full pipeline test (4 stereo WAVs in →
// encrypted .stems out → decoded 8 channels) needs ffmpeg on the PATH, like
// the other stems decode tests.

import (
	"encoding/hex"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBuildStemsDSI(t *testing.T) {
	dsi, err := buildStemsDSI(44100)
	if err != nil {
		t.Fatal(err)
	}
	// The tool's own parser and the real DSI bytes must agree.
	if cfg := int(dsi[1]>>3) & 0xF; cfg != 0 {
		t.Fatalf("channel configuration = %d, want 0 (PCE)", cfg)
	}
	ch, ok := pceChannels(dsi)
	if !ok {
		t.Fatal("pceChannels could not parse the crafted DSI")
	}
	if ch != 8 {
		t.Fatalf("pceChannels = %d, want 8", ch)
	}
	// ASC head: AOT 2 (AAC-LC), sampling index 4 (44100) — first byte
	// 0b00010_010 → 0x12, matching the layout of Engine's own files.
	if dsi[0] != 0x12 {
		t.Errorf("dsi[0] = %#x, want 0x12 (AOT 2, 44100 Hz)", dsi[0])
	}
	if !hasSuffixBytes(dsi, []byte{0x56, 0xe5, 0x00}) {
		t.Errorf("DSI lacks the syncExtension suffix: %x", dsi)
	}
}

func hasSuffixBytes(dsi, suffix []byte) bool {
	if len(dsi) < len(suffix) {
		return false
	}
	for i := range suffix {
		if dsi[len(dsi)-len(suffix)+i] != suffix[i] {
			return false
		}
	}
	return true
}

// TestPCEChannelsEngineDSI pins the PCE parser against the AudioSpecificConfig
// of a real Engine DJ stems file (AES payload aside, captured verbatim): AAC-LC
// at 44100 Hz, channel configuration 0 with a PCE describing five elements
// (front CPE+SCE, side CPE, back CPE+SCE — eight channels, with the stems
// laid across element boundaries). ffmpeg's writer produced it, so the
// parser must agree with ffmpeg's reader.
func TestPCEChannelsEngineDSI(t *testing.T) {
	dsi, err := hex.DecodeString("1200050848002008c8200e4c61766335382e3133342e31303056e500")
	if err != nil {
		t.Fatal(err)
	}
	ch, ok := pceChannels(dsi)
	if !ok {
		t.Fatal("pceChannels could not parse the Engine DSI")
	}
	if ch != 8 {
		t.Fatalf("pceChannels(Engine DSI) = %d, want 8", ch)
	}
}

func TestCPElementBitsRetags(t *testing.T) {
	// A synthetic CPE frame: id 001, tag 0000, filler, END.
	raw := []byte{0b0010_0000, 0x11, 0x22, 0b0111_0000}
	el, err := cpeElementBits(raw, 3)
	if err != nil {
		t.Fatal(err)
	}
	// Element bits: id 001, tag 0011 (3), then the filler minus the END,
	// packed and zero padded.
	got := packBits(el)
	want := []byte{0b0010_0110, 0x11, 0x22, 0x00}
	if len(got) != len(want) {
		t.Fatalf("element = %d bits (% x), want %d bytes", len(el), got, len(want))
	}
	for i, b := range want {
		if got[i] != b {
			t.Fatalf("element bytes = % x, want % x (byte %d: %08b, want %08b)", got, want, i, got[i], b)
		}
	}
}

func TestParseADTSFramesRejectsGarbage(t *testing.T) {
	if _, err := parseADTSFrames([]byte{1, 2, 3, 4, 5, 6, 7, 8}); err == nil {
		t.Fatal("expected an error for non-ADTS data")
	}
}

// TestStemsGenPipeline runs the generation audio path end to end without a
// server: four stereo tone WAVs (a different tone per channel) → AAC encode
// → packet assembly → encrypted .stems → decode → the channel layout must
// put stem i on channel pair 2i/2i+1 with its tones intact.
func TestStemsGenPipeline(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("stems generation tests need ffmpeg on the PATH")
	}
	if !StemsKeyConfigured() {
		if err := SetStemsKey("000102030405060708090a0b0c0d0e0f"); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()

	// Four stems, each a stereo pair of distinct tones. The tones sit low
	// enough to survive 160 kbps AAC cleanly.
	const rate = 44100
	const seconds = 1.0
	type stem struct {
		name string
		l, r float64
	}
	stems := []stem{
		{"vocals", 440, 494},
		{"bass", 587, 659},
		{"drums", 740, 831},
		{"other", 880, 988},
	}
	for _, s := range stems {
		frames := make([][2]float64, int(rate*seconds))
		for i := range frames {
			t := float64(i) / rate
			frames[i] = [2]float64{
				0.4 * math.Sin(2*math.Pi*s.l*t),
				0.4 * math.Sin(2*math.Pi*s.r*t),
			}
		}
		writeStemsGenWAV(filepath.Join(dir, s.name+".wav"), frames, rate)
		if err := encodeStemAAC(filepath.Join(dir, s.name+".wav"), filepath.Join(dir, s.name+".adts")); err != nil {
			t.Fatalf("encode %s: %v", s.name, err)
		}
	}

	packets, err := assembleStemsPackets(dir, []string{"vocals", "bass", "drums", "other"})
	if err != nil {
		t.Fatal(err)
	}
	// 1 s at 44100: ~43 full 1024-sample frames; the encoder pads the tail,
	// so allow a frame or two of slack in both directions.
	if len(packets) < 41 || len(packets) > 47 {
		t.Fatalf("assembled %d packets, want ~43", len(packets))
	}
	dsi, err := buildStemsDSI(rate)
	if err != nil {
		t.Fatal(err)
	}
	stemsPath := filepath.Join(dir, "1 test-uuid.stems")
	if err := writeStems(stemsPath, dsi, rate, 1024, 8, packets); err != nil {
		t.Fatal(err)
	}

	// Round-trip: open the encrypted file, decode it the way the player
	// does, and check each channel pair for the right tones.
	f, err := OpenStems(stemsPath)
	if err != nil {
		t.Fatal(err)
	}
	if f.Channels != 8 {
		t.Fatalf("channels = %d, want 8", f.Channels)
	}
	dec, err := newStemsDecoder(f, 0, rate)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	var pcm []float32
	buf := make([]float32, 8192)
	for {
		n, err := dec.Read(buf)
		pcm = append(pcm, buf[:n]...)
		if err != nil {
			break
		}
	}
	frames := len(pcm) / 8
	if frames < len(packets)*1024*9/10 {
		t.Fatalf("decoded %d frames, want ~%d", frames, len(packets)*1024)
	}

	// Goertzel magnitude of each decoded channel at each expected tone.
	expect := [][2]float64{ // per channel: {expected freq, other-channel freq}
		{440, 494}, {494, 440},
		{587, 659}, {659, 587},
		{740, 831}, {831, 740},
		{880, 988}, {988, 880},
	}
	goertzel := func(ch int, freq float64) float64 {
		start := frames / 4 // skip the encoder priming region
		n := frames / 2
		var coeff = 2 * math.Cos(2*math.Pi*freq/rate)
		var s1, s2 float64
		for i := start; i < start+n; i++ {
			v := float64(pcm[i*8+ch])
			s0 := v + coeff*s1 - s2
			s2, s1 = s1, s0
		}
		return math.Sqrt(s1*s1 + s2*s2 - coeff*s1*s2)
	}
	for ch, pair := range expect {
		wantMag := goertzel(ch, pair[0])
		otherMag := goertzel(ch, pair[1])
		if wantMag < 1000 {
			t.Errorf("ch%d: magnitude at %.0f Hz = %.0f, want > 1000 (tone missing or faint)", ch, pair[0], wantMag)
		}
		if otherMag > wantMag/10 {
			t.Errorf("ch%d: magnitude at %.0f Hz = %.0f, want < 1/10 of %.0f (wrong tone in channel)", ch, pair[1], otherMag, wantMag)
		}
	}
}

// writeStemsGenWAV writes a stereo 16-bit PCM WAV at the given rate (the
// test twin of encodeStemAAC's input format).
func writeStemsGenWAV(path string, frames [][2]float64, rate int) {
	var pcm []byte
	for _, f := range frames {
		for _, v := range f {
			x := int16(clamp(v*32768, -32768, 32767))
			pcm = append(pcm, byte(x), byte(x>>8))
		}
	}
	var hdr [44]byte
	copy(hdr[0:4], "RIFF")
	putLE32(hdr[4:8], uint32(36+len(pcm)))
	copy(hdr[8:12], "WAVE")
	copy(hdr[12:16], "fmt ")
	putLE32(hdr[16:20], 16)
	putLE16(hdr[20:22], 1)
	putLE16(hdr[22:24], 2)
	putLE32(hdr[24:28], uint32(rate))
	putLE32(hdr[28:32], uint32(rate*4))
	putLE16(hdr[32:34], 4)
	putLE16(hdr[34:36], 16)
	copy(hdr[36:40], "data")
	putLE32(hdr[40:44], uint32(len(pcm)))
	if err := os.WriteFile(path, append(hdr[:], pcm...), 0o644); err != nil {
		panic(err)
	}
}

func putLE16(b []byte, v uint16) { b[0] = byte(v); b[1] = byte(v >> 8) }
func putLE32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}
