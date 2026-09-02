//go:build !race

package v1

// raceDetectorEnabled is false in ordinary builds; see the //go:build race
// counterpart for why the saturation probe consults it.
const raceDetectorEnabled = false
