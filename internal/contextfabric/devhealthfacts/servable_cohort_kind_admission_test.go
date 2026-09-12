package devhealthfacts

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// The seam allow-list is a POLICY, and a policy nothing checks becomes a list
// of whatever was true when someone last looked. This file is what makes the
// rule the allow-list claims to follow an enforced one:
//
//	a member kind with a projected population AND a declaring cohort-fact
//	producer is servable.
//
// Both halves come from the authorities that own them, never from a list kept
// here: the population from devhealthsource's producer registries
// (ProjectedSubjectKinds), the producers from NewProviders -- the same
// constructor the running service registers -- read through their own
// Capability(). A list in this file would make the guard agree with itself
// while the service disagreed with both.
//
// WHY THIS IS NOT THE PAIR OF GUARDS NEXT DOOR. The cohort fact-requirement
// pins quantify over kinds ALREADY admitted; they cannot see a kind that is
// projected and served by a producer and simply never let through. That is
// precisely the state the corpus measured: the graph held incidents, the
// incidents producer declared them, and the question was refused at the
// allow-list before any of it was read.

// declaringCohortFactProviders returns the cohort-derived fact kinds some
// registered provider declares for subjectKind, read from NewProviders'
// own Capability() declarations.
//
// A nil client is what makes this an inspection of the REGISTRY rather than
// of a live read: Capability() reports what a provider answers for, and no
// provider touches its client to say so.
func declaringCohortFactKinds(subjectKind contextfabric.SubjectKind) []contextfabric.FactKind {
	var declaring []contextfabric.FactKind
	for _, factKind := range graphrank.CohortDerivedFactKinds() {
		for _, provider := range NewProviders(nil) {
			capability := provider.Capability()
			if capability.Kind != factKind {
				continue
			}
			for _, declared := range capability.SupportedSubjectKinds {
				if declared == subjectKind {
					declaring = append(declaring, factKind)
					break
				}
			}
		}
	}
	return declaring
}

// TestEveryProjectedKindWithACohortFactProducerIsServable is the forward
// direction of the admission rule: a kind the projection emits, for which a
// registered provider declares a cohort-derived fact, must be admitted.
//
// A failure here is not a style complaint. It names a question the service
// refuses while already holding, in the graph, every member it would need to
// answer, and reading, through a registered producer, every fact it would
// need about them.
func TestEveryProjectedKindWithACohortFactProducerIsServable(t *testing.T) {
	t.Parallel()
	servable := map[contextfabric.SubjectKind]bool{}
	for _, kind := range contextfabric.ServableCohortKindsForAudit() {
		servable[kind] = true
	}
	projected := devhealthsource.ProjectedSubjectKinds()
	if len(projected) == 0 {
		t.Fatal("devhealthsource.ProjectedSubjectKinds() is empty, so this guard quantifies over nothing -- the producer registries declare no subject kinds at all")
	}
	checked := 0
	for _, kind := range projected {
		declaring := declaringCohortFactKinds(kind)
		if len(declaring) == 0 {
			continue
		}
		checked++
		if !servable[kind] {
			t.Errorf("%q is projected and %v is declared for it by a registered provider, but the seam allow-list refuses it -- the service holds the members and can read their facts, and still refuses the question", kind, declaring)
		}
	}
	if checked == 0 {
		t.Fatal("no projected kind had a declaring cohort-fact provider, so this guard asserted nothing -- either the fact registry or the projection registry stopped reporting")
	}
}

// TestEveryServableKindIsProjected is the reverse direction, and it is the
// half that catches an allow-list widened by argument rather than by
// evidence: a kind admitted with no projected population ranks the empty set
// and presents it as an answer.
//
// It is stated separately from the forward guard because the two fail for
// opposite reasons and a reader needs to know which one fired: forward means
// a question is refused that could be answered, reverse means a question is
// answered that has nothing to answer it.
func TestEveryServableKindIsProjected(t *testing.T) {
	t.Parallel()
	projected := map[contextfabric.SubjectKind]bool{}
	for _, kind := range devhealthsource.ProjectedSubjectKinds() {
		projected[kind] = true
	}
	admitted := contextfabric.ServableCohortKindsForAudit()
	if len(admitted) == 0 {
		t.Fatal("the seam allow-list is empty, so this guard quantifies over nothing")
	}
	for _, kind := range admitted {
		if !projected[kind] {
			t.Errorf("%q is servable at the seam but no projection producer declares it -- a cohort of that kind can only ever be the empty set", kind)
		}
	}
}
