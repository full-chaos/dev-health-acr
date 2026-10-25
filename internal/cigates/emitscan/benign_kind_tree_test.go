//go:build !race

package emitscan_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
)

// This check repeats a real-tree scan that TestEmitPathsMatchTheCensus already runs under the race detector (the scanner is one
// goroutine; the helper runner's goroutine and the three process-wide mutexes in typed.go are exercised by that test), so
// the race job skips it to stay inside its time guard; the plain unit shards run it on every PR that touches emitscan.
func TestNoBenignKeyCarriesAnotherKindInTheTree(t *testing.T) {
	findings, err := emitscan.Scan(benignRepoRoot(t), []string{"internal", "cmd"})
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Rule == emitscan.RuleBenignKind && strings.HasPrefix(finding.Detail, "mismatch:") {
			t.Errorf("%s: %s is logged with a value of a kind the scrubber does not exempt for that key", finding.Where(), strings.TrimPrefix(finding.Detail, "mismatch:"))
		}
	}
}
