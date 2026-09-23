package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/hajimehoshi/go-mp3"
	. "go.hasen.dev/shirei"
	"go.hasen.dev/shirei/audio"
)

// stems.go: Engine DJ v5 stems support.
//
// A track with separated stems has a `<Engine Library>/Stems/<trackID>
// <databaseUuid>.stems` file — an MP4 whose audio payload is Engine's
// proprietary encoding (it does not parse as standard AAC; neither ffmpeg
// nor CoreAudio can decode it). Stems playback is therefore NOT working
// yet; the player below always plays the track's own audio file, decoded
// in memory with pure-Go decoders (MP3: go-mp3, WAV: built-in reader) or
// an ffmpeg pipe for M4A/AAC when ffmpeg is on the PATH.

// StemNames are the four stems, in Engine DJ channel-pair order.
var StemNames = [4]string{"Drums", "Bass", "Other", "Vocals"}

// stemsUUID caches the database uuid (part of the stems file name).
func (l *Library) stemsUUID() string {
	l.stemsOnce.Do(func() {
		var uuid string
		err := l.DB.QueryRow(`SELECT uuid FROM Information LIMIT 1`).Scan(&uuid)
		if err == nil {
			l.stemsUUIDVal = uuid
		}
	})
	return l.stemsUUIDVal
}

// StemsFile returns the .stems file of a track ("" when it has none).
func (l *Library) StemsFile(id int64) string {
	uuid := l.stemsUUID()
	if l.EngineLibrary == "" || uuid == "" {
		return ""
	}
	p := filepath.Join(l.EngineLibrary, "Stems", fmt.Sprintf("%d %s.stems", id, uuid))
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return ""
}

// HasStems reports whether a track has separated stems.
func (l *Library) HasStems(id int64) bool {
	return l.StemsFile(id) != ""
}

// StemsAvailable reports whether any track in the library has stems.
func (l *Library) StemsAvailable() bool {
	return l.EngineLibrary != "" && l.stemsUUID() != ""
}

// --- resampler: stateful linear interpolator to the 48kHz device rate ---

type resampler struct {
	rate float64 // source rate
	pos  float64 // fractional source position, carried across chunks
}

func (r *resampler) process(in []float32) []float32 {
	if r.rate == 48000 {
		return in
	}
	step := r.rate / 48000
	n := int(float64(len(in)) / step)
	if n == 0 {
		return nil
	}
	out := make([]float32, n)
	for i := range out {
		i0 := int(r.pos)
		if i0 >= len(in)-1 {
			out[i] = in[len(in)-1]
			r.pos += step
			continue
		}
		f := float32(r.pos - float64(i0))
		out[i] = in[i0]*(1-f) + in[i0+1]*f
		r.pos += step
	}
	return out
}

// f32leToMono converts little-endian float32 PCM bytes to a sample slice.
func f32leToMono(b []byte) []float32 {
	n := len(b) / 4
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = math.Float32frombits(uint32(b[i*4]) | uint32(b[i*4+1])<<8 |
			uint32(b[i*4+2])<<16 | uint32(b[i*4+3])<<24)
	}
	return out
}

// readWAVMono decodes a PCM WAV file to mono float32 at its native rate.
// Supports 16/24/32-bit integer and 32-bit float, mono or stereo (stereo is
// averaged down).
func readWAVMono(path string) ([]float32, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("%s: not a WAV file", filepath.Base(path))
	}
	var rate int
	var channels int
	var bits int
	var isFloat bool
	var pcm []byte
	off := 12
	for off+8 <= len(data) {
		id := string(data[off : off+4])
		size := int(uint32(data[off+4]) | uint32(data[off+5])<<8 | uint32(data[off+6])<<16 | uint32(data[off+7])<<24)
		body := data[off+8 : min(off+8+size, len(data))]
		switch id {
		case "fmt ":
			if len(body) >= 16 {
				channels = int(uint16(body[2]) | uint16(body[3])<<8)
				rate = int(uint32(body[4]) | uint32(body[5])<<8 | uint32(body[6])<<16 | uint32(body[7])<<24)
				bits = int(uint16(body[14]) | uint16(body[15])<<8)
				if format := uint16(body[0]) | uint16(body[1])<<8; format == 3 {
					isFloat = true
				}
			}
		case "data":
			pcm = body
		}
		off += 8 + size + (size & 1) // chunks are word-aligned
	}
	if channels < 1 || bits == 0 || len(pcm) == 0 {
		return nil, 0, fmt.Errorf("%s: unsupported WAV", filepath.Base(path))
	}
	frame := channels * bits / 8
	n := len(pcm) / frame
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		var sum float64
		for c := 0; c < channels; c++ {
			sum += wavSample(pcm[i*frame+c*bits/8:], bits, isFloat)
		}
		out[i] = float32(sum / float64(channels))
	}
	return out, rate, nil
}

func wavSample(b []byte, bits int, isFloat bool) float64 {
	switch bits {
	case 16:
		v := int16(uint16(b[0]) | uint16(b[1])<<8)
		return float64(v) / 32768
	case 24:
		v := int32(b[0]) | int32(b[1])<<8 | int32(b[2])<<16
		if v&0x800000 != 0 {
			v |= -1 << 24
		}
		return float64(v) / 8388608
	case 32:
		if isFloat {
			return float64(math.Float32frombits(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24))
		}
		v := int32(b[0]) | int32(b[1])<<8 | int32(b[2])<<16 | int32(b[3])<<24
		return float64(v) / 2147483648
	}
	return 0
}

// AudioPlayer plays a track's own audio file (the stems payload is
// Engine-proprietary and cannot be decoded yet). Decoding is in memory:
// MP3 (go-mp3) and WAV are pure Go; M4A/AAC streams through an ffmpeg
// pipe when ffmpeg is on the PATH. Everything is resampled to the 48kHz
// device rate and fed to a single streaming voice.
type AudioPlayer struct {
	mu          sync.Mutex
	Track       TrackRecord
	regularPath string // the track's own audio file
	stemsPath   string // "" when the track has no stems (unused for playback)

	stream  *audio.StreamVoice
	playing atomic.Bool
	stopCh  chan struct{}
	status  string
	err     string
}

// NewAudioPlayer creates a player for a track.
func NewAudioPlayer(track TrackRecord, regularPath, stemsPath string) *AudioPlayer {
	return &AudioPlayer{Track: track, regularPath: regularPath, stemsPath: stemsPath}
}

// Play starts playback of the regular file. Safe from the UI thread.
func (p *AudioPlayer) Play(mixer *audio.Mixer, setAudio func()) {
	p.mu.Lock()
	if p.playing.Load() {
		p.mu.Unlock()
		return
	}
	p.stopLocked()
	p.playing.Store(true)
	p.err = ""
	path := p.regularPath
	p.mu.Unlock()
	if path == "" {
		p.mu.Lock()
		p.err = "no audio file for this track"
		p.playing.Store(false)
		p.mu.Unlock()
		RequestNextFrame()
		return
	}
	p.status = "Playing"
	RequestNextFrame()

	go func() {
		// source stream: mono float32 at a native rate + the rate.
		var src func() ([]float32, bool)
		var rate int
		switch strings.ToLower(filepath.Ext(path)) {
		case ".wav":
			frames, r, err := readWAVMono(path)
			if err != nil {
				p.fail(err.Error())
				return
			}
			rate = r
			off := 0
			const wavChunk = 48000
			src = func() ([]float32, bool) {
				if off >= len(frames) {
					return nil, false
				}
				end := min(off+wavChunk, len(frames))
				out := frames[off:end]
				off = end
				return out, true
			}
		case ".mp3":
			f, err := os.Open(path)
			if err != nil {
				p.fail(err.Error())
				return
			}
			defer f.Close()
			dec, err := mp3.NewDecoder(f)
			if err != nil {
				p.fail(err.Error())
				return
			}
			rate = dec.SampleRate()
			src = func() ([]float32, bool) {
				// accumulate one chunk of interleaved stereo s16 → mono
				need := 48000 * 4 // 0.5s worth of bytes
				got := 0
				buf := make([]byte, need)
				for got < need {
					n, err := dec.Read(buf[got:need])
					got += n
					if err != nil {
						if got < 4 {
							return nil, false
						}
						break
					}
				}
				n := got / 4 // stereo frames
				out := make([]float32, n)
				for i := 0; i < n; i++ {
					l := int16(uint16(buf[i*4]) | uint16(buf[i*4+1])<<8)
					r := int16(uint16(buf[i*4+2]) | uint16(buf[i*4+3])<<8)
					out[i] = (float32(l) + float32(r)) / 2 / 32768
				}
				return out, true
			}
		case ".m4a", ".mp4", ".aac":
			ffmpeg, lerr := exec.LookPath("ffmpeg")
			if lerr != nil {
				p.fail("M4A playback needs ffmpeg on the PATH (or use MP3/WAV)")
				return
			}
			cmd := exec.Command(ffmpeg, "-v", "error", "-i", path, "-f", "f32le",
				"-ac", "1", "-ar", "48000", "pipe:1")
			stdout, perr := cmd.StdoutPipe()
			if perr != nil {
				p.fail(perr.Error())
				return
			}
			if serr := cmd.Start(); serr != nil {
				p.fail(serr.Error())
				return
			}
			defer cmd.Process.Kill()
			rate = 48000
			src = func() ([]float32, bool) {
				buf := make([]byte, 48000*4) // 0.5s of f32le
				n, err := io.ReadFull(stdout, buf)
				if n == 0 {
					return nil, false
				}
				_ = err // short final read is fine
				return f32leToMono(buf[:n]), true
			}
		default:
			p.fail(fmt.Sprintf("unsupported audio format: %s", filepath.Ext(path)))
			return
		}

		rs := &resampler{rate: float64(rate)}
		v := audio.NewStreamVoice(48000 * 2) // ~2s ring
		p.mu.Lock()
		p.stream = v
		p.mu.Unlock()
		setAudio()
		mixer.Add(v)

		for {
			select {
			case <-p.stopCh:
				v.Release()
				return
			default:
			}
			in, more := src()
			if len(in) > 0 {
				out := rs.process(in)
				if len(out) > 0 {
					if _, err := v.Write(out); err != nil {
						return
					}
				}
			}
			if !more {
				break
			}
		}
		v.Close()
	}()
}

// fail records a playback error on the producer side.
func (p *AudioPlayer) fail(msg string) {
	p.mu.Lock()
	p.err, p.status = msg, ""
	p.playing.Store(false)
	p.mu.Unlock()
	RequestNextFrame()
}

// Stop halts playback.
func (p *AudioPlayer) Stop() {
	p.mu.Lock()
	p.playing.Store(false)
	stopCh := p.stopCh
	p.status = ""
	p.mu.Unlock()
	if stopCh != nil {
		close(stopCh)
	}
	p.stopLocked()
}

// stopLocked tears down the current voice (caller holds p.mu or is Play).
func (p *AudioPlayer) stopLocked() {
	p.stopCh = make(chan struct{})
	if p.stream != nil {
		p.stream.Release()
		p.stream = nil
	}
}

// state returns the mode/voice/playing under the lock (test helper).
func (p *AudioPlayer) state() (stream *audio.StreamVoice, playing bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stream, p.playing.Load()
}

// Status returns the panel status line and error.
func (p *AudioPlayer) Status() (status, errMsg string, playing bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status, p.err, p.playing.Load()
}
