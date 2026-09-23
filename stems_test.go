package main

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	. "go.hasen.dev/shirei"
	"go.hasen.dev/shirei/drive"
)

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

func TestReadWAVMono(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.wav")
	frames := [][2]float64{{1, -1}, {0.5, -0.5}, {0, 0}}
	writeTestWAV(t, path, frames)
	samples, rate, err := readWAVMono(path)
	if err != nil {
		t.Fatal(err)
	}
	if rate != 48000 {
		t.Fatalf("rate = %d, want 48000", rate)
	}
	if len(samples) != 3 {
		t.Fatalf("samples = %d, want 3", len(samples))
	}
	for i, want := range []float64{0, 0, 0} { // (L+R)/2 of mirrored pairs
		if math.Abs(float64(samples[i])-want) > 1e-4 { // 16-bit quantization
			t.Errorf("sample %d = %v, want %v", i, samples[i], want)
		}
	}
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

	a := NewApp(filepath.Join(lib.Dir, "m.db"))
	defer a.Close()
	a.lib.EngineLibrary = lib.EngineLibrary

	p := NewAudioPlayer(a.Tracks[0], wav, "") // no stems
	p.Play(a.mixer, func() { a.ensureAudio() })

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if stream, playing := p.state(); stream != nil && playing {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stream, playing := p.state()
	if stream == nil || !playing {
		t.Fatalf("stream nil: %v playing: %v, err=%q", stream == nil, playing, p.err)
	}
	p.Stop()
	if _, playing := p.state(); playing {
		t.Error("player should be stopped after Stop")
	}
	if stream, _ := p.state(); stream != nil {
		t.Error("stream voice should be released")
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

func TestF32leToMono(t *testing.T) {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b[0:4], math.Float32bits(0.25))
	binary.LittleEndian.PutUint32(b[4:8], math.Float32bits(-0.5))
	out := f32leToMono(b)
	if len(out) != 2 || out[0] != 0.25 || out[1] != -0.5 {
		t.Fatalf("f32leToMono = %v", out)
	}
}
