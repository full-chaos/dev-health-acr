package falkorgraph

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// walkStepsInOrder are the step reads of the project deployment walk after
// its link read.
func walkStepsInOrder() []struct {
	name string
	step walkStep
} {
	return []struct {
		name string
		step walkStep
	}{
		{"repositories of the pull requests", pullRequestRepositoriesStep},
		{"deployments of the repositories", repositoryDeploymentsStep},
	}
}

func walkWindows(now time.Time) map[string]temporalFilter {
	start, end := now.Add(-24*time.Hour), now.Add(24*time.Hour)
	return map[string]temporalFilter{
		"current view": {},
		"window":       newTemporalFilter(contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}),
	}
}

// TestEveryWalkStepReadIsOnePathPattern pins the grammar of the step read's
// MATCH clause: the origin node, one relationship in the step's direction, the
// neighbour node, and nothing else. A connection double does not parse the
// query, so a second node pattern written beside the first passed every test
// while the graph store refused the read.
func TestEveryWalkStepReadIsOnePathPattern(t *testing.T) {
	arrows := map[walkDirection]string{walkOut: `-[r:Relates]->`, walkIn: `<-[r:Relates]-`, walkEither: `-[r:Relates]-`}
	for windowName, temporal := range walkWindows(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) {
		if windowName == "window" && !temporal.active {
			t.Fatal("the window fixture is not an active filter")
		}
		for _, s := range walkStepsInOrder() {
			cypher := walkStepCypher(s.step, temporal)
			from, to := strings.Index(cypher, " MATCH "), strings.Index(cypher, " WHERE ")
			if !strings.HasPrefix(cypher, "UNWIND $ids AS id MATCH ") || to < from {
				t.Fatalf("%s, %s: read = %q, want UNWIND ... MATCH ... WHERE", s.name, windowName, cypher)
			}
			pattern := cypher[from+len(" MATCH ") : to]
			want := `^\(a:Subject \{[^{}()]*\}\)` + regexp.QuoteMeta(arrows[s.step.direction]) + `\(b:Subject \{[^{}()]*\}\)$`
			if !regexp.MustCompile(want).MatchString(pattern) {
				t.Errorf("%s, %s: MATCH pattern = %q, want one path: the origin node, %s, the neighbour node", s.name, windowName, pattern, arrows[s.step.direction])
			}
			if !strings.HasSuffix(cypher, " LIMIT $limit") || !strings.Contains(cypher, " ORDER BY id, b.") {
				t.Errorf("%s, %s: read = %q, want a deterministic order and a bound", s.name, windowName, cypher)
			}
		}
	}
}

// TestTheProjectLinkReadIsOnePathPattern pins the grammar of the walk's link
// read and issue count: one path from the project through its issue to the
// linked pull request, and one path from the project to its issue.
func TestTheProjectLinkReadIsOnePathPattern(t *testing.T) {
	node := `\((%s):Subject \{[^{}()]*\}\)`
	link := regexp.MustCompile("^" + fmt.Sprintf(node, "p") + regexp.QuoteMeta("<-[rp:Relates]-") + fmt.Sprintf(node, "i") + regexp.QuoteMeta("-[rl:Relates]-") + fmt.Sprintf(node, "pr") + "$")
	count := regexp.MustCompile("^" + fmt.Sprintf(node, "p") + regexp.QuoteMeta("<-[rp:Relates]-") + fmt.Sprintf(node, "i") + "$")
	for windowName, temporal := range walkWindows(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) {
		for name, c := range map[string]struct {
			cypher  string
			pattern *regexp.Regexp
			suffix  string
		}{
			"link read":   {projectLinkCypher(temporal), link, " SKIP $skip LIMIT $limit"},
			"issue count": {projectIssueCountCypher(temporal), count, " RETURN count(DISTINCT i) AS issues"},
		} {
			from, to := strings.Index(c.cypher, "MATCH "), strings.Index(c.cypher, " WHERE ")
			if from != 0 || to < from {
				t.Fatalf("%s, %s: read = %q, want MATCH ... WHERE", name, windowName, c.cypher)
			}
			if pattern := c.cypher[len("MATCH "):to]; !c.pattern.MatchString(pattern) {
				t.Errorf("%s, %s: MATCH pattern = %q, want one path", name, windowName, pattern)
			}
			if !strings.HasSuffix(c.cypher, c.suffix) {
				t.Errorf("%s, %s: read = %q, want it to end %q", name, windowName, c.cypher, c.suffix)
			}
		}
	}
}
