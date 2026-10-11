package falkorgraph

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ownerEdge is one OWNED_BY_TEAM edge of the named repository as the graph
// stores it: the team's own activity and whether the assertion has ended.
type ownerEdge struct {
	team     string
	inactive bool
	ended    bool
}

// askOwningTeamCountWithOwners asks "how many teams own repository <slug>"
// over a graph whose ownership edges and team activity are stated by the
// test. The census read drops inactive teams as the stored query does; the
// edge read returns the ended edges only when it carries no window.
func askOwningTeamCountWithOwners(t *testing.T, s routeSeed, owners []ownerEdge, censusFault func() ([]row, error), principals ...storage.Principal) routeAnswer {
	principal := storage.Principal{OrgID: "org-1"}
	if len(principals) > 0 {
		principal = principals[0]
	}
	t.Helper()
	inactive := map[string]bool{}
	for _, o := range owners {
		inactive[o.team] = inactive[o.team] || o.inactive
	}
	base := s.conn()
	conn := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if params["rel"] == string(contractsv1.ContextFabricRelationshipOwnedByTeam) && params["fromKind"] == "repository" {
			_, windowed := params[temporalParamStart]
			var rows []row
			for _, o := range owners {
				if o.ended && windowed {
					continue
				}
				n := fakeSubjectNodeRow("team", o.team, strings.TrimPrefix(o.team, "team:"))["n"].(*node)
				if o.inactive {
					n.Properties[teamActiveProperty] = false
				}
				rows = append(rows, row{"id": routeOwnedRepository, "b": n, "r": &edge{Properties: map[string]interface{}{propRelationType: string(contractsv1.ContextFabricRelationshipOwnedByTeam), propRelationshipID: "rel_" + o.team}}})
			}
			return rows, nil
		}
		rows, err := base.queryFunc(ctx, key, cypher, params, readOnly)
		if isTeamCensus(params) {
			if censusFault != nil {
				return censusFault()
			}
			for _, r := range rows {
				if n := r["n"].(*node); inactive[propStringValue(n.Properties[propCanonicalID])] {
					n.Properties[teamActiveProperty] = false
				}
			}
		}
		return rows, err
	}}
	adapter := newFakeAdapter(t, conn)
	var identity []graphrank.IdentityRow
	for _, n := range s.nodes {
		if n.kind == "repository" {
			identity = append(identity, graphrank.IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: n.id, Label: n.label})
		}
	}
	adapter.config.IdentityUniverse = func(context.Context, string) ([]graphrank.IdentityRow, time.Time, bool, error) {
		return identity, time.Time{}, true, nil
	}
	return investigateAnchor(t, adapter, principal,
		projectDeploymentsInterpreter{name: routeOwnedSlug, kind: contextfabric.SubjectRepository, member: contextfabric.SubjectTeam, count: true},
		"how many teams own repository "+routeOwnedSlug, routeOwnershipMessage)
}

// seedRepositoryWithoutAttributeOwner is the owned repository whose own team
// no longer lists it, beside one team that owns another repository, so the
// census holds a team and none of them owns the named repository.
func seedRepositoryWithoutAttributeOwner() routeSeed {
	s := seedOwnedRepository()
	for i := range s.nodes {
		if s.nodes[i].id == "team:owner" {
			s.nodes[i].repos = []string{"acme/other-service"}
		}
	}
	return s
}

func exclusionReasons(a routeAnswer) map[string]int {
	reasons := map[string]int{}
	if a.result.Cohort != nil {
		for _, e := range a.result.Cohort.Exclusions {
			reasons[e.Reason]++
		}
	}
	return reasons
}

func TestACountOverARepositoryNoTeamOwnsIsAZeroWhenTheOwnershipReadWasComplete(t *testing.T) {
	answer := askOwningTeamCountWithOwners(t, seedRepositoryWithoutAttributeOwner(), nil, nil)
	value, claimed, outcome := servedCount(answer)
	if !claimed || value != 0 || outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count claimed=%t value=%d outcome=%q, want a certified 0", claimed, value, outcome)
	}
	if answer.result.Cohort == nil || !answer.result.Cohort.Complete || len(answer.result.Cohort.Members) != 0 {
		t.Fatalf("cohort = %+v, want a resolved empty complete cohort", answer.result.Cohort)
	}
	if answer.result.Cohort.Rationale != ownershipZeroOwnersRationale {
		t.Fatalf("rationale = %q, want the zero-owner rationale that names the separate projection pass", answer.result.Cohort.Rationale)
	}
}

func TestAnInactiveOwnerIsNotCountedAndIsDisclosed(t *testing.T) {
	answer := askOwningTeamCountWithOwners(t, seedRepositoryWithoutAttributeOwner(), []ownerEdge{{team: "team:former", inactive: true}}, nil)
	value, claimed, outcome := servedCount(answer)
	if !claimed || value != 0 || outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count claimed=%t value=%d outcome=%q, want a certified 0", claimed, value, outcome)
	}
	if got := exclusionReasons(answer); len(got) != 1 || got[contractsv1.CohortExclusionReasonInactiveTeam] != 1 {
		t.Fatalf("exclusions = %v, want inactive_team:1", got)
	}
}

func TestAnEndedOwnershipAssertionIsNotCountedAndIsDisclosed(t *testing.T) {
	answer := askOwningTeamCountWithOwners(t, seedRepositoryWithoutAttributeOwner(), []ownerEdge{{team: "team:matched", ended: true}}, nil)
	value, claimed, outcome := servedCount(answer)
	if !claimed || value != 0 || outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count claimed=%t value=%d outcome=%q, want a certified 0", claimed, value, outcome)
	}
	if got := exclusionReasons(answer); len(got) != 1 || got[contractsv1.CohortExclusionReasonEndedAssertion] != 1 {
		t.Fatalf("exclusions = %v, want ended_assertion:1", got)
	}
}

func TestACountWithAnActiveOwnerBesideAnInactiveOneCountsOnlyTheActiveOne(t *testing.T) {
	answer := askOwningTeamCountWithOwners(t, seedOwnedRepository(), []ownerEdge{{team: "team:owner"}, {team: "team:former", inactive: true}}, nil)
	value, claimed, outcome := servedCount(answer)
	if !claimed || value != 1 || outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count claimed=%t value=%d outcome=%q, want a certified 1", claimed, value, outcome)
	}
	if got := exclusionReasons(answer); got[contractsv1.CohortExclusionReasonInactiveTeam] != 1 || len(got) != 1 {
		t.Fatalf("exclusions = %v, want inactive_team:1", got)
	}
}

// A zero is an answer only over an ownership read that covered the census: a
// graph with no team at all has not projected ownership.
func TestAZeroOwnerCountIsNotCertifiedOverACensusWithNoTeam(t *testing.T) {
	s := seedRepositoryWithoutAttributeOwner()
	var kept []seededNode
	for _, n := range s.nodes {
		if n.kind != "team" {
			kept = append(kept, n)
		}
	}
	s.nodes = kept
	answer := askOwningTeamCountWithOwners(t, s, nil, nil)
	if _, claimed, outcome := servedCount(answer); claimed || outcome == contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count claimed=%t outcome=%q over a census with no team, want none certified", claimed, outcome)
	}
}

func TestAZeroOwnerCountIsNotCertifiedOverACutCensus(t *testing.T) {
	cut := func() ([]row, error) {
		rows := make([]row, 0, exactNameCandidateQueryLimit+1)
		for i := 0; i <= exactNameCandidateQueryLimit; i++ {
			id := "team:bulk-" + strings.Repeat("x", 1) + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26))
			rows = append(rows, fakeSubjectNodeRow("team", id, id))
		}
		return rows, nil
	}
	answer := askOwningTeamCountWithOwners(t, seedRepositoryWithoutAttributeOwner(), nil, cut)
	if _, claimed, outcome := servedCount(answer); claimed || outcome == contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count claimed=%t outcome=%q over a cut census, want none certified", claimed, outcome)
	}
}

// An owner the caller may not read is not a missing owner: the zero would
// state that no team owns a repository some team does.
func TestAZeroOwnerCountIsNotCertifiedWhenTheCallerMayNotReadTheOwner(t *testing.T) {
	s := seedRepositoryWithoutAttributeOwner()
	s.nodes = append(s.nodes, seededNode{kind: "team", id: "team:hidden", label: "hidden", repos: []string{"hidden/service"}})
	answer := askOwningTeamCountWithOwners(t, s, []ownerEdge{{team: "team:hidden"}}, nil, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{routeOwnedSlug}})
	if _, claimed, outcome := servedCount(answer); claimed || outcome == contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count claimed=%t outcome=%q while the one owner is unreadable to the caller, want none certified", claimed, outcome)
	}
}
