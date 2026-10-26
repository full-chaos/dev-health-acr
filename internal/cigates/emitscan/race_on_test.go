//go:build race

package emitscan_test

// raceSlowdown scales the wall-time limits of the cost rows: the race detector slows the scan several times over.
const raceSlowdown = 10

// builtWithRace is true in a binary built with -race.
const builtWithRace = true
