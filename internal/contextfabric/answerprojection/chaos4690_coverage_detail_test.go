package answerprojection

import (
	"reflect"
	"strconv"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// This file covers CHAOS-4690 Commit D: the answer projection surface for
// structured coverage details, source display labels, and evidence-ref
// labels (design §7.2). The projection contract fields (CoverageDetails,
// EvidenceRefLabels, ProjectedCoverage.Label/StateLabel) already exist on
// the wire as of ca02a246; this file proves Project() actually populates
// them, which it did not before this commit.

// richResultWithCoverageDetails clones richResult() and adds a degrading
// and a non-degrading coverage detail, source display labels, and an
// evidence-ref label map covering the fixture's full evidence-ref closure
// (computed via the same closure function the write-path validator uses,
// so this fixture stays honest about what a real engine-composed result
// looks like without hand-enumerating every ref id).
func richResultWithCoverageDetails() contractsv1.ContextFabricInvestigationResult {
	result := richResult()

	result.Coverage.Sources[0].Label = contractsv1.ContextFabricSourceObservationLabel(result.Coverage.Sources[0].Source)
	result.Coverage.Sources[0].StateLabel = contractsv1.ContextFabricSourceStateLabel(result.Coverage.Sources[0].State)
	result.Coverage.Sources[1].Label = contractsv1.ContextFabricSourceObservationLabel(result.Coverage.Sources[1].Source)
	result.Coverage.Sources[1].StateLabel = contractsv1.ContextFabricSourceStateLabel(result.Coverage.Sources[1].State)

	degrading := contractsv1.ContextFabricCoverageDetail{
		DetailID:  "cov-01",
		Source:    "deployments",
		Code:      contractsv1.ContextFabricCoverageDetailGraphEndpointLookupFailed,
		Degrading: true,
		Count:     intPtr(1),
		Raw:       "deployments unavailable",
	}
	degrading.Label = contractsv1.ComposeCoverageDetailLabel(degrading)

	nonDegrading := contractsv1.ContextFabricCoverageDetail{
		DetailID:  "cov-02",
		Source:    "work_items",
		Code:      contractsv1.ContextFabricCoverageDetailGraphExactNameCandidatesTruncated,
		Degrading: false,
	}
	nonDegrading.Label = contractsv1.ComposeCoverageDetailLabel(nonDegrading)

	result.Coverage.Details = []contractsv1.ContextFabricCoverageDetail{degrading, nonDegrading}

	closure := contractsv1.ContextFabricEvidenceRefClosure(result)
	labels := make(map[string]string, len(closure))
	for ref := range closure {
		label, _ := contractsv1.ContextFabricEvidenceRefLabel(ref)
		labels[ref] = label
	}
	result.EvidenceRefLabels = labels

	return result
}

func intPtr(v int) *int { return &v }

// TestFixtureWithCoverageDetailsIsCanonicallyValid keeps the rest of this
// file honest, the same way TestFixtureIsCanonicallyValid does for
// richResult() -- every assertion below describes how a VALID canonical
// result projects.
func TestFixtureWithCoverageDetailsIsCanonicallyValid(t *testing.T) {
	if err := richResultWithCoverageDetails().Validate(); err != nil {
		t.Fatalf("fixture is not a valid canonical result: %v", err)
	}
}

// TestProjectCoverageDetailsAreCopiedVerbatim is the parity test: the
// projected detail for a given DetailID must be byte-identical (deep-equal)
// to the canonical detail it came from -- Project selects and drops, it
// never rewrites (project.go's own PROJECTION RULE).
func TestProjectCoverageDetailsAreCopiedVerbatim(t *testing.T) {
	result := richResultWithCoverageDetails()
	projection := Project(result, Budget{})

	if len(projection.CoverageDetails) != len(result.Coverage.Details) {
		t.Fatalf("projected %d coverage details, canonical has %d", len(projection.CoverageDetails), len(result.Coverage.Details))
	}
	byID := make(map[string]contractsv1.ContextFabricCoverageDetail, len(projection.CoverageDetails))
	for _, detail := range projection.CoverageDetails {
		byID[detail.DetailID] = detail
	}
	for _, canonical := range result.Coverage.Details {
		projected, ok := byID[canonical.DetailID]
		if !ok {
			t.Fatalf("projection dropped detail %q", canonical.DetailID)
		}
		if !reflect.DeepEqual(projected, canonical) {
			t.Errorf("projected detail %q is not deep-equal to canonical:\n  got:  %+v\n  want: %+v", canonical.DetailID, projected, canonical)
		}
	}
	if projection.ProjectionBudget.CoverageOmitted != 0 {
		t.Errorf("coverage_omitted = %d, want 0 for a detail set under budget", projection.ProjectionBudget.CoverageOmitted)
	}
}

// TestProjectCoverageSourceLabelsCopiedVerbatim proves projectCoverage
// stamps Label/StateLabel from each retained source observation onto its
// ProjectedCoverage entry.
func TestProjectCoverageSourceLabelsCopiedVerbatim(t *testing.T) {
	result := richResultWithCoverageDetails()
	projection := Project(result, Budget{})

	bySource := make(map[string]contractsv1.ContextFabricSourceObservation, len(result.Coverage.Sources))
	for _, source := range result.Coverage.Sources {
		bySource[source.Source] = source
	}
	if len(projection.CoverageSummary) == 0 {
		t.Fatal("expected coverage summary entries")
	}
	for _, entry := range projection.CoverageSummary {
		canonical, ok := bySource[entry.Source]
		if !ok {
			t.Fatalf("projected coverage entry names unknown source %q", entry.Source)
		}
		if entry.Label != canonical.Label {
			t.Errorf("source %q: label = %q, want %q", entry.Source, entry.Label, canonical.Label)
		}
		if entry.StateLabel != canonical.StateLabel {
			t.Errorf("source %q: state_label = %q, want %q", entry.Source, entry.StateLabel, canonical.StateLabel)
		}
	}
}

// TestProjectCoverageDetailsOverflowSharesCoverageOmittedCounter proves
// that details beyond the projection's coverage-entry cap are counted into
// the SAME CoverageOmitted counter projectCoverage returns for oversized
// source lists -- design §7.2: "details share the coverage-entry cap;
// overflow is counted in ProjectionBudget.CoverageOmitted."
func TestProjectCoverageDetailsOverflowSharesCoverageOmittedCounter(t *testing.T) {
	result := richResultWithCoverageDetails()
	// No degrading detail beyond the one already paired with the fixture's
	// single DegradedReasons entry -- every additional detail here is
	// non-degrading, so the fixture's dual-write pairing invariant (which
	// this projection-level test does not exercise) stays satisfied
	// trivially by construction.
	result.Coverage.Details = result.Coverage.Details[1:2] // keep only the non-degrading one
	result.Coverage.DegradedReasons = nil
	for i := 0; i < 130; i++ {
		detail := contractsv1.ContextFabricCoverageDetail{
			DetailID: "cov-overflow-" + strconv.Itoa(i),
			Source:   "graph",
			Code:     contractsv1.ContextFabricCoverageDetailGraphExactNameCandidatesTruncated,
		}
		detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
		result.Coverage.Details = append(result.Coverage.Details, detail)
	}
	// 131 details total (1 kept + 130 generated); cap is
	// ContextFabricProjectedCoverageMaxCount (100), so 31 must be omitted.
	const wantOmitted = 131 - contractsv1.ContextFabricProjectedCoverageMaxCount

	// Coverage.Sources stays small so the ONLY coverage omission source is
	// the details overflow -- isolating what this test actually proves.
	projection := Project(result, Budget{})

	if len(projection.CoverageDetails) != contractsv1.ContextFabricProjectedCoverageMaxCount {
		t.Errorf("projected %d coverage details, want the cap %d", len(projection.CoverageDetails), contractsv1.ContextFabricProjectedCoverageMaxCount)
	}
	if projection.ProjectionBudget.CoverageOmitted != wantOmitted {
		t.Errorf("coverage_omitted = %d, want %d", projection.ProjectionBudget.CoverageOmitted, wantOmitted)
	}
	if !projection.ProjectionBudget.Truncated {
		t.Error("dropping coverage details must set truncated")
	}
}

// TestProjectEvidenceRefLabelsFilteredToRetainedRefs is the mutation-proof
// target for the label filter: with a tight evidence budget, the projected
// EvidenceRefLabels map must key EXACTLY the refs the projection retained
// in EvidenceRefIDs -- never the full canonical closure. A ref the evidence
// budget dropped must have no label entry; every retained ref must have
// one, equal to the canonical label.
func TestProjectEvidenceRefLabelsFilteredToRetainedRefs(t *testing.T) {
	result := richResultWithCoverageDetails()
	fullClosure := contractsv1.ContextFabricEvidenceRefClosure(result)
	if len(fullClosure) < 3 {
		t.Fatalf("fixture closure too small to prove filtering (%d refs)", len(fullClosure))
	}

	projection := Project(result, Budget{MaxEvidenceRefs: 3})

	if len(projection.EvidenceRefIDs) >= len(fullClosure) {
		t.Fatalf("test requires the budget to actually drop refs: retained %d of %d", len(projection.EvidenceRefIDs), len(fullClosure))
	}
	retained := make(map[string]struct{}, len(projection.EvidenceRefIDs))
	for _, id := range projection.EvidenceRefIDs {
		retained[id] = struct{}{}
	}

	if len(projection.EvidenceRefLabels) != len(retained) {
		t.Fatalf("evidence_ref_labels has %d entries, want exactly %d (the retained ref count)", len(projection.EvidenceRefLabels), len(retained))
	}
	for id := range retained {
		label, ok := projection.EvidenceRefLabels[id]
		if !ok {
			t.Errorf("retained ref %q has no label entry", id)
			continue
		}
		if label != result.EvidenceRefLabels[id] {
			t.Errorf("retained ref %q label = %q, want canonical %q", id, label, result.EvidenceRefLabels[id])
		}
	}
	for ref := range fullClosure {
		if _, isRetained := retained[ref]; isRetained {
			continue
		}
		if _, present := projection.EvidenceRefLabels[ref]; present {
			t.Errorf("ref %q was dropped by the evidence budget but still has a label entry", ref)
		}
	}
}

// TestProjectEvidenceRefLabelsNilForLegacyResult proves the ruled exception
// (design §7.3): a result with no EvidenceRefLabels map (every result
// stored before CHAOS-4690) projects to a nil map, never a synthesized
// empty one.
func TestProjectEvidenceRefLabelsNilForLegacyResult(t *testing.T) {
	result := richResult() // richResult() never sets EvidenceRefLabels
	if result.EvidenceRefLabels != nil {
		t.Fatal("fixture precondition failed: expected nil EvidenceRefLabels")
	}

	projection := Project(result, Budget{})

	if projection.EvidenceRefLabels != nil {
		t.Errorf("evidence_ref_labels = %v, want nil for a legacy result", projection.EvidenceRefLabels)
	}
}

// TestProjectEvidenceRefLabelsNonNilEvenWhenNothingRetained is the direct
// (white-box) proof that projectEvidenceRefLabels distinguishes "the
// result carries a label map, but the budget kept no refs" (non-nil empty
// map) from "the result predates labels" (nil) -- the same distinction
// project.go's own doc comment draws.
func TestProjectEvidenceRefLabelsNonNilEvenWhenNothingRetained(t *testing.T) {
	result := richResultWithCoverageDetails()

	got := projectEvidenceRefLabels(result, nil)

	if got == nil {
		t.Fatal("expected a non-nil (possibly empty) map when the result carries a label map")
	}
	if len(got) != 0 {
		t.Errorf("expected an empty map for zero retained refs, got %d entries", len(got))
	}
}
