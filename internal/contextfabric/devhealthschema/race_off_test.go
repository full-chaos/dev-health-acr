//go:build !race

package devhealthschema_test

// See race_on_test.go: the two files give the package one compile-time
// constant to read instead of guessing at -race some other way.
const raceDetectorEnabled = false
