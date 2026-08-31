package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4690 item 4: applyCoverageDisplayLabels is the single composer
// called at every fresh-result exit, immediately before Validate. These
// tests exercise it directly against hand-built InvestigationResult
// literals -- both decisive- and terminal-shaped, per the brief's own
// "exact-closure on a decisive and a terminal result" requirement -- so
// the composer's own correctness is pinned independent of how any
// particular Investigate() exit happens to construct its result.

// --- Source/detail label stamping (the "terminals compose Coverage
// directly" case: no MergeCoverage call, so nothing else would ever stamp
// these) ---

func TestChaos4690_ApplyCoverageDisplayLabels_StampsUnmergedSourceLabels(t *testing.T) {
	t.Parallel()
	result := &InvestigationResult{
		Coverage: Coverage{
			Sources: []SourceObservation{
				{Source: "context-fabric:graph", State: SourceUnavailable},
			},
		},
	}
	applyCoverageDisplayLabels(result)

	wantLabel := contractsv1.ContextFabricSourceObservationLabel("context-fabric:graph")
	wantStateLabel := contractsv1.ContextFabricSourceStateLabel(SourceUnavailable)
	got := result.Coverage.Sources[0]
	if got.Label != wantLabel {
		t.Fatalf("Label = %q, want %q", got.Label, wantLabel)
	}
	if got.StateLabel != wantStateLabel {
		t.Fatalf("StateLabel = %q, want %q", got.StateLabel, wantStateLabel)
	}
}

func TestChaos4690_ApplyCoverageDisplayLabels_RecomputesDetailLabel(t *testing.T) {
	t.Parallel()
	count := 3
	result := &InvestigationResult{
		Coverage: Coverage{
			Details: []CoverageDetail{{
				DetailID: "cov-01", Source: "context-fabric:graph",
				Code: contractsv1.ContextFabricCoverageDetailGraphEndpointLookupFailed,
				Degrading: true, Count: &count, Raw: "endpoint_lookup_failed:3",
				Label: "", // deliberately unstamped -- a terminal path composing Coverage by hand
			}},
		},
	}
	applyCoverageDisplayLabels(result)

	want := contractsv1.ComposeCoverageDetailLabel(result.Coverage.Details[0])
	if result.Coverage.Details[0].Label == "" {
		t.Fatal("Label is still empty after applyCoverageDisplayLabels")
	}
	if result.Coverage.Details[0].Label != want {
		t.Fatalf("Label = %q, want the registry composition %q", result.Coverage.Details[0].Label, want)
	}
}

// --- EvidenceRefLabels exact closure: decisive-shaped result ---

func TestChaos4690_ApplyCoverageDisplayLabels_DecisiveResultExactClosureAndFallbackCount(t *testing.T) {
	t.Parallel()
	result := &InvestigationResult{
		EvidenceRefIDs: []string{"acr:v1:work-item:WI-100"},
		Drivers: []DriverJudgment{{
			DriverID: "driver_1", EvidenceRefIDs: []string{"acr:v1:pull-request:PR-7", "acr:v1:mystery-entity:XYZ-1"},
		}},
		RemainingWork: []Finding{{FindingID: "finding_1", EvidenceRefIDs: []string{"acr:v1:work-item:WI-100"}}}, // same ref, must not double count
	}
	fallbacks := applyCoverageDisplayLabels(result)

	wantClosure := map[string]struct{}{
		"acr:v1:work-item:WI-100":     {},
		"acr:v1:pull-request:PR-7":    {},
		"acr:v1:mystery-entity:XYZ-1": {},
	}
	if len(result.EvidenceRefLabels) != len(wantClosure) {
		t.Fatalf("EvidenceRefLabels = %#v, want exactly %d entries (the result's own evidence-ref closure)", result.EvidenceRefLabels, len(wantClosure))
	}
	for ref := range wantClosure {
		if _, ok := result.EvidenceRefLabels[ref]; !ok {
			t.Fatalf("EvidenceRefLabels = %#v, missing closure member %q", result.EvidenceRefLabels, ref)
		}
	}
	if got := result.EvidenceRefLabels["acr:v1:work-item:WI-100"]; got != "Work item: WI-100" {
		t.Fatalf("label for a KNOWN segment = %q, want the registry label", got)
	}
	if got := result.EvidenceRefLabels["acr:v1:mystery-entity:XYZ-1"]; got == "Evidence" {
		t.Fatalf("label for an unknown segment with an id = %q, want the id-carrying fallback \"Evidence: XYZ-1\", not the bare generic", got)
	}
	if fallbacks != 1 {
		t.Fatalf("fallbacks = %d, want exactly 1 (only the unknown mystery-entity segment)", fallbacks)
	}
}

// --- EvidenceRefLabels exact closure: terminal-shaped result (no evidence
// at all -- a subjectless terminal never read a canonical fact) ---

func TestChaos4690_ApplyCoverageDisplayLabels_TerminalResultEmptyClosureIsNonNilMap(t *testing.T) {
	t.Parallel()
	result := &InvestigationResult{
		EvidenceRefIDs: []string{},
		Drivers:        []DriverJudgment{},
		RemainingWork:  []Finding{},
		ReadinessGaps:  []Finding{},
		Conflicts:      []Finding{},
	}
	fallbacks := applyCoverageDisplayLabels(result)

	if result.EvidenceRefLabels == nil {
		t.Fatal("EvidenceRefLabels is nil, want a non-nil (possibly empty) map on every FRESH result -- nil is reserved for a legacy stored result that predates this field (design §7.3)")
	}
	if len(result.EvidenceRefLabels) != 0 {
		t.Fatalf("EvidenceRefLabels = %#v, want empty: a subjectless terminal cites no evidence", result.EvidenceRefLabels)
	}
	if fallbacks != 0 {
		t.Fatalf("fallbacks = %d, want 0", fallbacks)
	}
}

func TestChaos4690_ApplyCoverageDisplayLabels_AllKnownSegmentsZeroFallbacks(t *testing.T) {
	t.Parallel()
	result := &InvestigationResult{
		EvidenceRefIDs: []string{"acr:v1:commit:abc123", "acr:v1:incident:INC-1"},
	}
	fallbacks := applyCoverageDisplayLabels(result)
	if fallbacks != 0 {
		t.Fatalf("fallbacks = %d, want 0: every segment (commit, incident) is in the registry", fallbacks)
	}
}
