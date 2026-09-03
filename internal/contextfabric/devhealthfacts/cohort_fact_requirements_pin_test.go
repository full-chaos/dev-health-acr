package devhealthfacts

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// graphrank's cohort fact-requirement table is a COPY of a rule that lives
// here, in the providers' own declared capabilities. This file is what makes
// a disagreement between the two a test failure instead of a comment nobody
// re-reads.
//
// The table cannot live beside the providers: graphrank is backend-neutral
// decision logic and must not import a ClickHouse-backed fact package. It
// cannot be derived at call time either -- the graph adapter has no registry.
// So it is a copy, and a copy is only safe while something compares it to the
// authority. That comparison is here, and it runs in BOTH directions, because
// each direction misses what the other catches: forward alone lets a servable
// pair be silently dropped, reverse alone lets a claim survive after the
// producer that justified it stops declaring the kind.

// cohortDerivedFactKinds is the set the graph adapter merges onto a
// discovered cohort -- health and workload.
//
// It is stated here rather than derived from every registered FactKind
// because the reverse direction below asks "of the kinds a cohort asks for,
// is any missing from the table", not "does the table list every fact this
// system can read". A cohort does not ask for deployments or reviews; that a
// producer serves them for repositories is true and irrelevant.
var cohortDerivedFactKinds = []contextfabric.FactKind{
	contextfabric.FactHealth,
	contextfabric.FactWorkload,
}

// capabilityDeclares reports whether any registered provider answers factKind
// for subjectKind, read from the providers' own Capability() rather than from
// any list in this file.
func capabilityDeclares(factKind contextfabric.FactKind, subjectKind contextfabric.SubjectKind) bool {
	for _, provider := range allProvidersForKindAudit() {
		capability := provider.Capability()
		if capability.Kind != factKind {
			continue
		}
		for _, declared := range capability.SupportedSubjectKinds {
			if declared == subjectKind {
				return true
			}
		}
	}
	return false
}

// TestCohortFactRequirementsClaimNothingNoProviderServes is the FORWARD
// direction: every pair the table claims must be declared by a real provider.
//
// This is the direction that was broken before the table existed. The adapter
// merged FactWorkload onto every discovered cohort unconditionally, and
// WorkloadProvider declares team and project only -- so a repository cohort
// asked for a fact kind nothing could serve for it, every time, structurally.
func TestCohortFactRequirementsClaimNothingNoProviderServes(t *testing.T) {
	t.Parallel()
	pairs := 0
	for subjectKind, factKinds := range graphrank.CohortFactRequirementKinds() {
		for _, factKind := range factKinds {
			pairs++
			if !capabilityDeclares(factKind, subjectKind) {
				t.Errorf("the cohort fact-requirement table claims %q for a %q cohort, but no registered provider declares that pair -- the requirement could only ever be pruned", factKind, subjectKind)
			}
		}
	}
	// A table that had emptied itself would pass every assertion above by
	// having nothing to check. The floor is the measured count, not len>0.
	if pairs < 5 {
		t.Fatalf("the table yielded only %d (cohort kind, fact kind) pairs; team and project each declare health and workload and repository declares health, so 5 is the floor -- a smaller number means rows were dropped, not that the check passed", pairs)
	}
}

// TestCohortFactRequirementsOmitNothingAProviderServes is the REVERSE
// direction: if a provider DOES declare a cohort-derived fact kind for a
// servable cohort kind, the table must ask for it.
//
// Without this, the table could be trimmed to the empty set for every kind
// and the forward test would still pass -- and every team and project answer
// would quietly lose its workload and health facts.
func TestCohortFactRequirementsOmitNothingAProviderServes(t *testing.T) {
	t.Parallel()
	table := graphrank.CohortFactRequirementKinds()
	for _, subjectKind := range graphrank.ServableCohortKindsForAudit() {
		asked := map[contextfabric.FactKind]bool{}
		for _, factKind := range table[subjectKind] {
			asked[factKind] = true
		}
		for _, factKind := range cohortDerivedFactKinds {
			if capabilityDeclares(factKind, subjectKind) && !asked[factKind] {
				t.Errorf("a provider declares %q for %q, but a %q cohort does not ask for it -- the answer silently loses facts it could have had", factKind, subjectKind, subjectKind)
			}
		}
	}
}

// TestEveryServableCohortKindHasARequirementRow closes the gap between the
// two guards above: both quantify over what the table CONTAINS, so a kind
// admitted at the seam with no row at all would satisfy both by being absent.
//
// A missing row is not a crash -- CohortFactRequirements returns nothing and
// the cohort simply asks for no facts -- which is exactly why it needs a test.
// An answer that serves a cohort and reads nothing about its members is the
// hollow answer the seam exists to prevent, arriving one layer further down.
func TestEveryServableCohortKindHasARequirementRow(t *testing.T) {
	t.Parallel()
	table := graphrank.CohortFactRequirementKinds()
	for _, subjectKind := range graphrank.ServableCohortKindsForAudit() {
		if len(table[subjectKind]) == 0 {
			t.Errorf("%q is servable at the seam but has no cohort fact-requirement row; a cohort of that kind would be served with nothing read about its members", subjectKind)
		}
	}
}

// TestRepositoryCohortsDoNotAskForWorkload states the specific asymmetry in
// the table as an ENFORCED expectation rather than a comment.
//
// It is not a restatement of the forward guard: this asserts the CURRENT
// declared state of the workload producer, so if repository workload is ever
// implemented, this test fails and points at itself. That is the intent --
// the follow-up is tracked, and the day it lands, this row and this test move
// together.
func TestRepositoryCohortsDoNotAskForWorkload(t *testing.T) {
	t.Parallel()
	if capabilityDeclares(contextfabric.FactWorkload, contextfabric.SubjectRepository) {
		t.Fatal("the workload provider now declares repository -- add FactWorkload to the repository row of graphrank's cohort fact-requirement table and delete this test")
	}
	for _, factKind := range graphrank.CohortFactRequirements(contextfabric.SubjectRepository) {
		if factKind == contextfabric.FactWorkload {
			t.Error("a repository cohort asks for workload, which no provider serves for it")
		}
	}
}
