package falkorgraph

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// walkStepsInOrder are the four step reads of the project deployment walk.
func walkStepsInOrder() []struct {
	name string
	step walkStep
} {
	return []struct {
		name string
		step walkStep
	}{
		{"issues of the project", projectIssuesStep},
		{"pull requests linked to the issues", issuePullRequestsStep},
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
