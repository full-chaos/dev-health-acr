//go:build race

package v1

// raceDetectorEnabled is true in binaries built with -race.
//
// The saturation probe rebuilds a ~520MB document once per probed field. Under
// the race detector that is roughly an order of magnitude slower and blows the
// package test timeout -- CI's `race (shard 1 of 4)` died at 420s doing exactly
// this. -race does NOT imply -short, so the -short guard alone does not cover
// it and the job failed while the -short and default runs both passed.
const raceDetectorEnabled = true
