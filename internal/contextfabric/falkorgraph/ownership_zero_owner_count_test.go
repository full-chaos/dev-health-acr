package falkorgraph

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
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
	future   bool
}

// askOwningTeamCountWithOwners asks "how many teams own repository <slug>"
// over a graph whose ownership edges and team activity are stated by the
// test. The census read drops inactive teams as the stored query does; the
// edge read returns the ended edges only when it carries no window.
func askOwningTeamCountWithOwners(t *testing.T, s routeSeed, owners []ownerEdge, censusFault func() ([]row, error), principals ...storage.Principal) routeAnswer {
	t.Helper()
	return askOwningTeamCountWithOwnersAt(t, s, owners, censusFault, nil, principals...)
}

func askOwningTeamCountWithOwnersAt(t *testing.T, s routeSeed, owners []ownerEdge, censusFault func() ([]row, error), timeContext *contextfabric.TimeContext, principals ...storage.Principal) routeAnswer {
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
				if (o.ended || o.future) && windowed {
					continue
				}
				n := fakeSubjectNodeRow("team", o.team, strings.TrimPrefix(o.team, "team:"))["n"].(*node)
				if o.inactive {
					n.Properties[teamActiveProperty] = false
				}
				props := map[string]interface{}{propRelationType: string(contractsv1.ContextFabricRelationshipOwnedByTeam), propRelationshipID: "rel_" + o.team}
				if o.future {
					props[propValidFromNs] = nsTimestamp(time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
				}
				rows = append(rows, row{"id": routeOwnedRepository, "b": n, "r": &edge{Properties: props}})
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
		projectDeploymentsInterpreter{name: routeOwnedSlug, kind: contextfabric.SubjectRepository, member: contextfabric.SubjectTeam, count: true, timeContext: timeContext},
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

func exclusionIDs(a routeAnswer) []string {
	var ids []string
	if a.result.Cohort != nil {
		for _, e := range a.result.Cohort.Exclusions {
			ids = append(ids, e.Subject.CanonicalID)
		}
	}
	return ids
}

// An owner the caller may not read is not named in the exclusions either.
func TestAnExcludedOwnerTheCallerMayNotReadIsNotDisclosed(t *testing.T) {
	s := seedRepositoryWithoutAttributeOwner()
	s.nodes = append(s.nodes, seededNode{kind: "team", id: "team:hidden", label: "hidden", repos: []string{"hidden/service"}})
	answer := askOwningTeamCountWithOwners(t, s, []ownerEdge{{team: "team:hidden", inactive: true}}, nil, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{routeOwnedSlug}})
	if ids := exclusionIDs(answer); len(ids) != 0 {
		t.Fatalf("exclusions = %v, want none: the caller may not read that team", ids)
	}
}

// The only owner is inactive and no other team is in the census: the zero is
// still an answer, because the repository's own ownership edge shows the
// ownership was projected.
func TestAZeroOwnerCountIsServedWhenTheOnlyProjectedTeamIsTheInactiveOwner(t *testing.T) {
	s := seedRepositoryWithoutAttributeOwner()
	var kept []seededNode
	for _, n := range s.nodes {
		if n.kind != "team" {
			kept = append(kept, n)
		}
	}
	s.nodes = append(kept, seededNode{kind: "team", id: "team:former", label: "former", repos: []string{routeOwnedSlug}})
	answer := askOwningTeamCountWithOwners(t, s, []ownerEdge{{team: "team:former", inactive: true}}, nil)
	value, claimed, outcome := servedCount(answer)
	if !claimed || value != 0 || outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count claimed=%t value=%d outcome=%q, want a certified 0", claimed, value, outcome)
	}
	if got := exclusionReasons(answer); got[contractsv1.CohortExclusionReasonInactiveTeam] != 1 {
		t.Fatalf("exclusions = %v, want inactive_team:1", got)
	}
}

// Under a stated window the present-tense ownership read cannot speak for the
// past, so no zero is served.
func TestAZeroOwnerCountIsNotServedForAStatedWindow(t *testing.T) {
	start, end := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	window := contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}
	answer := askOwningTeamCountWithOwnersAt(t, seedRepositoryWithoutAttributeOwner(), nil, nil, &window)
	if _, claimed, outcome := servedCount(answer); claimed || outcome == contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count claimed=%t outcome=%q for a stated window, want none certified", claimed, outcome)
	}
}

func TestMoreExcludedOwnersThanTheContractCarriesStillServeTheExactCount(t *testing.T) {
	var owners []ownerEdge
	for i := 0; i < 260; i++ {
		owners = append(owners, ownerEdge{team: fmt.Sprintf("team:former-%03d", i), inactive: true})
	}
	answer := askOwningTeamCountWithOwners(t, seedRepositoryWithoutAttributeOwner(), owners, nil)
	value, claimed, outcome := servedCount(answer)
	if !claimed || value != 0 || outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count claimed=%t value=%d outcome=%q, want a certified 0", claimed, value, outcome)
	}
	if n := len(exclusionIDs(answer)); n != 250 {
		t.Fatalf("exclusions = %d, want the contract's 250", n)
	}
	if !strings.Contains(answer.result.Cohort.Rationale, "10 further teams") {
		t.Fatalf("rationale = %q, want the 10 teams the list does not carry named", answer.result.Cohort.Rationale)
	}
}

func TestAnOwnershipAssertionThatHasNotStartedIsNotDisclosedAsEnded(t *testing.T) {
	answer := askOwningTeamCountWithOwners(t, seedRepositoryWithoutAttributeOwner(), []ownerEdge{{team: "team:matched", future: true}}, nil)
	value, claimed, outcome := servedCount(answer)
	if !claimed || value != 0 || outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count claimed=%t value=%d outcome=%q, want a certified 0", claimed, value, outcome)
	}
	if got := exclusionReasons(answer); len(got) != 0 {
		t.Fatalf("exclusions = %v, want none: the assertion has not started", got)
	}
}

// An owner of the stated window that is inactive now is not in the active
// census, so the census cannot prove that the window had no owner.
func TestAZeroOwnerCountIsNotServedForAWindowWhoseOwnerIsInactiveNow(t *testing.T) {
	start, end := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	window := contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}
	answer := askOwningTeamCountWithOwnersAt(t, seedRepositoryWithoutAttributeOwner(), []ownerEdge{{team: "team:former", inactive: true}}, nil, &window)
	if _, claimed, outcome := servedCount(answer); claimed && outcome == contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("a zero was certified for a window in which an owner existed")
	}
}

// The only owner is inactive and the caller may not read it: the zero would
// hide an owner that exists, so the count is not served.
func TestAZeroOwnerCountIsNotServedWhenTheOnlyOwnerIsInactiveAndUnreadable(t *testing.T) {
	s := seedRepositoryWithoutAttributeOwner()
	s.nodes = append(s.nodes, seededNode{kind: "team", id: "team:hidden", label: "hidden", repos: []string{"hidden/service"}})
	answer := askOwningTeamCountWithOwners(t, s, []ownerEdge{{team: "team:hidden", inactive: true}}, nil, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{routeOwnedSlug}})
	if _, claimed, outcome := servedCount(answer); claimed || outcome == contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count claimed=%t outcome=%q while an owner exists the caller may not read, want none certified", claimed, outcome)
	}
}

// The bound the disclosure hit is reported through the adapter's configured
// telemetry, with counts only.
func TestTheOwnershipDisclosureBoundIsReportedThroughTheConfiguredTelemetry(t *testing.T) {
	var logs bytes.Buffer
	adapter := newFakeAdapter(t, ownershipEdgeConn(nil, 260, nil))
	adapter.config.Telemetry = SlogTelemetry{Logger: slog.New(slog.NewTextHandler(&logs, nil))}
	disclosure, err := adapter.repositoryOwnerExclusions(context.Background(), "key", storage.Principal{OrgID: "org-1"}, contextfabric.RequestedScope{}, ownedRepository().CanonicalID, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if len(disclosure.Exclusions) != maxOwnerExclusions || disclosure.OverCap != 10 {
		t.Fatalf("exclusions = %d over cap = %d, want 250 and 10", len(disclosure.Exclusions), disclosure.OverCap)
	}
	if got := logs.String(); !strings.Contains(got, "over_cap=10") || strings.Contains(got, "team:edge") {
		t.Fatalf("configured logger received %q, want the over-cap count and no team id", got)
	}
}
