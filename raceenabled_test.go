//go:build race

package main

// raceEnabled is true when the test binary is built with -race. The drive
// harness mutates global shirei state and reads app fields across goroutines,
// which the race detector flags; such tests skip themselves in that mode.
const raceEnabled = true
