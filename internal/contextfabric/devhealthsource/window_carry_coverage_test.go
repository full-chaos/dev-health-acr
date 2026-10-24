package devhealthsource_test

import (
	"fmt"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// carryCoverage counts, over every trial of one producer in one statement set,
// what the parity oracle actually exercised. A parity oracle whose trials stop
// crossing a tie or an arm still passes; these counts are what fail instead.
type carryCoverage struct {
	complete   int
	nonEmpty   int
	tieKept    int
	tieDropped int
	arms       map[string]int
}

func (c *carryCoverage) add(result devhealthsource.CarryTrialForTest) {
	if result.Skipped {
		return
	}
	c.complete++
	if result.NonEmpty && result.Equal {
		c.nonEmpty++
	}
	c.tieKept += result.TieKept
	c.tieDropped += result.TieDropped
	for arm, n := range result.Arms {
		if c.arms == nil {
			c.arms = map[string]int{}
		}
		c.arms[arm] += n
	}
}

func (c carryCoverage) String() string {
	names := make([]string, 0, len(c.arms))
	for arm := range c.arms {
		names = append(names, arm)
	}
	sort.Strings(names)
	arms := ""
	for _, arm := range names {
		arms += fmt.Sprintf(" arm_%s=%d", arm, c.arms[arm])
	}
	return fmt.Sprintf("complete=%d non_empty_equal=%d tie_kept=%d tie_dropped=%d%s", c.complete, c.nonEmpty, c.tieKept, c.tieDropped, arms)
}

// gaps lists every coverage requirement the trials did not meet; requiredArms
// is empty for a producer without statement arms.
func (c carryCoverage) gaps(requiredArms []string) []string {
	var out []string
	if c.complete < 8 {
		out = append(out, fmt.Sprintf("only %d trials read a complete table; the seed or the limits are too small to decide", c.complete))
	}
	if c.nonEmpty < 5 {
		out = append(out, fmt.Sprintf("only %d trials compared a non-empty page; the parity of empty pages decides nothing", c.nonEmpty))
	}
	if c.tieKept == 0 {
		out = append(out, "no trial kept a row tied with the cursor's timestamp: the key tie rule was never exercised")
	}
	if c.tieDropped == 0 {
		out = append(out, "no trial dropped a row tied with the cursor's timestamp: the key tie rule was never exercised")
	}
	for _, arm := range requiredArms {
		if c.arms[arm] == 0 {
			out = append(out, fmt.Sprintf("membership arm %s was never read and compared", arm))
		}
	}
	return out
}
