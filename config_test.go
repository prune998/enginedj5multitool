package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateConfigCreatesDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ENGINDJ5_CONFIG_DIR", dir)

	lc := LoadOrCreateConfig()
	if lc.Err != nil {
		t.Fatalf("load: %v", lc.Err)
	}
	if !lc.Created {
		t.Error("expected Created = true on first run")
	}
	if lc.Library != "m.db" || lc.Theme != "auto" || lc.Tool != 0 || lc.BrowserWidth != 560 {
		t.Errorf("defaults = %+v", lc.Config)
	}
	if lc.FontFamily != "" || lc.FontSize != 0 {
		t.Errorf("font defaults = %q/%d, want empty/0", lc.FontFamily, lc.FontSize)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("default file not created: %v", err)
	}
	for _, want := range []string{"library: m.db", "theme: auto", "browser_width: 560"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("default config missing %q:\n%s", want, data)
		}
	}
}

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ENGINDJ5_CONFIG_DIR", dir)

	LoadOrCreateConfig() // create

	lc := LoadOrCreateConfig()
	lc.Library = "/music/Engine Library/m.db"
	lc.MusicRoot = "/music"
	lc.Theme = "dark"
	lc.Tool = 1
	lc.BrowserWidth = 700
	lc.FontFamily = "Courier New"
	lc.FontSize = 18
	lc.DiscogsToken = "tok-123"
	if err := SaveConfigFile(lc.Path, lc.Config); err != nil {
		t.Fatal(err)
	}

	lc2 := LoadOrCreateConfig()
	if lc2.Created {
		t.Error("second load must not report Created")
	}
	if lc2.Library != "/music/Engine Library/m.db" || lc2.MusicRoot != "/music" ||
		lc2.Theme != "dark" || lc2.Tool != 1 || lc2.BrowserWidth != 700 ||
		lc2.FontFamily != "Courier New" || lc2.FontSize != 18 ||
		lc2.DiscogsToken != "tok-123" {
		t.Errorf("round trip mismatch: %+v", lc2.Config)
	}
}

func TestConfigMalformedPreserved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ENGINDJ5_CONFIG_DIR", dir)
	path := filepath.Join(dir, "config.yaml")
	garbage := "library: [unclosed\n\tbroken: {{{"
	if err := os.WriteFile(path, []byte(garbage), 0o644); err != nil {
		t.Fatal(err)
	}

	lc := LoadOrCreateConfig()
	if lc.Err == nil {
		t.Fatal("expected a parse error")
	}
	if lc.Library != "m.db" || lc.Theme != "auto" {
		t.Errorf("expected defaults on parse error, got %+v", lc.Config)
	}
	// The malformed file must be left untouched (user edits preserved).
	data, _ := os.ReadFile(path)
	if string(data) != garbage {
		t.Errorf("malformed file was overwritten:\n%s", data)
	}
}

func TestConfigZeroValuesFallBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ENGINDJ5_CONFIG_DIR", dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("library: \"\"\ntheme: \"\"\ntool: -3\nbrowser_width: 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lc := LoadOrCreateConfig()
	if lc.Err != nil {
		t.Fatalf("load: %v", lc.Err)
	}
	if lc.Library != "m.db" || lc.Theme != "auto" || lc.Tool != 0 || lc.BrowserWidth != 560 {
		t.Errorf("zero values did not fall back: %+v", lc.Config)
	}
}

func TestConfigDirEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ENGINDJ5_CONFIG_DIR", dir)
	got, err := ConfigDir()
	if err != nil || got != dir {
		t.Errorf("ConfigDir = %q, %v; want %q", got, err, dir)
	}
}
