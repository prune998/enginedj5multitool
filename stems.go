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
	"time"

	"github.com/ebitengine/oto/v3"
	"github.com/hajimehoshi/go-mp3"
	. "go.hasen.dev/shirei"
)

// stems.go: Engine DJ v5 stems support.
//
// A track with separated stems has a `<Engine Library>/Stems/<trackID>
// <databaseUuid>.stems` file — an MP4 whose audio payload is Engine's
// proprietary encoding (8-channel AAC encrypted with AES-128-ECB; see
// stemsfile.go). When such a file exists the player decodes it (through an
// ffmpeg subprocess, no cgo) and mixes the four stems according to a
// per-stem activity mask. Without a stems file the track's own audio file
// plays, decoded in memory with pure-Go decoders (MP3: go-mp3, WAV: built-in
// reader) or an ffmpeg pipe for M4A/AAC when ffmpeg is on the PATH.
//
// Output goes through oto (pure Go on macOS/Linux/Windows) as 48 kHz stereo
// s16le; sources at other rates are linearly resampled. When no audio
// device can be opened playback degrades to a silent, real-time consumer so
// the UI still works headlessly.

// StemNames are the four stems in Engine DJ channel-pair order: channel
// pair 0 (ch 0/1) is Vocals, pair 1 (ch 2/3) is Bass, pair 2 (ch 4/5) is
// Drums and pair 3 (ch 6/7) is Other. The stems.go filter bitmask follows
// this order (bit i = StemNames[i]).
var StemNames = [4]string{"Vocals", "Bass", "Drums", "Other"}

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

// --- resampler: linear interpolation of interleaved stereo to the 48kHz
// device rate ---

type resampler struct {
	rate float64 // source rate
}

// process resamples one chunk of interleaved stereo float32. Each chunk is
// resampled independently (the phase resets at chunk boundaries — the
// timing stays exact and the sub-sample jitter is far below perception),
// which keeps the output frame-aligned even when a chunk's output count is
// odd. The input must have an even sample count (whole stereo frames).
func (r *resampler) process(in []float32) []float32 {
	if r.rate == 48000 {
		return in
	}
	step := r.rate / 48000
	frames := len(in) / 2
	n := int(float64(frames) / step)
	if n < 1 {
		return nil
	}
	out := make([]float32, n*2)
	for i := 0; i < n; i++ {
		pos := float64(i) * step
		i0 := int(pos)
		if i0 > frames-2 {
			i0 = frames - 2
		}
		if i0 < 0 {
			i0 = 0
		}
		f := float32(pos - float64(i0))
		out[i*2] = in[i0*2]*(1-f) + in[(i0+1)*2]*f
		out[i*2+1] = in[i0*2+1]*(1-f) + in[(i0+1)*2+1]*f
	}
	return out
}

// readWAVStereo decodes a PCM WAV file to interleaved stereo float32 at its
// native rate. Supports 16/24/32-bit integer and 32-bit float; mono is
// duplicated, and with more than two channels only the first two are kept.
func readWAVStereo(path string) ([]float32, int, error) {
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
	if channels < 1 || bits == 0 || len(pcm) == 0 || rate == 0 {
		return nil, 0, fmt.Errorf("%s: unsupported WAV", filepath.Base(path))
	}
	frame := channels * bits / 8
	n := len(pcm) / frame
	out := make([]float32, 0, n*2)
	for i := 0; i < n; i++ {
		base := pcm[i*frame:]
		l := wavSample(base, bits, isFloat)
		r := l
		if channels > 1 {
			r = wavSample(base[bits/8:], bits, isFloat)
		}
		out = append(out, float32(l), float32(r))
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

// --- output: one oto context for the whole process ---

var (
	audioOnce sync.Once
	audioCtx  *oto.Context
	audioErr  error
)

// audioContext lazily opens the 48 kHz stereo output device.
func audioContext() (*oto.Context, error) {
	audioOnce.Do(func() {
		op := &oto.NewContextOptions{SampleRate: 48000, ChannelCount: 2, Format: oto.FormatSignedInt16LE}
		ctx, ready, err := oto.NewContext(op)
		if err != nil {
			audioErr = err
			return
		}
		<-ready
		audioCtx = ctx
	})
	return audioCtx, audioErr
}

// pcmSource is an io.Reader the oto player pulls stereo s16le data from.
type pcmSource struct {
	mu     sync.Mutex
	cond   *sync.Cond
	chunks [][]byte
	bytes  int
	pulled uint64 // frames consumed by the player (2ch s16le = 4 B/frame)
	eof    bool
}

const pcmSourceMaxBytes = 1 << 20 // ~5 s of stereo s16 at 48 kHz: keeps the
// decoder ~real-time ahead of playback instead of buffering the whole file

func newPCMSource() *pcmSource {
	s := &pcmSource{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *pcmSource) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.chunks) == 0 && !s.eof {
		s.cond.Wait()
	}
	if len(s.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s.chunks[0])
	if n < len(s.chunks[0]) {
		s.bytes -= n
		s.chunks[0] = s.chunks[0][n:]
	} else {
		s.bytes -= len(s.chunks[0])
		s.chunks = s.chunks[1:]
	}
	s.pulled += uint64(n / 4)
	s.cond.Broadcast()
	return n, nil
}

func (s *pcmSource) push(b []byte) {
	s.mu.Lock()
	for s.bytes >= pcmSourceMaxBytes && !s.eof {
		s.cond.Wait()
	}
	s.chunks = append(s.chunks, b)
	s.bytes += len(b)
	s.cond.Signal()
	s.mu.Unlock()
}

func (s *pcmSource) finish() {
	s.mu.Lock()
	s.eof = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *pcmSource) reset() {
	s.mu.Lock()
	s.chunks = nil
	s.bytes = 0
	s.pulled = 0 // position bookkeeping restarts with the new offset
	s.eof = false
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *pcmSource) buffered() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytes
}

func (s *pcmSource) pulledFrames() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pulled
}

// stereoToS16 converts interleaved stereo float32 samples to clamped
// little-endian s16le bytes.
func stereoToS16(in []float32) []byte {
	out := make([]byte, len(in)*2)
	for i, v := range in {
		s := int32(v * 32768)
		if s > 32767 {
			s = 32767
		} else if s < -32768 {
			s = -32768
		}
		out[i*2] = byte(s)
		out[i*2+1] = byte(s >> 8)
	}
	return out
}

// downmixStems mixes the stems selected by mask (bit i = stem i, channel
// pair 2i/2i+1) from interleaved multi-channel float32 PCM into clamped
// stereo s16le bytes.
func downmixStems(pcm []float32, channels int, mask uint32) []byte {
	frames := 0
	if channels > 0 {
		frames = len(pcm) / channels
	}
	out := make([]byte, frames*4)
	for i := 0; i < frames; i++ {
		base := pcm[i*channels : i*channels+channels]
		var l, r float32
		for s := 0; s < len(StemNames); s++ {
			if mask&(1<<s) == 0 {
				continue
			}
			if s*2+1 >= channels {
				break
			}
			l += base[s*2]
			r += base[s*2+1]
		}
		lb := int32(clampF(l) * 32768)
		rb := int32(clampF(r) * 32768)
		out[i*4+0] = byte(lb)
		out[i*4+1] = byte(lb >> 8)
		out[i*4+2] = byte(rb)
		out[i*4+3] = byte(rb >> 8)
	}
	return out
}

func clampF(v float32) float32 {
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}

// AudioPlayer plays a track: its stems file when it has one (with a live
// per-stem filter), otherwise the track's own audio file. Everything is
// mixed to 48 kHz stereo s16le and fed to a single streaming oto player.
type AudioPlayer struct {
	mu          sync.Mutex
	Track       TrackRecord
	regularPath string // the track's own audio file
	stemsPath   string // "" when the track has no stems

	src       *pcmSource
	player    *oto.Player   // nil when no audio device is available
	dec       *stemsDecoder // active stems decode process
	stemsFile *stemsFile    // parsed stems container while playing stems
	stopCh    chan struct{} // closed to stop the producer
	seekSec   atomic.Int64  // regular-file seek target in ms (0 = none)
	stopOnce  sync.Once
	playing   atomic.Bool
	paused    atomic.Bool
	decDone   atomic.Bool
	seekTo    atomic.Int32 // packet index, or -1
	posBase   float64      // playback position at the last (re)start, seconds
	total     float64      // duration in seconds (0 = unknown)
	status    string
	err       string
	mask      atomic.Uint32 // stems to include in the mix (bit i = stem i)
}

// NewAudioPlayer creates a player for a track.
func NewAudioPlayer(track TrackRecord, regularPath, stemsPath string) *AudioPlayer {
	p := &AudioPlayer{Track: track, regularPath: regularPath, stemsPath: stemsPath}
	p.seekTo.Store(-1)
	p.mask.Store(0xf) // all stems on
	return p
}

// HasStems reports whether this player has a stems file to play.
func (p *AudioPlayer) HasStems() bool { return p.stemsPath != "" }

// SetStemMask selects which stems play (bit i = stem i). Live-applied.
func (p *AudioPlayer) SetStemMask(mask uint32) { p.mask.Store(mask) }

// StemMask returns the active stems bitmask.
func (p *AudioPlayer) StemMask() uint32 { return p.mask.Load() }

// Play starts playback. Safe from the UI thread.
func (p *AudioPlayer) Play() {
	p.mu.Lock()
	if p.playing.Load() {
		p.mu.Unlock()
		return
	}
	p.teardownLocked()
	p.stopCh = make(chan struct{})
	p.stopOnce = sync.Once{}
	p.playing.Store(true)
	p.paused.Store(false)
	p.decDone.Store(false)
	p.seekTo.Store(-1)
	p.posBase = 0
	p.err = ""
	p.status = "Loading…"
	ch := p.stopCh
	p.mu.Unlock()
	RequestNextFrame()
	go p.produce(ch)
	go p.monitor(ch)
}

// Stop halts playback.
func (p *AudioPlayer) Stop() {
	p.mu.Lock()
	p.teardownLocked()
	p.mu.Unlock()
	RequestNextFrame()
}

// TogglePause pauses or resumes playback; reports the paused state.
func (p *AudioPlayer) TogglePause() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.playing.Load() {
		return false
	}
	if p.paused.Load() {
		p.paused.Store(false)
		if p.player != nil {
			p.player.Play()
		}
	} else {
		p.paused.Store(true)
		if p.player != nil {
			p.player.Pause()
		}
	}
	return p.paused.Load()
}

// Seek jumps delta seconds relative to the current position — for every
// song: stems files jump by packet, regular files reposition their decoder.
func (p *AudioPlayer) Seek(delta float64) {
	p.mu.Lock()
	stems := p.stemsFile
	p.mu.Unlock()
	total, pos := p.Total(), p.Position()
	target := pos + delta
	if target < 0 {
		target = 0
	}
	if total > 0 && target > total {
		target = total
	}
	if stems != nil {
		idx := int(target * float64(stems.Timescale) / float64(stems.SamplesPerPkt))
		if idx >= len(stems.Packets) {
			idx = len(stems.Packets) - 1
		}
		if idx < 0 {
			idx = 0
		}
		p.seekTo.Store(int32(idx))
	} else {
		p.seekSec.Store(int64(target * 1000))
	}
	// a fresh player restarts the played-time bookkeeping at the new offset
	p.mu.Lock()
	p.posBase = target
	src, player := p.src, p.player
	if player != nil && src != nil {
		player.Close()
		np := audioCtx.NewPlayer(src)
		np.Play()
		p.player = np
	}
	if src != nil {
		src.reset()
	}
	p.paused.Store(false)
	p.mu.Unlock()
	RequestNextFrame()
}

// Position returns the playback position in seconds.
func (p *AudioPlayer) Position() float64 {
	p.mu.Lock()
	posBase, src, player, paused := p.posBase, p.src, p.player, p.paused.Load()
	p.mu.Unlock()
	if src == nil || paused {
		return posBase
	}
	pulled := src.pulledFrames()
	if player != nil {
		pulled -= uint64(player.BufferedSize() / 4) // bytes → stereo s16 frames
	}
	return posBase + float64(pulled)/48000
}

// Total returns the duration in seconds (0 = unknown).
func (p *AudioPlayer) Total() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.total
}

// Status returns the panel status line and error.
func (p *AudioPlayer) Status() (status, errMsg string, playing bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status, p.err, p.playing.Load()
}

// Paused reports whether playback is paused.
func (p *AudioPlayer) Paused() bool { return p.paused.Load() }

// state returns the streaming source and playing flag (test helper).
func (p *AudioPlayer) state() (src *pcmSource, playing bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.src, p.playing.Load()
}

// teardownLocked stops the current pipeline (caller holds p.mu).
func (p *AudioPlayer) teardownLocked() {
	p.playing.Store(false)
	p.paused.Store(false)
	if p.stopCh != nil {
		ch := p.stopCh
		p.stopCh = nil
		p.stopOnce.Do(func() { close(ch) })
	}
	if p.dec != nil {
		dec := p.dec
		p.dec = nil
		go dec.Close()
	}
	p.stemsFile = nil
	if p.player != nil {
		p.player.Close()
		p.player = nil
	}
	if p.src != nil {
		p.src.finish()
		p.src = nil
	}
	p.status = ""
}

// fail records a playback error and ends the session — but only when
// stopCh is still the current one (a replaced session owns the player now).
func (p *AudioPlayer) fail(stopCh chan struct{}, msg string) {
	p.mu.Lock()
	if p.stopCh != stopCh {
		p.mu.Unlock()
		return
	}
	p.err, p.status = msg, ""
	p.teardownLocked()
	p.mu.Unlock()
	RequestNextFrame()
}

// newOutputLocked creates the oto player for src, or — when no audio device
// is available — a silent real-time consumer so playback still progresses.
// Caller holds p.mu (creation is cheap and ordered against teardown).
func (p *AudioPlayer) newOutputLocked(src *pcmSource) *oto.Player {
	ctx, err := audioContext()
	if err != nil {
		p.status = "Playing (no audio device: silent)"
		go p.discardLoop(src, p.stopCh)
		return nil
	}
	player := ctx.NewPlayer(src)
	player.Play()
	return player
}

// discardLoop consumes the stream in real time when there is no device.
func (p *AudioPlayer) discardLoop(src *pcmSource, stopCh chan struct{}) {
	buf := make([]byte, 4800*4) // 100 ms of stereo s16
	for {
		select {
		case <-stopCh:
			return
		default:
		}
		if p.paused.Load() {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if _, err := src.Read(buf); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// monitor ends playback at end-of-stream and refreshes the UI.
func (p *AudioPlayer) monitor(stopCh chan struct{}) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
		}
		if !p.decDone.Load() || p.paused.Load() {
			RequestNextFrame()
			continue
		}
		p.mu.Lock()
		src, player := p.src, p.player
		p.mu.Unlock()
		drained := src == nil || src.buffered() == 0
		deviceIdle := player == nil || !player.IsPlaying()
		if drained && deviceIdle {
			p.mu.Lock()
			current := p.stopCh
			p.mu.Unlock()
			if current == stopCh { // a newer session owns the player otherwise
				p.Stop()
			}
			return
		}
		RequestNextFrame()
	}
}

// produce decodes the whole track and feeds the output source. Runs on its
// own goroutine; returns when playback ends, fails or is stopped.
func (p *AudioPlayer) produce(stopCh chan struct{}) {
	defer p.decDone.Store(true)
	if p.stemsPath != "" {
		p.produceStems(stopCh)
	} else {
		p.produceRegular(stopCh)
	}
	p.mu.Lock()
	src := p.src
	p.mu.Unlock()
	if src != nil {
		src.finish()
	}
}

// produceStems decodes the stems payload through ffmpeg.
func (p *AudioPlayer) produceStems(stopCh chan struct{}) {
	f, err := OpenStems(p.stemsPath)
	if err != nil {
		p.fail(stopCh, err.Error())
		return
	}
	p.mu.Lock()
	if p.stopCh != stopCh { // session replaced while loading
		p.mu.Unlock()
		return
	}
	p.stemsFile = f
	p.total = float64(f.DurationFrames) / float64(f.Timescale)
	src := newPCMSource()
	player := p.newOutputLocked(src)
	p.src, p.player = src, player
	p.status = "Playing"
	p.mu.Unlock()
	RequestNextFrame()

	start := 0
	if f.DSIIsFirstPkt {
		start = 1 // the extradata carrier decodes to nothing useful
	}
	if start >= len(f.Packets) {
		p.fail(stopCh, "stems file has no audio packets")
		return
	}

	pcm := make([]float32, 4096*f.Channels)
	for {
		dec, err := newStemsDecoder(f, start, 48000)
		if err != nil {
			p.fail(stopCh, err.Error())
			return
		}
		p.mu.Lock()
		if p.stopCh != stopCh { // session replaced while spawning
			p.mu.Unlock()
			dec.Close()
			return
		}
		p.dec = dec
		p.mu.Unlock()

		eof := false
		for {
			select {
			case <-stopCh:
				dec.Close()
				return
			default:
			}
			if t := p.seekTo.Swap(-1); t >= 0 {
				start = int(t)
				break
			}
			n, rerr := dec.Read(pcm)
			if n > 0 {
				out := downmixStems(pcm[:n], f.Channels, p.mask.Load())
				select {
				case <-stopCh:
					dec.Close()
					return
				default:
				}
				src.push(out)
			}
			if rerr != nil {
				eof = true
				break
			}
		}
		dec.Close()
		if eof {
			return
		}
	}
}

// produceRegular decodes the track's own audio file to stereo float32.
func (p *AudioPlayer) produceRegular(stopCh chan struct{}) {
	path := p.regularPath
	if path == "" {
		p.fail(stopCh, "no audio file for this track")
		return
	}
	var srcFn func() ([]float32, bool)
	var seekFn func(sec float64) error
	rate := 0
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav":
		frames, r, err := readWAVStereo(path)
		if err != nil {
			p.fail(stopCh, err.Error())
			return
		}
		p.mu.Lock()
		p.total = float64(len(frames)/2) / float64(r)
		p.mu.Unlock()
		rate = r
		off := 0
		const wavChunk = 48000 * 2
		srcFn = func() ([]float32, bool) {
			if off >= len(frames) {
				return nil, false
			}
			end := min(off+wavChunk, len(frames))
			out := frames[off:end]
			off = end
			return out, true
		}
		seekFn = func(sec float64) error {
			f := int(sec * float64(r))
			if f < 0 {
				f = 0
			}
			if max := len(frames) / 2; f > max {
				f = max
			}
			off = f * 2
			return nil
		}
	case ".mp3":
		f, err := os.Open(path)
		if err != nil {
			p.fail(stopCh, err.Error())
			return
		}
		defer f.Close()
		dec, err := mp3.NewDecoder(f)
		if err != nil {
			p.fail(stopCh, err.Error())
			return
		}
		rate = dec.SampleRate()
		p.mu.Lock()
		p.total = float64(dec.Length()) / float64(rate*4) // stereo s16 bytes
		p.mu.Unlock()
		srcFn = func() ([]float32, bool) {
			// one chunk of interleaved stereo s16 → float32
			need := 48000 * 4 // 0.5 s worth of bytes
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
			out := make([]float32, n*2)
			for i := 0; i < n; i++ {
				l := int16(uint16(buf[i*4]) | uint16(buf[i*4+1])<<8)
				r := int16(uint16(buf[i*4+2]) | uint16(buf[i*4+3])<<8)
				out[i*2] = float32(l) / 32768
				out[i*2+1] = float32(r) / 32768
			}
			return out, true
		}
		seekFn = func(sec float64) error {
			// go-mp3 seeks in decoded PCM bytes (4 per stereo frame)
			_, err := dec.Seek(int64(sec*float64(rate))*4, io.SeekStart)
			return err
		}
	case ".m4a", ".mp4", ".aac":
		ffmpeg, lerr := exec.LookPath("ffmpeg")
		if lerr != nil {
			p.fail(stopCh, "M4A playback needs ffmpeg on the PATH (or use MP3/WAV)")
			return
		}
		var cmd *exec.Cmd
		var stdout io.ReadCloser
		// start (re)spawns the decode process; -ss seeks instantly on the
		// seekable input file.
		start := func(sec float64) error {
			if cmd != nil {
				stdout.Close()
				cmd.Process.Kill()
				cmd.Wait()
				cmd = nil
			}
			args := []string{"-v", "error"}
			if sec > 0 {
				args = append(args, "-ss", fmt.Sprintf("%.3f", sec))
			}
			args = append(args, "-i", path, "-f", "f32le",
				"-ac", "2", "-ar", "48000", "pipe:1")
			cmd = exec.Command(ffmpeg, args...)
			var perr, serr error
			stdout, perr = cmd.StdoutPipe()
			if perr != nil {
				return perr
			}
			if serr = cmd.Start(); serr != nil {
				return serr
			}
			return nil
		}
		if err := start(0); err != nil {
			p.fail(stopCh, err.Error())
			return
		}
		rate = 48000
		srcFn = func() ([]float32, bool) {
			buf := make([]float32, 48000*2) // 0.5s of stereo f32
			b := make([]byte, len(buf)*4)
			n, err := io.ReadFull(stdout, b)
			if n == 0 {
				return nil, false
			}
			_ = err // short final read is fine
			for i := 0; i < n/4; i++ {
				buf[i] = math.Float32frombits(uint32(b[i*4]) | uint32(b[i*4+1])<<8 |
					uint32(b[i*4+2])<<16 | uint32(b[i*4+3])<<24)
			}
			return buf[:n/4], true
		}
		seekFn = start
		defer func() {
			if cmd != nil {
				stdout.Close()
				cmd.Process.Kill()
				cmd.Wait()
			}
		}()
	default:
		p.fail(stopCh, fmt.Sprintf("unsupported audio format: %s", filepath.Ext(path)))
		return
	}

	p.mu.Lock()
	if p.stopCh != stopCh { // session replaced while loading
		p.mu.Unlock()
		return
	}
	src := newPCMSource()
	player := p.newOutputLocked(src)
	p.src, p.player = src, player
	p.status = "Playing"
	p.mu.Unlock()
	RequestNextFrame()

	rs := &resampler{rate: float64(rate)}
	for {
		select {
		case <-stopCh:
			return
		default:
		}
		if ms := p.seekSec.Swap(0); ms > 0 {
			if seekFn != nil {
				if err := seekFn(float64(ms) / 1000); err != nil {
					p.fail(stopCh, err.Error())
					return
				}
				rs = &resampler{rate: float64(rate)}
			}
			continue
		}
		in, more := srcFn()
		// a seek that lands while decoding: drop the stale chunk and jump
		if ms := p.seekSec.Swap(0); ms > 0 {
			if seekFn != nil {
				if err := seekFn(float64(ms) / 1000); err != nil {
					p.fail(stopCh, err.Error())
					return
				}
				rs = &resampler{rate: float64(rate)}
			}
			continue
		}
		if len(in) > 0 {
			out := rs.process(in)
			if len(out) > 0 {
				select {
				case <-stopCh:
					return
				default:
				}
				src.push(stereoToS16(out))
			}
		}
		if !more {
			return
		}
	}
}
