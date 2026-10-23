// Package containergate is the one switch every container-starting test sits
// behind. A plain `go test` or `go test -run <regex>` on a host without the
// switch starts no container; hosted CI sets it and a CI job that lacks it
// fails instead of skipping. guard_test.go proves every start API in the
// module is preceded by Require.
package containergate

import (
	"os"
	"testing"
)

// Env is the switch: "1" runs container tests, "0" skips them.
const Env = "ACR_CONTAINER_TESTS"

type outcome int

const (
	run outcome = iota
	skip
	fail
)

func decide(value string, githubActions string) outcome {
	switch value {
	case "1":
		return run
	case "0":
		return skip
	case "":
		if githubActions == "true" {
			return fail
		}
		return skip
	default:
		return fail
	}
}

// Require gates a container start. Call it as a top-level statement of the
// function that starts the container, before the start.
func Require(tb testing.TB) {
	tb.Helper()
	value := os.Getenv(Env)
	switch decide(value, os.Getenv("GITHUB_ACTIONS")) {
	case run:
		return
	case skip:
		tb.Skipf("%s=1 starts container tests; not set, skipping", Env)
	default:
		tb.Fatalf("%s must be 0 or 1 (got %q); hosted CI must set it to 1", Env, value)
	}
}
