// Container-test switch. One env switch sits in front of every
// container-starting test. A plain `go test` or `go test -run <regex>` on a host without the
// switch starts no container; hosted CI sets it and a CI job that lacks it
// fails instead of skipping. containers_guard_test.go proves every start API in the
// module is preceded by RequireContainers.
package chfixture

import (
	"os"
	"testing"
)

// ContainerTestsEnv is the switch: "1" runs container tests, "0" skips them.
const ContainerTestsEnv = "ACR_CONTAINER_TESTS"

type containerOutcome int

const (
	containersRun containerOutcome = iota
	containersSkip
	containersFail
)

func decideContainers(value string, githubActions string) containerOutcome {
	switch value {
	case "1":
		return containersRun
	case "0":
		return containersSkip
	case "":
		if githubActions == "true" {
			return containersFail
		}
		return containersSkip
	default:
		return containersFail
	}
}

// RequireContainers gates a container start. Call it as a top-level statement of the
// function that starts the container, before the start.
func RequireContainers(tb testing.TB) {
	tb.Helper()
	value := os.Getenv(ContainerTestsEnv)
	switch decideContainers(value, os.Getenv("GITHUB_ACTIONS")) {
	case containersRun:
		return
	case containersSkip:
		tb.Skipf("%s=1 starts container tests; not set, skipping", ContainerTestsEnv)
	default:
		tb.Fatalf("%s must be 0 or 1 (got %q); hosted CI must set it to 1", ContainerTestsEnv, value)
	}
}
