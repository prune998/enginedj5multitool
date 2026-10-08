package main

// stemsgen.go: generate Engine DJ stems for a track through a local StemDeck
// server (github.com/stemdeckapp/stemdeck, by default http://localhost:8000).
//
// The pipeline for one track:
//  1. upload the track's audio file to StemDeck (POST /api/jobs, multipart)
//     and poll GET /api/jobs/{id} until the separation finishes;
//  2. download the six separated stem WAVs — the Demucs model always
//     separates vocals, drums, bass, guitar, piano and other;
//  3. build the four Engine stems: Vocals, Bass, Drums and Other, where
//     Other is "the original for the remaining" part of the mix (the guitar,
//     piano and other stems summed — Demucs stems add back up to the source);
//  4. encode each stem as stereo AAC (ffmpeg → ADTS) and assemble the four
//     channel-pair elements into a single 8-channel packet per frame. A
//     hand-built AudioSpecificConfig (channel configuration 0 with a program
//     config element describing four stereo pairs) describes the layout,
//     matching how Engine's own stems files decode: stem i occupies channel
//     pair 2i/2i+1 in the order Vocals, Bass, Drums, Other;
//  5. encrypt the packets and write the .stems file (see writeStems in
//     stemsfile.go) into <Engine Library>/Stems/<trackID> <uuid>.stems.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	. "go.hasen.dev/shirei"
)

// DefaultStemdeckURL is the StemDeck server used when the config sets none.
const DefaultStemdeckURL = "http://localhost:8000"

// StemsGenJob is one track's stems generation run. The goroutine mutates the
// fields under mu; the UI reads them through Status. Lifetime bookkeeping
// (a.stemsGen, a.stemsSet) is mutated by the goroutine under the frame lock.
type StemsGenJob struct {
	Track TrackRecord

	mu       sync.Mutex
	state    string  // uploading | processing | downloading | encoding | done | error | cancelled
	stage    string  // human-readable detail for the current state
	progress float64 // 0..100 during separation
	err      string
	jobID    string // StemDeck job id once submitted

	cancel   chan struct{}
	cancelMu sync.Mutex
	canceled bool // cancel() already ran (stops repeat HTTP cancel calls)
}

func newStemsGenJob(rec TrackRecord) *StemsGenJob {
	return &StemsGenJob{Track: rec, cancel: make(chan struct{})}
}

// Finished reports whether the job reached a terminal state.
func (j *StemsGenJob) Finished() bool {
	switch j.getState() {
	case "done", "error", "cancelled":
		return true
	}
	return false
}

func (j *StemsGenJob) getState() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state
}

func (j *StemsGenJob) set(state, stage string) {
	j.mu.Lock()
	j.state, j.stage = state, stage
	j.mu.Unlock()
	RequestNextFrame()
}

func (j *StemsGenJob) setProgress(state, stage string, progress float64) {
	j.mu.Lock()
	j.state, j.stage, j.progress = state, stage, progress
	j.mu.Unlock()
	RequestNextFrame()
}

func (j *StemsGenJob) fail(format string, args ...any) {
	j.mu.Lock()
	j.state, j.err = "error", fmt.Sprintf(format, args...)
	j.mu.Unlock()
	RequestNextFrame()
}

// Status returns the job state for display.
func (j *StemsGenJob) Status() (state, stage, errMsg string, progress float64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state, j.stage, j.err, j.progress
}

// Cancel stops the run: the local pipeline unwinds and StemDeck is asked to
// cancel (then delete) the remote job.
func (j *StemsGenJob) Cancel(server string) {
	j.cancelMu.Lock()
	defer j.cancelMu.Unlock()
	if j.canceled {
		return
	}
	j.canceled = true
	close(j.cancel)
	if id := j.jobID; id != "" {
		go func() {
			req, err := http.NewRequest(http.MethodPost, server+"/api/jobs/"+id+"/cancel", nil)
			if err == nil {
				resp, err := http.DefaultClient.Do(req)
				if err == nil {
					resp.Body.Close()
				}
			}
			req, err = http.NewRequest(http.MethodDelete, server+"/api/jobs/"+id, nil)
			if err == nil {
				resp, err := http.DefaultClient.Do(req)
				if err == nil {
					resp.Body.Close()
				}
			}
		}()
	}
	j.set("cancelled", "Cancelled")
}

// canceled reports whether the run was asked to stop.
func (j *StemsGenJob) wasCanceled() bool {
	select {
	case <-j.cancel:
		return true
	default:
		return false
	}
}

// startStemsGen launches stems generation for the selected track. Only one
// job may run at a time.
func (a *App) startStemsGen(rec TrackRecord) {
	if a.lib == nil || (a.stemsGen != nil && !a.stemsGen.Finished()) {
		return
	}
	server := a.StemdeckURL
	if server == "" {
		server = DefaultStemdeckURL
	}
	job := newStemsGenJob(rec)
	a.stemsGen = job
	RequestNextFrame()
	go job.run(a.lib, server, func() {
		WithFrameLock(func() {
			if a.stemsSet == nil {
				a.stemsSet = map[int64]bool{}
			}
			a.stemsSet[rec.ID] = true
		})
		RequestNextFrame()
	})
}

// run executes the whole pipeline. Runs on its own goroutine.
func (j *StemsGenJob) run(lib *Library, server string, onSuccess func()) {
	client := &http.Client{Timeout: 15 * time.Minute}

	// --- preconditions ---------------------------------------------------
	audioPath := ""
	if lib != nil {
		audioPath = lib.ResolveMedia(j.Track.Path)
	}
	if audioPath == "" {
		j.fail("cannot locate the audio file for this track")
		return
	}
	if !StemsKeyConfigured() {
		j.fail("the stems payload key is not configured — it is needed to encrypt the stems file (set it in Settings)")
		return
	}
	engineLibrary := ""
	dbUUID := ""
	if lib != nil {
		engineLibrary = lib.EngineLibrary
		dbUUID = lib.stemsUUID()
	}
	if engineLibrary == "" || dbUUID == "" {
		j.fail("stems need an Engine Library folder and its database uuid (open a library first)")
		return
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		j.fail("ffmpeg is required on the PATH to encode the stems")
		return
	}

	tmp, err := os.MkdirTemp("", "enginedj5-stems-")
	if err != nil {
		j.fail("create temp dir: %v", err)
		return
	}
	defer os.RemoveAll(tmp)

	// --- 1. upload to StemDeck -------------------------------------------
	j.set("uploading", "Uploading "+filepath.Base(audioPath)+" to StemDeck…")
	jobID, err := j.submit(client, server, audioPath)
	if err != nil {
		j.fail("StemDeck upload failed: %v", err)
		return
	}
	j.mu.Lock()
	j.jobID = jobID
	j.mu.Unlock()
	if j.wasCanceled() {
		j.Cancel(server)
		return
	}

	// --- 2. wait for the separation --------------------------------------
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	pollFails := 0
	for {
		if j.wasCanceled() {
			j.Cancel(server)
			return
		}
		st, err := fetchJobState(client, server, jobID)
		if err != nil {
			// A hiccup on the way to the (local) server should not kill a
			// separation that has been running for minutes.
			pollFails++
			if pollFails >= 5 {
				j.fail("StemDeck poll failed: %v", err)
				return
			}
			<-ticker.C
			continue
		}
		pollFails = 0
		switch st.Status {
		case "done":
			j.setProgress("downloading", "Separation complete — downloading stems", 100)
		case "error", "cancelled", "stopped":
			msg := st.Error
			if st.ErrorDetail != "" {
				if msg != "" {
					msg += ": " + st.ErrorDetail
				} else {
					msg = st.ErrorDetail
				}
			}
			if msg == "" {
				msg = "StemDeck reported " + st.Status
			}
			j.fail("StemDeck job %s: %s", st.Status, msg)
			return
		default:
			j.setProgress("processing", "StemDeck: "+st.Status+" — "+st.Stage, st.Progress)
			<-ticker.C
			continue
		}
		break
	}

	// --- 3. download the six stem WAVs ------------------------------------
	names := []string{"vocals", "bass", "drums", "guitar", "piano", "other"}
	for i, name := range names {
		if j.wasCanceled() {
			j.Cancel(server)
			return
		}
		j.setProgress("downloading", fmt.Sprintf("Downloading %s.wav (%d/%d)", name, i+1, len(names)), float64(i)*100/float64(len(names)))
		if err := downloadStem(client, server, jobID, name, filepath.Join(tmp, name+".wav")); err != nil {
			j.fail("download %s stem: %v", name, err)
			return
		}
	}

	// --- 4. Other = guitar + piano + other (the remaining original) -------
	j.set("encoding", "Building the Other stem (guitar + piano + other)…")
	if err := sumStems(tmp, []string{"guitar", "piano", "other"}, "otherfull"); err != nil {
		j.fail("build other stem: %v", err)
		return
	}

	// --- 5. encode each stem as stereo AAC (ADTS) -------------------------
	stems := map[string]string{
		"vocals": "vocals",
		"bass":   "bass",
		"drums":  "drums",
		"other":  "otherfull", // the summed remaining mix
	}
	order := []string{"vocals", "bass", "drums", "other"}
	for i, name := range order {
		if j.wasCanceled() {
			j.Cancel(server)
			return
		}
		j.setProgress("encoding", fmt.Sprintf("Encoding AAC: %s (%d/4)", name, i+1), float64(i)*100/4)
		if err := encodeStemAAC(filepath.Join(tmp, stems[name]+".wav"), filepath.Join(tmp, name+".adts")); err != nil {
			j.fail("encode %s stem: %v", name, err)
			return
		}
	}

	// --- 6. assemble the 8-channel packets --------------------------------
	j.setProgress("encoding", "Assembling 8-channel AAC packets", 75)
	packets, err := assembleStemsPackets(tmp, order)
	if err != nil {
		j.fail("assemble AAC packets: %v", err)
		return
	}
	if len(packets) == 0 {
		j.fail("the encoded stems hold no audio frames")
		return
	}

	// --- 7. craft the config and write the encrypted .stems file ----------
	j.set("encoding", "Writing encrypted stems file…")
	dsi, err := buildStemsDSI(44100)
	if err != nil {
		j.fail("build stems config: %v", err)
		return
	}
	dest := filepath.Join(engineLibrary, "Stems", fmt.Sprintf("%d %s.stems", j.Track.ID, dbUUID))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		j.fail("create Stems folder: %v", err)
		return
	}
	if err := writeStems(dest, dsi, 44100, 1024, 8, packets); err != nil {
		j.fail("write stems file: %v", err)
		return
	}
	if onSuccess != nil {
		onSuccess()
	}
	j.set("done", "Stems ready: "+filepath.Base(dest))
}

// submit uploads the track's audio file and returns the new StemDeck job id.
func (j *StemsGenJob) submit(client *http.Client, server, audioPath string) (string, error) {
	f, err := os.Open(audioPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		pw.CloseWithError(func() error {
			part, err := mw.CreateFormFile("file", filepath.Base(audioPath))
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, f); err != nil {
				return err
			}
			// The stems field selects the "selected mix" export only; the
			// separation always produces all six stems, so omit it.
			return mw.Close()
		}())
	}()

	req, err := http.NewRequest(http.MethodPost, server+"/api/jobs", pr)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, trimBody(body))
	}
	var out struct {
		JobID  string `json:"job_id"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("unexpected response: %s", trimBody(body))
	}
	if out.JobID == "" {
		if out.Detail != "" {
			return "", errors.New(out.Detail)
		}
		return "", fmt.Errorf("no job id in response: %s", trimBody(body))
	}
	return out.JobID, nil
}

// stemsJobState mirrors the job snapshot returned by GET /api/jobs/{id}.
type stemsJobState struct {
	JobID       string  `json:"job_id"`
	Status      string  `json:"status"`
	Progress    float64 `json:"progress"`
	Stage       string  `json:"stage"`
	Error       string  `json:"error"`
	ErrorDetail string  `json:"error_detail"`
}

func fetchJobState(client *http.Client, server, jobID string) (*stemsJobState, error) {
	resp, err := client.Get(server + "/api/jobs/" + jobID)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, trimBody(body))
	}
	var st stemsJobState
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func downloadStem(client *http.Client, server, jobID, name, dest string) error {
	resp, err := client.Get(server + "/api/jobs/" + jobID + "/stems/" + name + ".wav")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, trimBody(body))
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

func trimBody(body []byte) string {
	s := string(bytes.TrimSpace(body))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
