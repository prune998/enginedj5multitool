package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	. "go.hasen.dev/shirei"
	"go.hasen.dev/shirei/drive"
)

// The real stems payload key is a secret and never appears in the tests;
// synthetic round-trip fixtures only need any 16-byte key.
func TestMain(m *testing.M) {
	if err := SetStemsKey("000102030405060708090a0b0c0d0e0f"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

const testKeyHex = "f0f1f2f3f4f5f6f7f8f9fafbfcfdfeff"

// restoreTestKey puts the TestMain dummy key back after a test played with
// the global.
func restoreTestKey(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if err := SetStemsKey("000102030405060708090a0b0c0d0e0f"); err != nil {
			panic(err)
		}
	})
}

// TestLoadStemsKey covers the key resolution precedence: env var, then the
// gitignored stems_key file in the working directory, then the one in the
// user config directory (redirected to a temp dir via ENGINDJ5_CONFIG_DIR).
func TestLoadStemsKey(t *testing.T) {
	restoreTestKey(t)
	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "cfg")
	workDir := filepath.Join(tmp, "work")
	for _, d := range []string{cfgDir, workDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ENGINDJ5_CONFIG_DIR", cfgDir)
	t.Chdir(workDir)

	// nothing configured anywhere
	src, err := LoadStemsKey()
	if err != nil || src != "" {
		t.Fatalf("empty setup: src=%q err=%v, want \"\" nil", src, err)
	}

	// key file in the config directory
	if err := os.WriteFile(filepath.Join(cfgDir, StemsKeyFile), []byte(testKeyHex+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err = LoadStemsKey()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cfgDir, StemsKeyFile); src != want || !StemsKeyConfigured() {
		t.Fatalf("config-dir file: src=%q configured=%v, want %q true", src, StemsKeyConfigured(), want)
	}

	// the working-directory file wins over the config directory
	if err := os.WriteFile(filepath.Join(workDir, StemsKeyFile), []byte(strings.Repeat("00", 16)), 0o600); err != nil {
		t.Fatal(err)
	}
	src, _ = LoadStemsKey()
	if src != StemsKeyFile { // reported relative to the working directory
		t.Fatalf("workdir file: src=%q, want %q", src, StemsKeyFile)
	}

	// the environment variable beats both files
	t.Setenv(StemsKeyEnvVar, testKeyHex)
	src, err = LoadStemsKey()
	if err != nil || src != "env" {
		t.Fatalf("env var: src=%q err=%v, want \"env\" nil", src, err)
	}

	// a malformed key file is reported with its path
	os.Unsetenv(StemsKeyEnvVar)
	if err := os.WriteFile(filepath.Join(tmp, "work", StemsKeyFile), []byte("nothex"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = LoadStemsKey()
	if err == nil || !strings.Contains(err.Error(), StemsKeyFile) {
		t.Fatalf("malformed file: err=%v, want an error naming %s", err, StemsKeyFile)
	}
}

// buildStemsLibrary extends the playlist fixture with an Engine Library
// folder containing a Stems dir and the matching .stems file for track 5.
func buildStemsLibrary(t *testing.T) (*Library, string) {
	t.Helper()
	lib := buildPlaylistLibrary(t)
	root := t.TempDir()
	stemsDir := filepath.Join(root, "Stems")
	if err := os.MkdirAll(stemsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lib.EngineLibrary = root
	// The fixture's Information uuid is "test-uuid".
	stemsPath := filepath.Join(stemsDir, "1 test-uuid.stems")
	if err := os.WriteFile(stemsPath, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	return lib, stemsPath
}

func TestStemsDetection(t *testing.T) {
	lib, stemsPath := buildStemsLibrary(t)
	if !lib.HasStems(1) {
		t.Error("track 1 should have stems")
	}
	if lib.StemsFile(1) != stemsPath {
		t.Errorf("StemsFile = %q, want %q", lib.StemsFile(1), stemsPath)
	}
	if lib.HasStems(2) {
		t.Error("track 2 has no stems file")
	}
	if !lib.StemsAvailable() {
		t.Error("StemsAvailable should be true with EngineLibrary set")
	}
	lib.EngineLibrary = ""
	if lib.HasStems(1) {
		t.Error("no EngineLibrary → no stems")
	}
}

// writeTestWAV writes a 16-bit stereo PCM WAV with the given per-frame
// left/right samples.
func writeTestWAV(t *testing.T, path string, frames [][2]float64) {
	t.Helper()
	var pcm []byte
	for _, f := range frames {
		for _, v := range f {
			x := int16(clamp(v*32768, -32768, 32767))
			pcm = append(pcm, byte(x), byte(x>>8))
		}
	}
	hdr := make([]byte, 44)
	copy(hdr[0:4], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(36+len(pcm)))
	copy(hdr[8:12], "WAVE")
	copy(hdr[12:16], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:20], 16)
	binary.LittleEndian.PutUint16(hdr[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(hdr[22:24], 2) // stereo
	binary.LittleEndian.PutUint32(hdr[24:28], 48000)
	binary.LittleEndian.PutUint32(hdr[28:32], 48000*4)
	binary.LittleEndian.PutUint16(hdr[32:34], 4) // block align
	binary.LittleEndian.PutUint16(hdr[34:36], 16)
	copy(hdr[36:40], "data")
	binary.LittleEndian.PutUint32(hdr[40:44], uint32(len(pcm)))
	if err := os.WriteFile(path, append(hdr, pcm...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func TestReadWAVStereo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.wav")
	frames := [][2]float64{{0.25, -0.5}, {1, -1}, {0, 0}}
	writeTestWAV(t, path, frames)
	samples, rate, err := readWAVStereo(path)
	if err != nil {
		t.Fatal(err)
	}
	if rate != 48000 {
		t.Fatalf("rate = %d, want 48000", rate)
	}
	if len(samples) != 6 {
		t.Fatalf("samples = %d, want 6 (interleaved stereo)", len(samples))
	}
	for i, want := range []float64{0.25, -0.5, 1, -1, 0, 0} {
		if math.Abs(float64(samples[i])-want) > 1e-4 { // 16-bit quantization
			t.Errorf("sample %d = %v, want %v", i, samples[i], want)
		}
	}
}

// TestStemsContainerRoundTrip builds a synthetic .stems file, re-opens it
// and checks the decrypted packets survive the round trip.
func TestStemsContainerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "1 test-uuid.stems")
	dsi := []byte{0x11, 0x90} // AAC-LC, 48 kHz, stereo
	packets := [][]byte{
		bytes.Repeat([]byte{1}, 30),
		bytes.Repeat([]byte{2}, 47),
		bytes.Repeat([]byte{3}, 16),
	}
	if err := writeStems(path, dsi, 48000, 1024, 2, packets); err != nil {
		t.Fatal(err)
	}
	f, err := OpenStems(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Channels != 2 {
		t.Errorf("channels = %d, want 2", f.Channels)
	}
	if f.Timescale != 48000 {
		t.Errorf("timescale = %d, want 48000", f.Timescale)
	}
	if f.SamplesPerPkt != 1024 {
		t.Errorf("samples per packet = %d, want 1024", f.SamplesPerPkt)
	}
	if !bytes.Equal(f.DSI, dsi) {
		t.Errorf("dsi = % x, want % x", f.DSI, dsi)
	}
	if len(f.Packets) != len(packets) {
		t.Fatalf("packets = %d, want %d", len(f.Packets), len(packets))
	}
	for i := range packets {
		if !bytes.Equal(f.Packets[i], packets[i]) {
			t.Errorf("packet %d differs", i)
		}
	}
}

// TestDownmixStems checks the stem filter math on a synthetic 8-channel
// frame: each stem is a distinct stereo pair, the mask picks the sum.
func TestDownmixStems(t *testing.T) {
	pcm := []float32{0.5, 0.25, -0.5, -0.25, 0.1, 0.2, 0, 0}
	cases := []struct {
		mask   uint32
		l, r   float32
		frames int
	}{
		{0xf, 0.1, 0.2, 1},    // all four summed
		{0x1, 0.5, 0.25, 1},   // stem 1 solo
		{0x2, -0.5, -0.25, 1}, // stem 2 solo
		{0x4, 0.1, 0.2, 1},    // stem 3 solo
		{0x8, 0, 0, 1},        // stem 4 solo
		{0x5, 0.6, 0.45, 1},   // stems 1+3
		{0x0, 0, 0, 1},        // nothing
	}
	for _, c := range cases {
		out := downmixStems(pcm, 8, c.mask)
		if len(out) != c.frames*4 {
			t.Fatalf("mask %#x: out = %d bytes, want %d", c.mask, len(out), c.frames*4)
		}
		l := float32(int16(uint16(out[0])|uint16(out[1])<<8)) / 32768
		r := float32(int16(uint16(out[2])|uint16(out[3])<<8)) / 32768
		if math.Abs(float64(l-c.l)) > 1e-4 || math.Abs(float64(r-c.r)) > 1e-4 {
			t.Errorf("mask %#x: l=%v r=%v, want %v %v", c.mask, l, r, c.l, c.r)
		}
	}
	// a stereo source only ever uses stem 1
	out := downmixStems([]float32{0.5, -0.5}, 2, 0x2)
	l := float32(int16(uint16(out[0])|uint16(out[1])<<8)) / 32768
	if l != 0 {
		t.Errorf("stereo source with mask 2 should be silent, got %v", l)
	}
}

// adtsPackets strips ADTS framing and returns the raw AAC packets plus a
// synthesized AudioSpecificConfig from the first header.
func adtsPackets(data []byte) ([][]byte, []byte) {
	var pkts [][]byte
	var dsi []byte
	off := 0
	for off+7 <= len(data) {
		if data[off] != 0xFF || data[off+1]&0xF6 != 0xF0 {
			break
		}
		profile := int(data[off+2]>>6) & 3
		freqIdx := int(data[off+2]>>2) & 0xF
		chCfg := int(data[off+2]&1)<<2 | int(data[off+3]>>6)&3
		n := int(data[off+3]&3)<<11 | int(data[off+4])<<3 | int(data[off+5]>>5)
		if n < 7 || off+n > len(data) {
			break
		}
		if dsi == nil {
			// ASC: 5 bits AOT (profile+1), 4 bits frequency, 4 bits
			// channel config, 3 GASpecificConfig bits.
			dsi = []byte{byte(profile+1)<<3 | byte(freqIdx)>>1,
				byte(freqIdx)<<7 | byte(chCfg)<<3}
		}
		pkts = append(pkts, data[off+7:off+n])
		off += n
	}
	return pkts, dsi
}

// makeSineStems writes a synthetic .stems file holding an ffmpeg-encoded
// stereo sine (skipped when ffmpeg is unavailable).
func makeSineStems(t *testing.T, dir string, seconds float64) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("stems decode tests need ffmpeg on the PATH")
	}
	adts := filepath.Join(dir, "sine.adts")
	cmd := exec.Command(ffmpeg, "-v", "error",
		"-f", "lavfi", "-i",
		fmt.Sprintf("aevalsrc=0.5*sin(2*PI*440*t):s=48000:d=%v", seconds),
		"-ac", "2", "-c:a", "aac", "-b:a", "128k", "-f", "adts", adts)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg encode: %v\n%s", err, out)
	}
	data, err := os.ReadFile(adts)
	if err != nil {
		t.Fatal(err)
	}
	packets, dsi := adtsPackets(data)
	if len(packets) < 5 {
		t.Fatalf("only %d ADTS frames decoded", len(packets))
	}
	stemsPath := filepath.Join(dir, "1 test-uuid.stems")
	if err := writeStems(stemsPath, dsi, 48000, 1024, 2, packets); err != nil {
		t.Fatal(err)
	}
	return stemsPath
}

// TestStemsDecodeFFmpeg runs the full decode pipeline: encrypted container
// → decrypt → clear MP4 → ffmpeg → PCM.
func TestStemsDecodeFFmpeg(t *testing.T) {
	dir := t.TempDir()
	stemsPath := makeSineStems(t, dir, 0.5)
	f, err := OpenStems(stemsPath)
	if err != nil {
		t.Fatal(err)
	}
	if f.Channels != 2 {
		t.Fatalf("channels = %d, want 2", f.Channels)
	}
	dec, err := newStemsDecoder(f, 0, 48000)
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
	frames := len(pcm) / 2
	want := len(f.Packets) * 1024
	if frames < want*9/10 || frames > want*11/10 {
		t.Errorf("decoded %d frames, want ~%d", frames, want)
	}
	peak := float32(0)
	for _, v := range pcm {
		if v > peak {
			peak = v
		}
	}
	if peak < 0.1 {
		t.Errorf("decoded audio is silent (peak %v)", peak)
	}
}

// TestAudioPlayerRegularWAV drives the player on a plain WAV file.
func TestAudioPlayerRegularWAV(t *testing.T) {
	lib, _ := buildStemsLibrary(t)
	// Give track 1 a real WAV file to play.
	dir := t.TempDir()
	wav := filepath.Join(dir, "song.wav")
	frames := make([][2]float64, 4800) // 0.1s of stereo
	for i := range frames {
		frames[i] = [2]float64{0.25, 0.25}
	}
	writeTestWAV(t, wav, frames)
	if _, err := lib.DB.Exec(`UPDATE Track SET path = ? WHERE id = 1`, wav); err != nil {
		t.Fatal(err)
	}

	tracks, err := lib.Tracks("")
	if err != nil {
		t.Fatal(err)
	}
	p := NewAudioPlayer(tracks[0], wav, "") // no stems
	if p.HasStems() {
		t.Error("player without a stems file should not claim stems")
	}
	p.Play()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if src, playing := p.state(); src != nil && playing {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	src, playing := p.state()
	if src == nil || !playing {
		status, errMsg, _ := p.Status()
		t.Fatalf("src nil: %v playing: %v, err=%q status=%q", src == nil, playing, errMsg, status)
	}
	p.Stop()
	if _, playing := p.state(); playing {
		t.Error("player should be stopped after Stop")
	}
}

// TestAudioPlayerStemsPlayback drives the whole stems pipeline through the
// player: encrypted container → ffmpeg decode → stem filter → output.
func TestAudioPlayerStemsPlayback(t *testing.T) {
	dir := t.TempDir()
	stemsPath := makeSineStems(t, dir, 0.8)

	p := NewAudioPlayer(TrackRecord{}, "", stemsPath)
	if !p.HasStems() {
		t.Fatal("player should claim stems")
	}
	// Solo the second stem: with a stereo source that is silence, so
	// switch back to stem 1 before checking audio flows.
	p.SetStemMask(0x1)
	p.Play()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if src, playing := p.state(); src != nil && playing {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	src, playing := p.state()
	if src == nil || !playing {
		status, errMsg, _ := p.Status()
		t.Fatalf("src nil: %v playing: %v, err=%q status=%q", src == nil, playing, errMsg, status)
	}
	time.Sleep(400 * time.Millisecond)
	if pos := p.Position(); pos <= 0.05 {
		t.Errorf("playback position = %v, want it to advance", pos)
	} else if pos > 2 {
		t.Errorf("playback position = %v, pacing is off", pos)
	}
	p.Stop()
	if _, playing := p.state(); playing {
		t.Error("player should be stopped after Stop")
	}
}

// TestStemsSeek changes position during stems playback; the player should
// keep playing from a new offset.
func TestStemsSeek(t *testing.T) {
	dir := t.TempDir()
	stemsPath := makeSineStems(t, dir, 3)
	p := NewAudioPlayer(TrackRecord{}, "", stemsPath)
	p.Play()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if src, playing := p.state(); src != nil && playing {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	p.Seek(1.5)
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if pos := p.Position(); pos >= 1.4 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pos := p.Position(); pos < 1.4 {
		t.Errorf("position after seek = %v, want ≥ 1.4", pos)
	}
	p.Stop()
}

// TestStemsIconOpensPanel drives the app: the stem icon renders in the
// browser for a track with stems, and clicking it opens the stems panel
// for that track.
func TestStemsIconOpensPanel(t *testing.T) {
	if raceEnabled {
		t.Skip("drive harness races under -race (global shirei state)")
	}
	lib, _ := buildStemsLibrary(t)
	GetHost().WindowSize = Vec2{1800, 1000}
	a := NewApp(filepath.Join(lib.Dir, "m.db"))
	defer a.Close()
	a.lib.EngineLibrary = lib.EngineLibrary
	a.buildStemsSet()
	<-a.stemsScanDone
	if !a.stemsSet[1] {
		t.Fatal("stems set missing track 1")
	}

	port, err := drive.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	AcceptInputCommands(port)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				RunFrameFn(a.RootView)
				time.Sleep(8 * time.Millisecond)
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()
	time.Sleep(100 * time.Millisecond)

	if n, err := drive.Count(port, "stems-1"); err != nil || n < 1 {
		t.Fatalf("stem icon not rendered: count=%d err=%v", n, err)
	}
	if _, err := drive.Click(port, "stems-1"); err != nil {
		t.Fatalf("click stem icon: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	// The icon jumps to the MP3 Tags pane with the track selected — the
	// stems section lives in that pane.
	if a.ActiveTool != 1 {
		t.Fatalf("ActiveTool = %d, want 1 (MP3 Tags)", a.ActiveTool)
	}
	if a.Selected != 1 {
		t.Fatalf("Selected = %d, want 1", a.Selected)
	}
}

func TestResampleTo48k(t *testing.T) {
	// 44100 → 48000: 100 samples become ~108-109, endpoints preserved.
	in := make([]float32, 100)
	for i := range in {
		in[i] = float32(i)
	}
	rs := &resampler{rate: 44100}
	out := rs.process(in)
	if len(out) < 106 || len(out) > 110 {
		t.Fatalf("resampled length = %d, want ~108", len(out))
	}
	if out[0] != in[0] {
		t.Errorf("first sample = %v, want %v", out[0], in[0])
	}
	// Same-rate passthrough.
	rs2 := &resampler{rate: 48000}
	if got := rs2.process(in); len(got) != len(in) {
		t.Fatalf("48k passthrough changed length: %d", len(got))
	}
}

func TestStereoToS16(t *testing.T) {
	out := stereoToS16([]float32{0.25, -0.5, 2, -2})
	want := []int16{8192, -16384, 32767, -32768}
	for i, w := range want {
		got := int16(uint16(out[i*2]) | uint16(out[i*2+1])<<8)
		if got != w {
			t.Errorf("sample %d = %d, want %d", i, got, w)
		}
	}
}
