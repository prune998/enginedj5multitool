package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the persisted user configuration (config.yaml inside the per-OS
// user config directory). It is created with defaults on first run and
// rewritten on exit with the session's settings (theme, tool, browser width,
// Discogs token).
type Config struct {
	Library       string  `yaml:"library"`        // default Engine DJ database path
	MusicRoot     string  `yaml:"music_root"`     // optional root used to resolve relative track paths
	EngineLibrary string  `yaml:"engine_library"` // Engine Library folder (e.g. ~/Music/Engine Library)
	Theme         string  `yaml:"theme"`          // auto | light | dark
	Tool          int     `yaml:"tool"`           // tool tab opened at startup
	BrowserWidth  float32 `yaml:"browser_width"`  // track list width in points
	WindowWidth   float64 `yaml:"window_width"`   // main window width (saved on quit; 0 = default)
	WindowHeight  float64 `yaml:"window_height"`  // main window height (saved on quit; 0 = default)
	DiscogsToken  string  `yaml:"discogs_token"`  // personal access token for artwork search
}

func defaultConfig() Config {
	return Config{Library: "m.db", Theme: "auto", Tool: 0, BrowserWidth: 560}
}

// ConfigDir returns the per-OS user configuration directory for the app:
// ~/Library/Application Support/enginedj5multitool on macOS, ~/.config/
// enginedj5multitool on Linux, %AppData%\enginedj5multitool on Windows.
// The ENGINDJ5_CONFIG_DIR environment variable overrides it.
func ConfigDir() (string, error) {
	if v := os.Getenv("ENGINDJ5_CONFIG_DIR"); v != "" {
		return v, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "enginedj5multitool"), nil
}

// ConfigPath returns the full path of config.yaml.
func ConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// LoadedConfig bundles the parsed configuration with its file location and
// load status.
type LoadedConfig struct {
	Config
	Path    string
	Created bool
	Err     error // non-nil when defaults are in use (unreadable or malformed file)
}

// LoadOrCreateConfig loads config.yaml, creating it with defaults when
// missing. A malformed file is reported as an error and defaults are
// returned; the file is left untouched so user edits are not lost.
func LoadOrCreateConfig() LoadedConfig {
	lc := LoadedConfig{Config: defaultConfig()}
	path, perr := ConfigPath()
	if perr != nil {
		lc.Err = fmt.Errorf("cannot resolve config dir: %w", perr)
		return lc
	}
	lc.Path = path
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			lc.Err = fmt.Errorf("read config: %w", err)
			return lc
		}
		if werr := SaveConfigFile(path, lc.Config); werr != nil {
			lc.Err = fmt.Errorf("create default config: %w", werr)
			return lc
		}
		lc.Created = true
		return lc
	}
	if err := yaml.Unmarshal(data, &lc.Config); err != nil {
		lc.Config = defaultConfig()
		lc.Err = fmt.Errorf("parse %s: %w (using defaults; the file was left untouched)", path, err)
		return lc
	}
	// Zero values fall back to defaults.
	def := defaultConfig()
	if lc.Library == "" {
		lc.Library = def.Library
	}
	if lc.EngineLibrary == "" {
		lc.EngineLibrary = defaultEngineLibrary()
	}
	if lc.Theme == "" {
		lc.Theme = def.Theme
	}
	if lc.Tool < 0 {
		lc.Tool = def.Tool
	}
	if lc.BrowserWidth <= 0 {
		lc.BrowserWidth = def.BrowserWidth
	}
	return lc
}

// defaultEngineLibrary returns the standard Engine Library folder
// (~/Music/Engine Library) when it exists, else "".
func defaultEngineLibrary() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, "Music", "Engine Library")
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		return p
	}
	return ""
}

// SaveConfigFile writes the config as YAML, creating the directory if needed.
func SaveConfigFile(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("# enginedj5multitool configuration (created automatically; safe to edit)\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
