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
	if len(answer.committed) != 1 || answer.committed[0].CanonicalID != routeOwnedRepository {
		t.Fatalf("resolver committed %+v, want the exact-label repository", answer.committed)
	}
	if _, claimed, outcome := servedCount(answer); claimed || outcome == contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count claimed=%t outcome=%q after an incomplete identity lookup, want none certified", claimed, outcome)
	}
}
