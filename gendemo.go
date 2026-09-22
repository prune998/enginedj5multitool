package main

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"

	"github.com/bogem/id3v2/v2"
	_ "modernc.org/sqlite"
)

// genDemoDB creates a demo Engine DJ database with fictitious tracks for
// documentation screenshots (no real library data).
func genDemoDB(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	schema := `
	CREATE TABLE Track (
		id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT, artist TEXT, album TEXT,
		filename TEXT, path TEXT, fileType TEXT, bpmAnalyzed REAL, length INTEGER,
		bpm INTEGER, year INTEGER, playOrder INTEGER, genre TEXT, comment TEXT, composer TEXT,
		rating INTEGER, key INTEGER, fileBytes INTEGER
	);
	CREATE TABLE PerformanceData (
		trackId INTEGER PRIMARY KEY, trackData BLOB, quickCues BLOB, loops BLOB, activeOnLoadLoops INTEGER
	);`
	if _, err := db.Exec(schema); err != nil {
		return err
	}

	// base is the demo root: <base>/Engine Library/Database2/m.db plus the
	// music folders the audio files are written to.
	base := filepath.Dir(filepath.Dir(filepath.Dir(path)))

	const sampleRate = 44100.0
	type demoTrack struct {
		title, artist, album, genre string
		bpm                         int
		year                        int
		lengthSec                   int
		rating                      int64
		key                         int64
		comment                     string
		cues                        [][2]any // {label, minute mark seconds}
		loops                       [][4]any // {label, startSec, endSec}
	}
	tracks := []demoTrack{
		{"Midnight Circuit", "Vector Nine", "Neon District", "Techno", 128, 2023, 372, 80, 5, "#techno #peaktime #cued #looped",
			[][2]any{{"Intro", 0}, {"Build", 128}, {"Drop", 256}, {"Break", 312}},
			[][4]any{{"Loop 8", 256.5, 263.5}, {"Roll", 128, 131.5}}},
		{"Solar Drift", "Aurora Kid", "Sunwalker", "House", 124, 2022, 415, 60, 8, "#house #warmup #cued",
			[][2]any{{"Intro", 0}, {"Vox", 96}, {"Outro", 384}},
			[][4]any{{"Vox Loop", 96, 103}}},
		{"Glass Corridor", "Mono Field", "Interiors", "Electronic", 118, 2021, 341, 0, 0, "",
			[][2]any{{"Cue 1", 12}, {"Cue 2", 200}, {"Cue 3", 84}}, // out of order on purpose
			nil},
		{"Paper Planes Over Kyoto", "The Glass Host", "Signal Fires", "Indie Dance", 112, 2024, 289, 40, 11, "#indiedance #cued",
			[][2]any{{"Intro", 0}, {"Hook", 64}},
			[][4]any{{"Hook 4", 64, 67}, {"Break", 160, 167.5}}},
		{"Chrome Sunset", "Vector Nine", "Neon District", "Techno", 130, 2023, 402, 100, 4, "#techno #cued #looped",
			[][2]any{{"Intro", 0}, {"Acid", 148}, {"Peak", 288}},
			[][4]any{{"Acid Roll", 148, 151}, {"Peak 8", 288, 295}}},
		{"Low Tide", "Marina Spec", "Undertow", "Dub Techno", 122, 2020, 448, 20, 6, "#dubtechno",
			[][2]any{{"Cue 1", 30}},
			nil},
		{"Static Bloom", "Aurora Kid", "Sunwalker", "House", 126, 2022, 367, 0, -1, "",
			nil, nil},
		{"Ferrite Hearts", "Cassette Ghost", "Magnetic", "Electro", 108, 2019, 315, 60, 1, "#electro #classic",
			[][2]any{{"Intro", 0}, {"Verse", 48}, {"Hook", 120}, {"Bridge", 200}},
			[][4]any{{"Hook 8", 120, 127}}},
	}
	// A second library entry pointing at the same file as track 8 gives the
	// Dedup screenshot a real duplicate group.
	tracks = append(tracks, tracks[7])
	tracks[8].title = "Ferrite Hearts (copy)"
	tracks[8].rating, tracks[8].key, tracks[8].comment = 0, -1, ""
	for i, tr := range tracks {
		fileType := "mp3"
		// Two tracks live in a "moved" folder so the Relink screenshot has
		// proposals; the DB still points at the original location.
		fileDir := filepath.Join(base, "Music", "Demo", tr.artist)
		if i == 2 || i == 6 {
			fileDir = filepath.Join(base, "Music", "DemoMoved")
		}
		// The duplicate entry (i == 8) shares the file of the track above.
		pathTitle := tr.title
		if i == 8 {
			pathTitle = "Ferrite Hearts"
		}
		audioPath := filepath.Join(fileDir, pathTitle+"."+fileType)
		fileBytes, ok := fileSize(audioPath)
		if !ok {
			var err error
			fileBytes, err = writeDemoAudio(audioPath, tr)
			if err != nil {
				return err
			}
		}
		storedRel, _ := filepath.Rel(filepath.Join(base, "Engine Library"),
			filepath.Join(base, "Music", "Demo", tr.artist, pathTitle+"."+fileType))
		res, err := db.Exec(`INSERT INTO Track (title, artist, album, filename, path, fileType, bpmAnalyzed, length, bpm, year, playOrder, genre, comment, composer, rating, key, fileBytes)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?)`,
			tr.title, tr.artist, tr.album, tr.title+"."+fileType, "../"+storedRel,
			fileType, float64(tr.bpm), tr.lengthSec, tr.bpm, tr.year, (i%4)+1, tr.genre, tr.comment, tr.rating, tr.key, fileBytes)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()

		var td []byte
		td = appendBE(td, math.Float64bits(sampleRate))
		td = appendBE(td, uint64(tr.lengthSec)*44100)

		var qc []byte
		if len(tr.cues) > 0 {
			var cues QuickCues
			for n, c := range tr.cues {
				cues.Cues = append(cues.Cues, Cue{
					Num:    n + 1,
					Label:  c[0].(string),
					Sample: float64(c[1].(int)) * 44100, // seconds → samples
					RGBA:   standardColors[n%8],
				})
			}
			qc = serializeQuickCues(cues)
		}
		var lb []byte
		if len(tr.loops) > 0 {
			var loops []Loop
			for n, l := range tr.loops {
				start := int(toFloat(l[1]) * 44100)
				end := int(toFloat(l[2]) * 44100)
				loops = append(loops, Loop{
					Num: n + 1, Label: l[0].(string),
					Start: float64(start), End: float64(end),
					StartSet: true, EndSet: true,
					RGBA: standardColors[n%8],
				})
			}
			lb = serializeLoops(loops)
		}
		active := 0
		if _, err := db.Exec(`INSERT INTO PerformanceData (trackId, trackData, quickCues, loops, activeOnLoadLoops) VALUES (?, ?, ?, ?, ?)`,
			id, td, qc, lb, active); err != nil {
			return err
		}
	}
	fmt.Printf("demo database written: %s (%d tracks)\n", path, len(tracks))
	return nil
}

// fileSize reports the size of an existing file (false when missing).
func fileSize(path string) (int64, bool) {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return 0, false
	}
	return st.Size(), true
}

// writeDemoAudio writes a small but valid MP3 file with ID3v2 tags and
// embedded cover art, returning the file size.
func writeDemoAudio(path string, tr struct {
	title, artist, album, genre string
	bpm                         int
	year                        int
	lengthSec                   int
	rating                      int64
	key                         int64
	comment                     string
	cues                        [][2]any
	loops                       [][4]any
}) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	// 40 MPEG-1 Layer III frames (128 kbps, 44.1 kHz, 417 bytes each) of
	// digital silence — enough for a real, playable-looking file.
	frame := make([]byte, 417)
	frame[0], frame[1], frame[2] = 0xFF, 0xFB, 0x90
	audio := bytes.Repeat(frame, 40)

	// Simple generated cover art (solid panel with a diagonal stripe).
	art := demoPNG(uint8(tr.bpm), uint8(tr.year), uint8(tr.lengthSec))

	tmp, err := os.CreateTemp(filepath.Dir(path), ".demo*"+filepath.Base(path))
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(audio); err != nil {
		tmp.Close()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}

	tag := id3v2.NewEmptyTag()
	applyMediaTags(tag, MediaTags{
		Title:   tr.title,
		Artist:  tr.artist,
		Album:   tr.album,
		Genre:   tr.genre,
		Year:    strconv.Itoa(tr.year),
		BPM:     strconv.Itoa(tr.bpm),
		Comment: tr.comment,
	})
	if tr.rating > 0 {
		applyPOPM(tag, tr.rating)
	}
	applyArt(tag, &MediaArt{MIME: "image/png", Data: art})
	if err := writeTagToFile(tmpName, tag); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return 0, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// demoPNG renders a small generated cover image.
func demoPNG(r, g, b uint8) []byte {
	const size = 96
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			c := color.RGBA{r, g, b, 255}
			if (x+y)/10%2 == 0 {
				c = color.RGBA{r / 2, g / 2, b / 2, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

// toFloat widens the numeric literals in the demo track table.
func toFloat(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case float64:
		return n
	}
	return 0
}

func appendBE(b []byte, v uint64) []byte {
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], v)
	return append(b, tmp[:]...)
}
