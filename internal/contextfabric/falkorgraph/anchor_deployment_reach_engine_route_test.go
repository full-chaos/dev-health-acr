package falkorgraph

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// busyTeam seeds the team "tango" with workItems work items of its own (their
// ownership edges listed first, so they rank ahead in a generic walk) and
// repositories repositories it owns, each with two deployments, plus a
// repository it does not own.
func busyTeam(workItems, repositories int) (routeSeed, []string) {
	s := routeSeed{text: map[string]string{}, reach: map[string][]string{}, deployments: map[string][]string{}}
	s.nodes = append(s.nodes, seededNode{kind: "team", id: "team:tango", label: "tango"})
	s.text["team|team:tango"] = "tango"
	for i := 0; i < workItems; i++ {
		id := fmt.Sprintf("work_item:gh:busy-%03d", i)
		s.nodes = append(s.nodes, seededNode{kind: "work_item", id: id, label: id, repos: []string{"acme/busy"}, workItemType: "issue"})
		s.edges = append(s.edges, seededEdge{"OWNED_BY_TEAM", "work_item", id, "team", "team:tango", ""})
	}
	var owned []string
	for r := 0; r <= repositories; r++ {
		slug := fmt.Sprintf("acme/service-%02d", r)
		repoID := "repository:github:" + slug
		s.nodes = append(s.nodes, seededNode{kind: "repository", id: repoID, label: slug, repos: []string{slug}})
		if r < repositories {
			s.edges = append(s.edges, seededEdge{"OWNED_BY_TEAM", "repository", repoID, "team", "team:tango", ""})
		}
		for d := 0; d < 2; d++ {
			depID := fmt.Sprintf("deployment:%s:%d", slug, d)
			s.nodes = append(s.nodes, seededNode{kind: "deployment", id: depID, label: depID, repos: []string{slug}})
			s.text["deployment|"+depID] = "deployments production"
			s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "deployment", depID, "repository", repoID, ""})
			if r < repositories {
				owned = append(owned, depID)
			}
		}
	}
	sort.Strings(owned)
	return s, owned
}

func TestABusyTeamServesTheDeploymentsOfTheRepositoriesItOwnsThroughTheEngine(t *testing.T) {
	s, owned := busyTeam(40, 3)
	answer := askAnchorDeployments(t, s, storage.Principal{OrgID: "org-1"}, contextfabric.SubjectTeam, "tango")
	if len(answer.committed) != 1 || answer.committed[0].CanonicalID != "team:tango" {
		t.Fatalf("resolver committed %+v, want the named team", answer.committed)
	}
	if got := answer.members(); strings.Join(got, ",") != strings.Join(owned, ",") || !answer.result.Cohort.Complete {
		t.Fatalf("served %v, want the %d deployments of the 3 owned repositories as a complete cohort: the team's work items must not use up the read", got, len(owned))
	}
	if len(answer.walkLines) != 1 || answer.walkLines[0]["outcome"] != "members" || answer.walkLines[0]["anchor_kind"] != "team" {
		t.Fatalf("walk lines = %v, want one members line for the team anchor", answer.walkLines)
	}
	ownership := 0
	for _, path := range answer.result.Paths {
		for _, e := range path.Edges {
			if e.Type == contractsv1.ContextFabricRelationshipOwnedByTeam && e.To.CanonicalID == "team:tango" && e.From.Kind == contextfabric.SubjectRepository {
				ownership++
			}
		}
	}
	if ownership != 3 {
		t.Fatalf("paths carry %d repository ownership edges to the team, want the 3 owned repositories' edges", ownership)
	}
}

func TestATeamOwningMoreRepositoriesThanTheBudgetReadsCutThroughTheEngine(t *testing.T) {
	s, _ := busyTeam(0, 30)
	answer := askAnchorDeployments(t, s, storage.Principal{OrgID: "org-1"}, contextfabric.SubjectTeam, "tango")
	if answer.result.Cohort == nil || answer.result.Cohort.Complete || !answer.result.Cohort.Truncated {
		t.Fatalf("cohort = %+v, want a truncated cohort: the team owns more repositories than the read budget", answer.result.Cohort)
	}
	if len(answer.walkLines) != 1 || answer.walkLines[0]["truncated"] != true {
		t.Fatalf("walk lines = %v, want one line recording the cut", answer.walkLines)
	}
}

func TestARestrictedCallerOfATeamWhoseRepositoriesAreHiddenGetsOnlyTheNeutralReason(t *testing.T) {
	s, _ := busyTeam(0, 3)
	// The caller sees the team through a repository the team's list names,
	// and none of the repositories the team's ownership edges reach.
	s.nodes[0].repos = []string{"acme/elsewhere"}
	answer := askAnchorDeployments(t, s, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/elsewhere"}}, contextfabric.SubjectTeam, "tango")
	if len(answer.committed) != 1 || answer.committed[0].CanonicalID != "team:tango" {
		t.Fatalf("resolver committed %+v, want the named team: the fixture must reach the read", answer.committed)
	}
	if got := answer.members(); len(got) != 0 {
		t.Fatalf("served %v, want no member: the caller is granted none of the team's repositories", got)
	}
	denied := answer.detail(contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization)
	if denied == nil || denied.Count == nil || *denied.Count != 0 || len(answer.result.Coverage.Details) != 1 {
		t.Fatalf("coverage details = %+v, want only the neutral denied reason with no count", answer.result.Coverage.Details)
	}
	if len(answer.walkLines) != 1 || answer.walkLines[0]["outcome"] != "denied" || answer.walkLines[0]["anchor_kind"] != "team" {
		t.Fatalf("walk lines = %v, want one denied line for the team anchor", answer.walkLines)
	}
}

func TestATeamWhoseRepositoriesHoldNoDeploymentIsNoDeploymentsNotUnlinked(t *testing.T) {
	s := routeSeed{text: map[string]string{"team|team:tango": "tango"}, reach: map[string][]string{}, deployments: map[string][]string{}}
	s.nodes = append(s.nodes,
		seededNode{kind: "team", id: "team:tango", label: "tango"},
		seededNode{kind: "repository", id: "repository:github:acme/quiet", label: "acme/quiet", repos: []string{"acme/quiet"}})
	s.edges = append(s.edges, seededEdge{"OWNED_BY_TEAM", "repository", "repository:github:acme/quiet", "team", "team:tango", ""})
	answer := askAnchorDeployments(t, s, storage.Principal{OrgID: "org-1"}, contextfabric.SubjectTeam, "tango")
	if len(answer.walkLines) != 1 || answer.walkLines[0]["outcome"] != "no_deployments" {
		t.Fatalf("walk lines = %v, want one no_deployments line", answer.walkLines)
	}
	if answer.detail(contractsv1.ContextFabricCoverageDetailGraphProjectDeploymentsUnlinked) != nil {
		t.Fatal("a team anchor has no issue links to lack: the unlinked limitation belongs to a project")
	}
}

func TestANamedRepositoryServesItsOwnDeploymentsThroughTheEngine(t *testing.T) {
	s, _ := busyTeam(40, 1)
	s.text["repository|repository:github:acme/service-00"] = "acme service 00"
	answer := askAnchorDeployments(t, s, storage.Principal{OrgID: "org-1"}, contextfabric.SubjectRepository, "acme/service-00")
	want := []string{"deployment:acme/service-00:0", "deployment:acme/service-00:1"}
	if got := answer.members(); strings.Join(got, ",") != strings.Join(want, ",") || !answer.result.Cohort.Complete {
		t.Fatalf("served %v, want %v as a complete cohort", got, want)
	}
	if len(answer.walkLines) != 1 || answer.walkLines[0]["outcome"] != "members" || answer.walkLines[0]["anchor_kind"] != "repository" {
		t.Fatalf("walk lines = %v, want one members line for the repository anchor", answer.walkLines)
	}
}
