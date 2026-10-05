package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// askOwningTeamCount runs "how many teams own repository <slug>" through
// Engine.Investigate with the real adapter as its graph reader.
func askOwningTeamCount(t *testing.T, s routeSeed, principal storage.Principal) routeAnswer {
	t.Helper()
	return askOwningTeamCountOver(t, s, principal, true)
}

// askOwningTeamCountOver is the same question when the identity lookup the
// resolver reads (the production composition wires one) is complete or not.
// The universe carries every repository of the seed by its label only.
func askOwningTeamCountOver(t *testing.T, s routeSeed, principal storage.Principal, lookupComplete bool) routeAnswer {
	t.Helper()
	adapter := newFakeAdapter(t, s.conn())
	var rows []graphrank.IdentityRow
	for _, n := range s.nodes {
		if n.kind == "repository" {
			rows = append(rows, graphrank.IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: n.id, Label: n.label})
		}
	}
	adapter.config.IdentityUniverse = func(context.Context, string) ([]graphrank.IdentityRow, time.Time, bool, error) {
		return rows, time.Time{}, lookupComplete, nil
	}
	return investigateAnchor(t, adapter, principal,
		projectDeploymentsInterpreter{name: routeOwnedSlug, kind: contextfabric.SubjectRepository, member: contextfabric.SubjectTeam, count: true},
		"how many teams own repository "+routeOwnedSlug, routeOwnershipMessage)
}

// servedCount is the count the served document states: the integer of its
// cardinality claim and the outcome of its assembled count row.
func servedCount(a routeAnswer) (value int64, claimed bool, outcome contractsv1.ContextFabricPlanRequirementOutcome) {
	for _, claim := range a.result.ClaimedFacts {
		if claim.Value.Integer != nil && claim.Kind == contractsv1.ContextFabricFactCardinality {
			value, claimed = *claim.Value.Integer, true
		}
	}
	for _, row := range a.result.Completeness.Outcomes {
		if row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult && row.Obligation == string(contextfabric.ObligationCount) {
			outcome = row.Outcome
		}
	}
	return value, claimed, outcome
}

func TestACountOverARepositoryNamedByItsLabelIsCertifiedWhenTheLabelMatchesOne(t *testing.T) {
	answer := askOwningTeamCount(t, seedOwnedRepository(), storage.Principal{OrgID: "org-1"})
	if len(answer.committed) != 1 || answer.committed[0].CanonicalID != routeOwnedRepository || answer.basis != contextfabric.CommitBasisStatistical {
		t.Fatalf("resolver committed %+v on basis %q, want the one repository on the exact-label tier", answer.committed, answer.basis)
	}
	if got := strings.Join(answer.members(), ","); got != "team:owner" {
		t.Fatalf("served %v, want the one owning team", got)
	}
	value, claimed, outcome := servedCount(answer)
	if !claimed || value != 1 || outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count = %d claimed=%t outcome=%q, want a certified 1", value, claimed, outcome)
	}
}

func TestACountOverALabelThatMatchesTwoRepositoriesIsNotCertified(t *testing.T) {
	s := seedOwnedRepository()
	const twin = "repository:gitlab:" + routeOwnedSlug
	s.nodes = append(s.nodes, seededNode{kind: "repository", id: twin, label: routeOwnedSlug, repos: []string{routeOwnedSlug}})
	s.text["repository|"+twin] = "acme alpha service"
	answer := askOwningTeamCount(t, s, storage.Principal{OrgID: "org-1"})
	if _, claimed, outcome := servedCount(answer); claimed || outcome == contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count claimed=%t outcome=%q over a label that matches two repositories, want none certified", claimed, outcome)
	}
}

func TestACountOverALabelThatMatchesNothingIsNotCertified(t *testing.T) {
	s := seedOwnedRepository()
	var kept []seededNode
	for _, n := range s.nodes {
		if n.id != routeOwnedRepository {
			kept = append(kept, n)
		}
	}
	s.nodes = kept
	delete(s.text, "repository|"+routeOwnedRepository)
	answer := askOwningTeamCount(t, s, storage.Principal{OrgID: "org-1"})
	if _, claimed, outcome := servedCount(answer); claimed || outcome == contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count claimed=%t outcome=%q over a label that matches nothing, want none certified", claimed, outcome)
	}
}

// A restricted caller's count is over the repositories the caller may see: a
// second repository with the same label that the caller cannot see is not a
// match, and a caller granted only the other repository sees none.
func TestACountOverALabelFollowsWhatTheCallerMaySee(t *testing.T) {
	s := seedOwnedRepository()
	const hidden = "repository:gitlab:" + routeOwnedSlug
	const hiddenScope = "hidden/alpha-service"
	s.nodes = append(s.nodes, seededNode{kind: "repository", id: hidden, label: routeOwnedSlug, repos: []string{hiddenScope}})
	s.text["repository|"+hidden] = "acme alpha service"
	t.Run("granted only the named repository", func(t *testing.T) {
		answer := askOwningTeamCount(t, s, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{routeOwnedSlug}})
		value, claimed, outcome := servedCount(answer)
		if !claimed || value != 1 || outcome != contractsv1.ContextFabricRequirementSatisfied {
			t.Fatalf("count = %d claimed=%t outcome=%q, want a certified 1: the repository the caller cannot see is not a match", value, claimed, outcome)
		}
		for _, id := range answer.members() {
			if id == "team:hidden" {
				t.Fatalf("served %v, a team of a repository the caller cannot see leaked", answer.members())
			}
		}
	})
	t.Run("unrestricted sees both", func(t *testing.T) {
		answer := askOwningTeamCount(t, s, storage.Principal{OrgID: "org-1"})
		if _, claimed, _ := servedCount(answer); claimed {
			t.Fatalf("a count was certified over two visible repositories with the label")
		}
	})
	t.Run("granted only the other repository", func(t *testing.T) {
		answer := askOwningTeamCount(t, s, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/bravo-service"}})
		if _, claimed, outcome := servedCount(answer); claimed || outcome == contractsv1.ContextFabricRequirementSatisfied {
			t.Fatalf("count claimed=%t outcome=%q for a caller who may see no repository with the label", claimed, outcome)
		}
	})
}

// A crowd of repositories whose text matches the label truncates the search.
// The one exact-label match still commits, but the lookup cannot rule out a
// second repository with the label, so its count is not certified.
func TestACountOverALabelIsNotCertifiedWhenTheSearchWasTruncated(t *testing.T) {
	s := seedOwnedRepository()
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("repository:github:acme/alpha-service-clone-%02d", i)
		s.nodes = append(s.nodes, seededNode{kind: "repository", id: id, label: id, repos: []string{routeOwnedSlug}})
		s.text["repository|"+id] = "acme alpha service"
	}
	answer := askOwningTeamCount(t, s, storage.Principal{OrgID: "org-1"})
	if len(answer.committed) != 1 || answer.committed[0].CanonicalID != routeOwnedRepository {
		t.Fatalf("resolver committed %+v, want the exact-label repository to still commit under truncation", answer.committed)
	}
	if _, claimed, outcome := servedCount(answer); claimed || outcome == contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count claimed=%t outcome=%q after a truncated search, want none certified", claimed, outcome)
	}
}

// An incomplete identity lookup cannot rule out another subject claiming the
// label by an alias or provider key, so the one exact-label match is not
// enough to certify a count.
func TestACountOverALabelIsNotCertifiedWhenTheIdentityLookupWasIncomplete(t *testing.T) {
	answer := askOwningTeamCountOver(t, seedOwnedRepository(), storage.Principal{OrgID: "org-1"}, false)
	if len(answer.committed) != 0 {
		t.Fatalf("resolver committed %+v, want nothing: an incomplete identity lookup cannot prove the exact label names one repository", answer.committed)
	}
	if _, claimed, outcome := servedCount(answer); claimed || outcome == contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count claimed=%t outcome=%q after an incomplete identity lookup, want none certified", claimed, outcome)
	}
}

func TestALabelNamedRepositoryKeepsTheExactLabelCommitOnlyWhenTheLookupDidNotRunForTheTimeAxis(t *testing.T) {
	start, end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for name, cell := range map[string]struct {
		timeContext contextfabric.TimeContext
		lookups     int
		committed   bool
	}{
		"current axis, lookup ran incomplete": {contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, 1, false},
		"range axis, lookup not run":          {contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, 0, true},
	} {
		t.Run(name, func(t *testing.T) {
			adapter := newFakeAdapter(t, seedOwnedRepository().conn())
			lookups := 0
			adapter.config.IdentityUniverse = func(context.Context, string) ([]graphrank.IdentityRow, time.Time, bool, error) {
				lookups++
				return []graphrank.IdentityRow{{Kind: contextfabric.SubjectRepository, CanonicalID: routeOwnedRepository, Label: routeOwnedSlug}}, time.Time{}, false, nil
			}
			interpreter := projectDeploymentsInterpreter{name: routeOwnedSlug, kind: contextfabric.SubjectRepository, member: contextfabric.SubjectTeam, count: true, timeContext: &cell.timeContext}
			interpreted, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{}, contextfabric.InvestigationRequest{})
			if err != nil {
				t.Fatal(err)
			}
			request := fakeDiscoveryRequest(contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: routeOwnedRepository, Label: routeOwnedSlug}, 10).Request
			request.Question = "how many teams own repository " + routeOwnedSlug
			request.RequestedScope.SubjectHints = nil
			request.TimeContext = cell.timeContext
			resolution, _, _, _, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, outcome.Frame, contextfabric.SubjectRepository)
			if err != nil {
				t.Fatalf("ResolveSubjects error = %v", err)
			}
			if lookups != cell.lookups {
				t.Fatalf("identity universe read %d times, want %d", lookups, cell.lookups)
			}
			committed := len(resolution.Committed) == 1 && resolution.Committed[0].CanonicalID == routeOwnedRepository
			if committed != cell.committed || (!cell.committed && len(resolution.Committed) != 0) {
				t.Fatalf("committed %+v candidates %+v, want the exact-label repository committed=%t", resolution.Committed, resolution.Candidates, cell.committed)
			}
		})
	}
}

// The same count question asked twice with answer reuse on is certified, or
// not, the same way both times: a stored answer whose count the scope decision
// would not state again is not reused and the question is answered fresh.
func TestTheSameLabelNamedCountQuestionIsCertifiedTheSameWayWhenAskedTwice(t *testing.T) {
	twinSeed := seedOwnedRepository()
	const twin = "repository:gitlab:" + routeOwnedSlug
	twinSeed.nodes = append(twinSeed.nodes, seededNode{kind: "repository", id: twin, label: routeOwnedSlug, repos: []string{routeOwnedSlug}})
	twinSeed.text["repository|"+twin] = "acme alpha service"
	for name, cell := range map[string]struct {
		seed    routeSeed
		certify bool
	}{
		"one match":   {seedOwnedRepository(), true},
		"two matches": {twinSeed, false},
	} {
		t.Run(name, func(t *testing.T) {
			interpreter := projectDeploymentsInterpreter{name: routeOwnedSlug, kind: contextfabric.SubjectRepository, member: contextfabric.SubjectTeam, count: true}
			_, outcome, _ := interpreter.Interpret(context.Background(), storage.Principal{}, contextfabric.InvestigationRequest{})
			store := &routeStore{frame: outcome.Frame, anchorKind: contextfabric.SubjectRepository}
			type served struct {
				value   int64
				claimed bool
				outcome contractsv1.ContextFabricPlanRequirementOutcome
				members string
			}
			var turns []served
			var reused []bool
			for turn := 1; turn <= 2; turn++ {
				adapter := newFakeAdapter(t, cell.seed.conn())
				adapter.config.IdentityUniverse = func(context.Context, string) ([]graphrank.IdentityRow, time.Time, bool, error) {
					return nil, time.Time{}, true, nil
				}
				answer := investigateAnchorReusing(t, adapter, storage.Principal{OrgID: "org-1"}, interpreter, "how many teams own repository "+routeOwnedSlug, routeOwnershipMessage, store)
				value, claimed, outcome := servedCount(answer)
				turns = append(turns, served{value, claimed, outcome, strings.Join(answer.members(), ",")})
				reused = append(reused, answer.result.Reused)
				t.Logf("turn %d: reused=%t claimed=%t value=%d outcome=%q members=%q", turn, answer.result.Reused, claimed, value, outcome, turns[turn-1].members)
			}
			if store.offers != 1 {
				t.Fatalf("the stored answer was offered %d times, want once: the second turn must have reached the reuse decision", store.offers)
			}
			if cell.certify && reused[1] {
				t.Fatalf("the stored label-named count was reused: its commit leaves no stored digest, so the second turn must be answered fresh")
			}
			if turns[0] != turns[1] {
				t.Fatalf("asked twice: first %+v, second %+v, want the same certification", turns[0], turns[1])
			}
			if turns[0].claimed != cell.certify || (cell.certify && (turns[0].value != 1 || turns[0].outcome != contractsv1.ContextFabricRequirementSatisfied)) {
				t.Fatalf("certified = %t value %d outcome %q, want certified=%t", turns[0].claimed, turns[0].value, turns[0].outcome, cell.certify)
			}
		})
	}
}
