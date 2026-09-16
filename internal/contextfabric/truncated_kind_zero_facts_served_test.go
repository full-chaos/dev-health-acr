package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestATruncatedProviderZeroedByTheBundleCapIsNarrowedAtAnHonestZero: the
// registry's own bundle-wide fact cap (fact_registry.go's
// mergeFactProviderResult) can slice a provider's result down to ZERO
// retained facts and still mint SourceTruncated for it -- truncation says a
// limit was hit, not that anything survived it. A read requirement whose
// only fact-bearing kind is that truncated, zero-retained source must
// credit Served honestly (zero), never a count of facts nobody received.
//
// Driven through the REAL producer path -- a real registry, a real
// bundle-wide cap trim, and factKindsWithFacts(bundle.Facts) (the same
// derivation readPopulationEvidenceFrom uses in production) -- rather than a
// hand-built evidence fixture, so it proves the wiring and not just the
// per-kind classification in isolation.
func TestATruncatedProviderZeroedByTheBundleCapIsNarrowedAtAnHonestZero(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_repro", Label: "Repro"}

	// Provider A fills the bundle-wide cap exactly. Provider B then has ZERO
	// room left: mergeFactProviderResult's `remaining` is 0, so its facts are
	// sliced to result.Facts[:0] and result.State becomes SourceTruncated.
	providerA := &factProviderStub{
		capability: FactCapability{Kind: FactStatus, Name: "status", Version: "v1",
			SupportedSubjectKinds: []SubjectKind{SubjectProject}, Dimension: HealthDimensionExecutionCompletion,
			SubjectRoles: []FactRole{FactRoleSubject}},
		result: FactProviderResult{Facts: manyFacts(FactStatus, project, maxCanonicalFactsPerBundle), State: SourceAvailable, Version: "v1"},
	}
	providerB := &factProviderStub{
		capability: FactCapability{Kind: FactReadiness, Name: "readiness", Version: "v1",
			SupportedSubjectKinds: []SubjectKind{SubjectProject}, Dimension: HealthDimensionExecutionCompletion,
			SubjectRoles: []FactRole{FactRoleSubject}},
		result: FactProviderResult{Facts: manyFacts(FactReadiness, project, 5), State: SourceAvailable, Version: "v1"},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{providerA, providerB}, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry: %v", err)
	}
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
		canonicalFactRequest(project, FactStatus, FactReadiness))
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}

	// Find whichever kind actually ended up truncated to zero (order across
	// two providers is not asserted -- the shape fires on whichever one
	// loses the race for remaining capacity).
	var zeroedKind FactKind
	for _, source := range bundle.Coverage.Sources {
		kind, ok := canonicalFactKindOf(source.Source)
		if !ok {
			continue
		}
		count := 0
		for _, fact := range bundle.Facts {
			if fact.Kind == kind {
				count++
			}
		}
		if source.State == SourceTruncated && count == 0 {
			zeroedKind = kind
			break
		}
	}
	if zeroedKind == "" {
		t.Fatal("expected one provider to be truncated to zero retained facts by the bundle-wide cap -- none was; the fixture no longer forces the cap boundary")
	}
	t.Logf("kind=%s truncated to zero retained facts", zeroedKind)

	requirement := contractsv1.ContextFabricPlanRequirement{
		Requirement: "state/subject/project", Obligation: "state", Kind: string(ObligationKindRead),
		Subject: SubjectProject, FactKinds: []FactKind{zeroedKind}, Quantifier: string(CompletionQuantifierAtLeastOne),
	}
	// factKindsWithFacts(bundle.Facts) is the SAME derivation
	// readPopulationEvidenceFrom uses in production -- this is not a
	// hand-picked test map, it is the real bundle read back through the real
	// helper.
	rows := appendReadRequirementEvaluations(nil, []contractsv1.ContextFabricPlanRequirement{requirement}, bundle.Coverage,
		readPopulationEvidence{KindsWithFacts: factKindsWithFacts(bundle.Facts)})
	if len(rows) != 1 {
		t.Fatalf("appended %d rows, want 1: %+v", len(rows), rows)
	}
	row := rows[0]
	t.Logf("row: outcome=%s impact=%s served=%d declared=%d cause=%s", row.Outcome, row.Impact, row.Served, row.Declared, row.CauseCoverage)

	// The row must never claim the reader got something for a kind that
	// retained zero facts.
	if row.Served != 0 {
		t.Fatalf("row credited Served=%d for %s, which retained 0 facts (bundle-wide cap exhausted before it merged)", row.Served, zeroedKind)
	}
	// AND IT MUST STILL NOT READ `unavailable`: SourceTruncated is a
	// fact-bearing state BY TYPE (TestFactBearingAgreesWithTheRegistrysOwnRule),
	// whatever this particular read actually kept -- an honest zero is a
	// narrowed zero, not an unavailable one.
	if row.Outcome != contractsv1.ContextFabricRequirementNarrowed {
		t.Fatalf("outcome = %q, want %q -- a truncated state must never read unavailable even when it retained nothing",
			row.Outcome, contractsv1.ContextFabricRequirementNarrowed)
	}
	if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("the emitted row is not contract-valid: %v", err)
	}
}
