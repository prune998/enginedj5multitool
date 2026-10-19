package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// bracketRe matches one balanced (), {} or [] group (no nesting inside).
var bracketRe = regexp.MustCompile(`\([^()]*\)|\{[^{}]*\}|\[[^\[\]]*\]`)

// stripBrackets removes every (), {} and [] group (including the brackets)
// from a search term — repeatedly, so nested groups are handled too — and
// collapses the remaining whitespace. "Behind The Wheel (Extended Mix)"
// becomes "Behind The Wheel".
func stripBrackets(s string) string {
	prev := s
	for {
		s = bracketRe.ReplaceAllString(s, " ")
		if s == prev {
			break
		}
		prev = s
	}
	return strings.Join(strings.Fields(s), " ")
}

// artwork.go: search cover art candidates for a release on MusicBrainz (via
// the Cover Art Archive) and Discogs, download the images, plus helpers to
// sniff and decode images.

var (
	musicBrainzBaseURL = "https://musicbrainz.org/ws/2"
	coverArtURL        = "https://coverartarchive.org/release"
	discogsSearchURL   = "https://api.discogs.com/database/search"
)

const userAgent = "enginedj5multitool/1.0"

var artHTTPClient = &http.Client{Timeout: 20 * time.Second}

// MediaArt (defined in mp3tags.go) carries the raw image bytes + MIME type.

func httpGetBody(url string, headers map[string]string) ([]byte, string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := artHTTPClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", fmt.Errorf("not found (404)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, hostOf(url))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Host
}

// sniffImageMIME detects the image format from magic bytes, falling back to
// the HTTP Content-Type. Returns "" when it does not look like a supported
// raster image.
func sniffImageMIME(data []byte, contentType string) string {
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}):
		return "image/png"
	case len(data) >= 6 && (bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a"))):
		return "image/gif"
	}
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "image/jpeg", "image/jpg", "image/png", "image/gif":
		return ct
	}
	return ""
}

// decodeRGBA decodes image bytes into an RGBA image for shirei rendering.
func decodeRGBA(data []byte) *image.RGBA {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	return rgba
}

// artCandidate is one artwork search result: the provider it came from and
// the URL to download the image from.
type artCandidate struct {
	Source string // "MusicBrainz" or "Discogs"
	URL    string
}

const (
	// Per-provider cap on the candidates kept for the ‹ › picker.
	maxArtCandidatesPerSource = 6
	// Total cap (MusicBrainz candidates come first, then Discogs).
	maxArtCandidates = 12
)

// musicBrainzArtCandidates searches the recording by artist + song title
// (topped up / falling back to a release search on artist + album) and
// returns the front-cover URL of every matching release on the Cover Art
// Archive, best match first. Many releases have no cover on the archive —
// those URLs simply fail when downloaded, and the UI skips them.
func musicBrainzArtCandidates(artist, title, album string) ([]artCandidate, error) {
	var ids []string
	seen := map[string]bool{}
	addID := func(id string) {
		if id == "" || seen[id] || len(ids) >= maxArtCandidatesPerSource {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}

	// 1) Recording search by song title: every release of every recording.
	q := url.Values{}
	q.Set("query", fmt.Sprintf(`artist:"%s" AND recording:"%s"`, artist, title))
	q.Set("fmt", "json")
	q.Set("limit", "5")
	data, _, err := httpGetBody(musicBrainzBaseURL+"/recording?"+q.Encode(), map[string]string{
		"Accept": "application/json",
	})
	if err == nil {
		var res struct {
			Recordings []struct {
				Releases []struct {
					ID string `json:"id"`
				} `json:"releases"`
			} `json:"recordings"`
		}
		if json.Unmarshal(data, &res) == nil {
			for _, rec := range res.Recordings {
				for _, rel := range rec.Releases {
					addID(rel.ID)
				}
			}
		}
	}

	// 2) Top-up / fallback: release search by album.
	if len(ids) < maxArtCandidatesPerSource && strings.TrimSpace(album) != "" {
		q := url.Values{}
		q.Set("query", fmt.Sprintf(`artist:"%s" AND release:"%s"`, artist, album))
		q.Set("fmt", "json")
		q.Set("limit", "5")
		data, _, err := httpGetBody(musicBrainzBaseURL+"/release?"+q.Encode(), map[string]string{
			"Accept": "application/json",
		})
		if err == nil {
			var res struct {
				Releases []struct {
					ID string `json:"id"`
				} `json:"releases"`
			}
			if json.Unmarshal(data, &res) == nil {
				for _, rel := range res.Releases {
					addID(rel.ID)
				}
			}
		}
	}

	if len(ids) == 0 {
		return nil, errors.New("no MusicBrainz release found for this artist/title")
	}
	cands := make([]artCandidate, len(ids))
	for i, id := range ids {
		cands[i] = artCandidate{
			Source: "MusicBrainz",
			URL:    fmt.Sprintf("%s/%s/front-500", coverArtURL, url.PathEscape(id)),
		}
	}
	return cands, nil
}

// discogsArtCandidates searches the release by artist + song title (falling
// back to artist + album) and returns the cover-image URL of every match.
// Requires a personal API token from discogs.com/settings/developers.
func discogsArtCandidates(artist, title, album, token string) ([]artCandidate, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("Discogs requires a personal access token (discogs.com → Settings → Developers)")
	}
	search := func(query url.Values) ([]string, error) {
		q := query
		q.Set("type", "release")
		q.Set("per_page", "10")
		data, _, err := httpGetBody(discogsSearchURL+"?"+q.Encode(), map[string]string{
			"Authorization": "Discogs token=" + strings.TrimSpace(token),
			"Accept":        "application/json",
		})
		if err != nil {
			return nil, err
		}
		var res struct {
			Results []struct {
				CoverImage string `json:"cover_image"`
			} `json:"results"`
		}
		if err := json.Unmarshal(data, &res); err != nil {
			return nil, err
		}
		var out []string
		seen := map[string]bool{}
		for _, r := range res.Results {
			if r.CoverImage == "" || seen[r.CoverImage] {
				continue
			}
			seen[r.CoverImage] = true
			out = append(out, r.CoverImage)
			if len(out) >= maxArtCandidatesPerSource {
				break
			}
		}
		return out, nil
	}

	// 1) Song title search.
	q := url.Values{}
	q.Set("artist", artist)
	q.Set("track", title)
	covers, err := search(q)

	// 2) Fallback: album search.
	if len(covers) == 0 && strings.TrimSpace(album) != "" {
		q := url.Values{}
		q.Set("artist", artist)
		q.Set("release_title", album)
		covers, err = search(q)
	}
	if err != nil {
		return nil, fmt.Errorf("Discogs search: %w", err)
	}
	if len(covers) == 0 {
		return nil, errors.New("no Discogs release found for this artist/title")
	}
	cands := make([]artCandidate, len(covers))
	for i, cover := range covers {
		cands[i] = artCandidate{Source: "Discogs", URL: cover}
	}
	return cands, nil
}

// downloadImage fetches an image URL and sniffs the image type.
func downloadImage(imageURL string) (*MediaArt, error) {
	data, ct, err := httpGetBody(imageURL, nil)
	if err != nil {
		return nil, err
	}
	mime := sniffImageMIME(data, ct)
	if mime == "" {
		return nil, errors.New("unsupported image format")
	}
	return &MediaArt{MIME: mime, Data: data}, nil
}
