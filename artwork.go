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
	"strings"
	"time"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// artwork.go: download cover art for a release from MusicBrainz (via the
// Cover Art Archive) and Discogs, plus helpers to sniff and decode images.

var (
	musicBrainzBaseURL = "https://musicbrainz.org/ws/2"
	coverArtURL        = "https://coverartarchive.org/release"
	discogsSearchURL   = "https://api.discogs.com/database/search"
)

const userAgent = "enginedj5multitool/1.0 ( https://github.com/prune998/enginedj5multitool )"

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

// fetchArtMusicBrainz searches the recording by artist + song title (falling
// back to a release search on artist + album) and downloads the front cover
// of the first matching release from the Cover Art Archive.
func fetchArtMusicBrainz(artist, title, album string) (*MediaArt, error) {
	releaseID := ""

	// 1) Recording search by song title.
	q := url.Values{}
	q.Set("query", fmt.Sprintf(`artist:"%s" AND recording:"%s"`, artist, title))
	q.Set("fmt", "json")
	q.Set("limit", "1")
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
		if json.Unmarshal(data, &res) == nil &&
			len(res.Recordings) > 0 && len(res.Recordings[0].Releases) > 0 {
			releaseID = res.Recordings[0].Releases[0].ID
		}
	}

	// 2) Fallback: release search by album.
	if releaseID == "" && strings.TrimSpace(album) != "" {
		q := url.Values{}
		q.Set("query", fmt.Sprintf(`artist:"%s" AND release:"%s"`, artist, album))
		q.Set("fmt", "json")
		q.Set("limit", "1")
		data, _, err := httpGetBody(musicBrainzBaseURL+"/release?"+q.Encode(), map[string]string{
			"Accept": "application/json",
		})
		if err == nil {
			var res struct {
				Releases []struct {
					ID string `json:"id"`
				} `json:"releases"`
			}
			if json.Unmarshal(data, &res) == nil && len(res.Releases) > 0 {
				releaseID = res.Releases[0].ID
			}
		}
	}

	if releaseID == "" {
		return nil, errors.New("no MusicBrainz release found for this artist/title")
	}
	artURL := fmt.Sprintf("%s/%s/front-500", coverArtURL, url.PathEscape(releaseID))
	imgData, ct, err := httpGetBody(artURL, nil)
	if err != nil {
		return nil, fmt.Errorf("Cover Art Archive: %w", err)
	}
	mime := sniffImageMIME(imgData, ct)
	if mime == "" {
		return nil, errors.New("Cover Art Archive returned an unsupported image format")
	}
	return &MediaArt{MIME: mime, Data: imgData}, nil
}

// fetchArtDiscogs searches the release by artist + song title (falling back
// to artist + album) and downloads its cover image. Requires a personal API
// token from discogs.com/settings/developers.
func fetchArtDiscogs(artist, title, album, token string) (*MediaArt, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("Discogs requires a personal access token (discogs.com → Settings → Developers)")
	}
	search := func(query url.Values) (string, error) {
		q := query
		q.Set("type", "release")
		q.Set("per_page", "1")
		data, _, err := httpGetBody(discogsSearchURL+"?"+q.Encode(), map[string]string{
			"Authorization": "Discogs token=" + strings.TrimSpace(token),
			"Accept":        "application/json",
		})
		if err != nil {
			return "", err
		}
		var res struct {
			Results []struct {
				CoverImage string `json:"cover_image"`
			} `json:"results"`
		}
		if err := json.Unmarshal(data, &res); err != nil {
			return "", err
		}
		if len(res.Results) == 0 {
			return "", errors.New("no results")
		}
		return res.Results[0].CoverImage, nil
	}

	// 1) Song title search.
	q := url.Values{}
	q.Set("artist", artist)
	q.Set("track", title)
	cover, err := search(q)

	// 2) Fallback: album search.
	if err != nil && strings.TrimSpace(album) != "" {
		q := url.Values{}
		q.Set("artist", artist)
		q.Set("release_title", title)
		cover, err = search(q)
	}
	if err != nil {
		return nil, fmt.Errorf("Discogs search: %w", err)
	}
	if cover == "" {
		return nil, errors.New("no Discogs release found for this artist/title")
	}
	imgData, ct, err := httpGetBody(cover, nil)
	if err != nil {
		return nil, fmt.Errorf("Discogs cover: %w", err)
	}
	mime := sniffImageMIME(imgData, ct)
	if mime == "" {
		return nil, errors.New("Discogs returned an unsupported image format")
	}
	return &MediaArt{MIME: mime, Data: imgData}, nil
}
