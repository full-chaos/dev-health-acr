//go:build race

package devhealthschema_test

// The go tool sets the "race" build tag automatically whenever -race is
// passed to `go build`/`go test` -- no explicit -tags flag needed. This file
// and its !race twin give the test package a single compile-time constant to
// branch on, rather than each test guessing at -race some other way.
const raceDetectorEnabled = true
