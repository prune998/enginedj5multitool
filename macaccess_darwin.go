//go:build darwin

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// fullDiskAccessRestricted reports whether macOS privacy protection (TCC)
// blocks this process from reading the user's Music folder — the usual case
// for apps launched from Finder without the Full Disk Access permission.
// Missing directories are not a verdict (no Apple Music installed); only an
// existing-but-unreadable folder counts as restricted.
func fullDiskAccessRestricted() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	probes := []string{
		filepath.Join(home, "Music", "Music", "Media"),  // Music.app media tree
		filepath.Join(home, "Music", "Engine Library"),  // Engine DJ default
		filepath.Join(home, "Music", "Engine Library2"), // alternate Engine layout
		filepath.Join(home, "Music"),                    // the folder itself
	}
	for _, p := range probes {
		_, err := os.ReadDir(p)
		switch {
		case err == nil:
			return false // readable: access is fine
		case errors.Is(err, os.ErrNotExist):
			continue // not present: try the next probe
		case errors.Is(err, os.ErrPermission):
			return true // exists but blocked by TCC
		}
	}
	return false
}

// openFullDiskAccessPane opens System Settings on the Full Disk Access pane.
func openFullDiskAccessPane() error {
	return exec.Command("open", "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles").Start()
}
