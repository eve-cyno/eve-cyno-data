//go:build race

package gofa

// raceEnabled lets slow real-SDE tests skip themselves under -race, where the
// uncached SQLite path is ~30x slower.
const raceEnabled = true
