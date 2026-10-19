package devhealthsource

import (
	"os"
	"testing"
)

// FIXTURE_MODE shapes one real `go test -json` run of this package:
// pass, skip (a top-level test skips), subskip (a subtest skips), fail.
func mode() string { return os.Getenv("FIXTURE_MODE") }

func TestAlpha(t *testing.T) {}

func TestBravo(t *testing.T) {
	t.Run("one", func(t *testing.T) {})
	t.Run("two", func(t *testing.T) {
		if mode() == "subskip" {
			t.Skip("skipped inside a subtest")
		}
	})
}

func TestCharlie(t *testing.T) {
	if mode() == "skip" {
		t.Skip("skipped under the race build")
	}
	if mode() == "fail" {
		t.Fatal("fails")
	}
}
