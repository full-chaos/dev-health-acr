package falkorgraph

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// projectLinkHops are the project deployment path's first two hops, read
// fused: the project's issues and their links to pull requests.
func projectLinkHops(t *testing.T) (treeHop, treeHop) {
	t.Helper()
	path, ok := treePath(treeProject, treeDeployment)
	if !ok || len(path) != 4 || !path[1].edge.link {
		t.Fatalf("project -> deployment path = %+v, want project, issue, pull request, repository, deployment", path)
	}
	return path[0], path[1]
}

// walkStepsInOrder are the step reads of the project deployment walk after
// its link read.
func walkStepsInOrder() []struct {
	name string
	step walkStep
} {
	path, _ := treePath(treeProject, treeDeployment)
	return []struct {
		name string
		step walkStep
	}{
		{"repositories of the pull requests", path[2].step},
		{"deployments of the repositories", path[3].step},
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
// read and source count: one path from the project through its issue to the
// linked pull request (the link is directed, issue to pull request), and one
// path from the project to its issue.
func TestTheProjectLinkReadIsOnePathPattern(t *testing.T) {
	feed, link := projectLinkHops(t)
	node := `\((%s):Subject \{[^{}()]*\}\)`
	linkPattern := regexp.MustCompile("^" + fmt.Sprintf(node, "a") + regexp.QuoteMeta("<-[ra:Relates]-") + fmt.Sprintf(node, "m") + regexp.QuoteMeta("-[rl:Relates]->") + fmt.Sprintf(node, "b") + "$")
	count := regexp.MustCompile("^" + fmt.Sprintf(node, "a") + regexp.QuoteMeta("<-[ra:Relates]-") + fmt.Sprintf(node, "m") + "$")
	for windowName, temporal := range walkWindows(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) {
		for name, c := range map[string]struct {
			cypher  string
			pattern *regexp.Regexp
			suffix  string
		}{
			"link read":            {linkSegmentCypher(feed, link, temporal, false), linkPattern, " SKIP $skip LIMIT $limit"},
			"restricted link read": {linkSegmentCypher(feed, link, temporal, true), linkPattern, " SKIP $skip LIMIT $limit"},
			"issue count":          {linkSourceCountCypher(feed, temporal), count, " RETURN count(DISTINCT m) AS sources"},
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

// TestTheLinkReadAdmitsOnlyTheTiersOfTheTable pins the tier filter of every
// link read, restricted or not, and the order that puts the strongest tier
// first.
func TestTheLinkReadAdmitsOnlyTheTiersOfTheTable(t *testing.T) {
	feed, link := projectLinkHops(t)
	for _, restricted := range []bool{false, true} {
		got := linkSegmentCypher(feed, link, temporalFilter{}, restricted)
		if !strings.Contains(got, "rl.property_link_provenance IN $tiers") {
			t.Errorf("restricted=%t: link read = %q, want the tier filter", restricted, got)
		}
		if !strings.Contains(got, " ORDER BY rl.property_link_provenance_rank DESC, m.canonical_id, b.canonical_id, rl.") {
			t.Errorf("restricted=%t: link read = %q, want the tier rank first", restricted, got)
		}
	}
	params := linkSegmentParams("org-1", contextfabric.SubjectRef{CanonicalID: projectAnchorID}, feed, link, 0, 10, temporalFilter{})
	if got := fmt.Sprint(params["tiers"]); got != "[native explicit_text heuristic]" {
		t.Errorf("tiers = %s, want every tier of the table, strongest first", got)
	}
}

// TestARestrictedLinkReadKeepsOnlyLinksTheGrantsCanAdmit pins the grant clause
// of a restricted caller's link read, and its absence for any other caller:
// the pull request must meet the grants, and the issue must, or have no
// repository and be linked by a tier that grants authority.
func TestARestrictedLinkReadKeepsOnlyLinksTheGrantsCanAdmit(t *testing.T) {
	feed, link := projectLinkHops(t)
	repos := func(v string) string {
		return "ANY(s IN " + v + ".authorization_repositories WHERE s IN $grantRaw OR toLower(trim(s)) IN $grantNorm OR ANY(o IN $grantOwners WHERE toLower(trim(s)) STARTS WITH o))"
	}
	clause := repos("b") + " AND (" + repos("m") + " OR ($noRepository IN m.authorization_repositories AND rl.property_link_provenance IN $authorityTiers))"
	if got := linkSegmentCypher(feed, link, temporalFilter{}, true); !strings.Contains(got, clause) {
		t.Errorf("restricted link read = %q, want the grant clause", got)
	}
	if got := linkSegmentCypher(feed, link, temporalFilter{}, false); strings.Contains(got, "$grant") || strings.Contains(got, "$authorityTiers") {
		t.Errorf("unrestricted link read = %q, want no grant clause", got)
	}
}
