package falkorgraph

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const removedTeamLimitation = "A named team is removed at its source: its work item, project and repository edges are kept and may be out of date; they are not retracted."

func askAboutTeamTango(t *testing.T, teamProps map[string]interface{}) routeAnswer {
	t.Helper()
	s := routeSeed{text: map[string]string{"team|team:tango": "tango"}, reach: map[string][]string{}, deployments: map[string][]string{}}
	s.nodes = append(s.nodes,
		seededNode{kind: "team", id: "team:tango", label: "tango", props: teamProps},
		seededNode{kind: "repository", id: "repository:github:acme/quiet", label: "acme/quiet", repos: []string{"acme/quiet"}})
	s.edges = append(s.edges, seededEdge{"OWNED_BY_TEAM", "repository", "repository:github:acme/quiet", "team", "team:tango", ""})
	return askAnchorDeployments(t, s, storage.Principal{OrgID: "org-1"}, contextfabric.SubjectTeam, "tango")
}

func countRemovedTeamLimitation(a routeAnswer) int {
	n := 0
	for _, l := range a.result.Limitations {
		if strings.Contains(l, removedTeamLimitation) {
			n++
		}
	}
	return n
}

func TestARemovedTeamIsFlaggedInTheServedLimitations(t *testing.T) {
	removed := askAboutTeamTango(t, map[string]interface{}{propPropertyPrefix + "gone_at": "2026-10-09 17:30:00.000000"})
	if len(removed.committed) != 1 || removed.committed[0].CanonicalID != "team:tango" {
		t.Fatalf("resolver committed %+v, want the named team", removed.committed)
	}
	if got := countRemovedTeamLimitation(removed); got != 1 {
		t.Fatalf("limitations = %q: the removed-team sentence appears %d times, want exactly once", removed.result.Limitations, got)
	}
}

func TestATeamNeverProvedGoneIsNotFlagged(t *testing.T) {
	live := askAboutTeamTango(t, nil)
	if len(live.committed) != 1 || live.committed[0].CanonicalID != "team:tango" {
		t.Fatalf("resolver committed %+v, want the named team", live.committed)
	}
	if got := countRemovedTeamLimitation(live); got != 0 {
		t.Fatalf("limitations = %q: an active team carries the removed-team sentence", live.result.Limitations)
	}
}
