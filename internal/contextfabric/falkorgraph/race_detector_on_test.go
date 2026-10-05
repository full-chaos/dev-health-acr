//go:build race

package falkorgraph

// raceDetectorEnabled: this test binary runs under the race detector, where
// wall times are several times production's and a time budget is not measured.
const raceDetectorEnabled = true
