//go:build !darwin

package main

// fullDiskAccessRestricted is a no-op outside macOS (no TCC protection).
func fullDiskAccessRestricted() bool { return false }

// openFullDiskAccessPane is a no-op outside macOS.
func openFullDiskAccessPane() error { return nil }
