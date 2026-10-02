package graphrank

import (
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func deploymentDiscovery(cap int) contextfabric.GraphDiscoveryRequest {
	kind := contextfabric.SubjectDeployment
	discovery := frameDiscovery(contextfabric.SubjectExpression{
		Kind:   contextfabric.SubjectExpressionChildrenOfScope,
		Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"payments"}, MemberKind: kind},
	}, "deployments", []string{"payments"})
	discovery.Request.Options.MaxCohortMembers = cap
	return discovery
}

func deploymentNodes(repos string, count int, prefix string) []CandidateNode {
	nodes := make([]CandidateNode, 0, count)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("%s_%d", prefix, i)
		nodes = append(nodes, candidateNode(contextfabric.SubjectDeployment, id, id, 0.9, []string{repos}))
	}
	return nodes
}

func TestDeploymentCohortRequiresTheDeploymentsFact(t *testing.T) {
	t.Parallel()
	got := CohortFactRequirements(contextfabric.SubjectDeployment)
	if len(got) != 1 || got[0] != contextfabric.FactDeployments {
		t.Fatalf("CohortFactRequirements(deployment) = %v, want [deployments]", got)
	}
}

func TestDeploymentCohortCountExcludesDeploymentsTheCallerCannotSee(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/visible"}}
	visible := deploymentNodes("acme/visible", 3, "dep_v")
	hidden := deploymentNodes("acme/hidden", 4, "dep_h")
	cohortWith, _, _, _, basis, populationWith := DiscoveredCohort(principal, deploymentDiscovery(10), append(append([]CandidateNode{}, visible...), hidden...), false, noInternal)
	cohortWithout, _, _, _, _, populationWithout := DiscoveredCohort(principal, deploymentDiscovery(10), visible, false, noInternal)
	if basis != CohortKindFromFrameMemberKind || cohortWith == nil || cohortWithout == nil {
		t.Fatalf("fixture never reached assembly: basis=%q", basis)
	}
	if populationWith != 3 || populationWithout != 3 {
		t.Fatalf("population with hidden = %d, without = %d, want 3 and 3: a hidden deployment must change neither the count nor any difference between two counts", populationWith, populationWithout)
	}
	if len(cohortWith.Members) != 3 || len(cohortWithout.Members) != 3 {
		t.Fatalf("members = %d / %d, want 3 / 3", len(cohortWith.Members), len(cohortWithout.Members))
	}
	for _, member := range cohortWith.Members {
		if member.Subject.Kind != contextfabric.SubjectDeployment {
			t.Fatalf("member %+v is not a deployment", member.Subject)
		}
	}
}

func TestDeploymentCohortCappedCountIsAFloorNotAnExactCount(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	nodes := deploymentNodes("*", 7, "dep")
	cohort, _, _, _, _, population := DiscoveredCohort(principal, deploymentDiscovery(3), nodes, false, noInternal)
	if cohort == nil {
		t.Fatal("cohort = nil")
	}
	if len(cohort.Members) != 3 || population != 7 {
		t.Fatalf("members = %d, population = %d, want 3 and 7", len(cohort.Members), population)
	}
	if cohort.Complete || !cohort.Truncated {
		t.Fatalf("capped cohort Complete=%v Truncated=%v, want false/true: a capped count must never read as exact", cohort.Complete, cohort.Truncated)
	}
	exact, _, _, _, _, _ := DiscoveredCohort(principal, deploymentDiscovery(10), deploymentNodes("*", 2, "dep"), true, noInternal)
	if exact == nil || exact.Complete || !exact.Truncated {
		t.Fatalf("a truncated pool must read Truncated, got %+v", exact)
	}
}

func TestDeploymentCohortIsNotDiscoveredForOtherExpressions(t *testing.T) {
	t.Parallel()
	discovery := frameDiscovery(contextfabric.SubjectExpression{
		Kind:       contextfabric.SubjectExpressionDiscoveredKind,
		Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectDeployment},
	}, "deployments", []string{"deployments"})
	cohort, _, _, _, basis, _ := DiscoveredCohort(storage.Principal{OrgID: "org_1"}, discovery, deploymentNodes("*", 2, "dep"), false, noInternal)
	if cohort != nil || basis != CohortKindMemberKindUnservable {
		t.Fatalf("discovered_kind deployment cohort=%v basis=%q, want nil and %q", cohort, basis, CohortKindMemberKindUnservable)
	}
}
