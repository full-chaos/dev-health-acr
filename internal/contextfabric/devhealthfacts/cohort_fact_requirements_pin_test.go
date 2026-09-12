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
	if pairs < 7 {
		t.Fatalf("the table yielded only %d (cohort kind, fact kind) pairs; team and project each declare health and workload, repository declares health, and incident and pull_request each declare their own producer's fact, so 7 is the floor -- a smaller number means rows were dropped, not that the check passed", pairs)
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
	for _, subjectKind := range contextfabric.ServableCohortKindsForAudit() {
		asked := map[contextfabric.FactKind]bool{}
		for _, factKind := range table[subjectKind] {
			asked[factKind] = true
		}
		for _, factKind := range graphrank.CohortDerivedFactKinds() {
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
	for _, subjectKind := range contextfabric.ServableCohortKindsForAudit() {
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

// TestCohortDerivedFactKindsIsExactlyTheTablesUnion closes the hole every
// other guard in this file shares: they take their QUANTIFICATION UNIVERSE
// from CohortDerivedFactKinds(), which is the artifact under test.
//
// TestCohortFactRequirementsOmitNothingAProviderServes loops over
// `graphrank.CohortDerivedFactKinds()` to decide which fact kinds to ask
// about. So a derived set that silently LOSES a value does not fail that
// guard -- it shrinks it. Drop FactIncidents from the union and the guard
// simply stops asking whether anything declares FactIncidents; the same
// shrinkage blinds the admission guard next door, whose
// declaringCohortFactKinds walks the same set and therefore reports "no
// declaring producer" for a kind whose producer is registered and declaring.
// Every one of those tests stays green while the accessor no longer describes
// the table. A guard whose universe is supplied by its own subject cannot see
// its subject get smaller.
//
// This test recomputes the union INDEPENDENTLY, from the table itself
// (CohortFactRequirementKinds, a different accessor over the same rows), and
// compares. It quantifies over the rows, so shrinking the derived set is a
// disagreement rather than a narrower question.
func TestCohortDerivedFactKindsIsExactlyTheTablesUnion(t *testing.T) {
	t.Parallel()
	table := graphrank.CohortFactRequirementKinds()
	if len(table) == 0 {
		t.Fatal("the cohort fact-requirement table is empty, so this comparison has nothing to disagree about")
	}
	fromRows := map[contextfabric.FactKind]bool{}
	for _, declared := range table {
		for _, factKind := range declared {
			fromRows[factKind] = true
		}
	}
	if len(fromRows) == 0 {
		t.Fatal("no row declares any fact kind, so the union is empty and this guard asserts nothing")
	}
	fromAccessor := map[contextfabric.FactKind]bool{}
	for _, factKind := range graphrank.CohortDerivedFactKinds() {
		fromAccessor[factKind] = true
	}
	for factKind := range fromRows {
		if !fromAccessor[factKind] {
			t.Errorf("%q is declared by a row of the cohort fact-requirement table but is absent from CohortDerivedFactKinds() -- every guard that takes its universe from that accessor has silently stopped asking about %q", factKind, factKind)
		}
	}
	for factKind := range fromAccessor {
		if !fromRows[factKind] {
			t.Errorf("CohortDerivedFactKinds() reports %q, which no row of the table declares -- the accessor claims a cohort can ask for a fact nothing asks for", factKind)
		}
	}
}
