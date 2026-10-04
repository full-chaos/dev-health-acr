package falkorgraph

import (
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	routeOwnedRepository = "repository:github:acme/alpha-service"
	routeOwnedSlug       = "acme/alpha-service"
)

// seedOwnedRepository adds, to the two linked projects, the owner of the alpha
// repository and a team that owns another repository and whose indexed text
// matches the question. The alpha repository is indexed under its own slug.
func seedOwnedRepository() routeSeed {
	s := seedTwoLinkedProjects()
	s.text["repository|"+routeOwnedRepository] = "acme alpha service"
	s.nodes = append(s.nodes,
		seededNode{kind: "team", id: "team:owner", label: "owner", repos: []string{routeOwnedSlug}},
		seededNode{kind: "team", id: "team:matched", label: "matched", repos: []string{"acme/bravo-service"}})
	s.text["team|team:matched"] = "teams own"
	return s
}

// askOwningTeams runs "which teams own repository <slug>" through
// Engine.Investigate with the real adapter as its graph reader.
func askOwningTeams(t *testing.T, s routeSeed, principal storage.Principal) routeAnswer {
	t.Helper()
	return investigateAnchorMembers(t, newFakeAdapter(t, s.conn()), principal, contextfabric.SubjectTeam, contextfabric.SubjectRepository,
		routeOwnedSlug, "which teams own repository "+routeOwnedSlug, routeOwnershipMessage)
}

func TestARepositoryNamedByItsLabelServesItsOwningTeamsThroughTheEngine(t *testing.T) {
	for name, principal := range map[string]storage.Principal{
		"unrestricted":              {OrgID: "org-1"},
		"granted the repository":    {OrgID: "org-1", RepositoryScopes: []string{routeOwnedSlug}},
		"granted both repositories": {OrgID: "org-1", RepositoryScopes: []string{routeOwnedSlug, "acme/bravo-service"}},
	} {
		t.Run(name, func(t *testing.T) {
			answer := askOwningTeams(t, seedOwnedRepository(), principal)
			if len(answer.committed) != 1 || answer.committed[0].CanonicalID != routeOwnedRepository || answer.basis != contextfabric.CommitBasisStatistical {
				t.Fatalf("resolver committed %+v on basis %q, want the named repository on the exact-label tier", answer.committed, answer.basis)
			}
			if got := answer.members(); strings.Join(got, ",") != "team:owner" {
				t.Fatalf("served %v, want the one team that owns the repository: a team the question text matched is not an owner", got)
			}
			for _, m := range answer.result.Cohort.Members {
				if m.InclusionReasons[0] != "Team that owns the repository the question names." || len(m.InclusionReasons) != 1 {
					t.Errorf("member %s inclusion reasons = %q", m.Subject.CanonicalID, m.InclusionReasons)
				}
			}
			if len(answer.walkLines) != 1 {
				t.Fatalf("%d decision lines, want exactly one", len(answer.walkLines))
			}
			line := answer.walkLines[0]
			if line["outcome"] != "owners" || line["anchor_kind"] != "repository" || line["anchor_basis"] != "sole_commit" || line["census"] != float64(2) || line["owners"] != float64(1) {
				t.Errorf("decision line = %v, want outcome=owners anchor_kind=repository anchor_basis=sole_commit census=2 owners=1", line)
			}
		})
	}
}

// TestACrowdOfTeamsTheQuestionTextMatchesDoesNotCrowdOutTheOwner: forty teams
// that own nothing here match the question text; the served cohort is still
// the owner alone and is complete.
func TestACrowdOfTeamsTheQuestionTextMatchesDoesNotCrowdOutTheOwner(t *testing.T) {
	s := seedOwnedRepository()
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("team:crowd-%02d", i)
		s.nodes = append(s.nodes, seededNode{kind: "team", id: id, label: id, repos: []string{"acme/bravo-service"}})
		s.text["team|"+id] = "teams own"
	}
	answer := askOwningTeams(t, s, storage.Principal{OrgID: "org-1"})
	if got := answer.members(); strings.Join(got, ",") != "team:owner" {
		t.Fatalf("served %v, want the one owning team", got)
	}
	if !answer.result.Cohort.Complete {
		t.Fatalf("cohort = %+v, want a complete cohort", answer.result.Cohort)
	}
}
