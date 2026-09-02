package answerprojection

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The reuse degrade path removes evidence references the authorization
// recheck could not prove from the payload it serves. This package is the
// ONE narrowing surface every consumer of that payload goes through, and it
// derives its own evidence lists and its own label map from the result it
// is given.
//
// So there is a question the degrade cannot answer on its own: can
// projection put back a reference the degrade removed? If it derives
// everything from the result, no; if it consults any other source, the
// invariant would leak through this surface no matter how careful the strip
// is. This test settles it by EXECUTION rather than by reading the code,
// because "it only filters" is exactly the kind of claim that stays true
// until it quietly is not.
func TestProjectionNeverReintroducesARefAbsentFromTheResultItProjects(t *testing.T) {
	t.Parallel()

	stripped := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, "pr_removed_by_the_recheck")

	// A result in the state the degrade leaves behind: the reference is
	// gone from every list AND from the label map.
	result := degradedResultFixture(t)
	if strings.Contains(mustJSON(t, result), stripped) {
		t.Fatalf("fixture still mentions %q; the test would prove nothing", stripped)
	}

	projection := Project(result, Budget{})

	// The decisive assertion: serialize the WHOLE projection and look for
	// the reference anywhere in it. Checking named fields would only cover
	// the fields whoever wrote this test thought of, which is the same
	// mistake that let the reference leak through a label map.
	if encoded := mustJSON(t, projection); strings.Contains(encoded, stripped) {
		t.Fatalf("projection reintroduced %q, which the payload it projected does not carry", stripped)
	}

	// And the positive control: a reference the result DOES carry must
	// survive, or the test above would pass on an empty projection.
	if len(result.EvidenceRefIDs) == 0 {
		t.Fatal("fixture carries no citations; the negative result above would be vacuous")
	}
	if encoded := mustJSON(t, projection); !strings.Contains(encoded, result.EvidenceRefIDs[0]) {
		t.Fatalf("projection dropped the still-carried citation %q; the negative assertion above proves nothing on an empty projection", result.EvidenceRefIDs[0])
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}

func degradedResultFixture(t *testing.T) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	subject := contractsv1.ContextFabricSubjectRef{
		Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev",
	}
	kept := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, "pr_still_visible")
	result := contractsv1.ContextFabricInvestigationResult{
		SchemaVersion: contractsv1.ContextFabricInvestigationResultSchema,
		ResultID:      "result_degraded_1", RequestID: "request_degraded1",
		GeneratedAt: time.Now().UTC(), Status: contractsv1.ContextFabricInvestigationPartial,
		Question: "What is the status of Ask Dev?",
		Interpretation: contractsv1.ContextFabricInterpretedQuestion{
			Shape: contractsv1.ContextFabricShapeOpen, RequestedJudgment: "status_and_drivers",
			TimeContext:      contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
			FactRequirements: []contractsv1.ContextFabricFactRequirement{{Kind: contractsv1.ContextFabricFactStatus}},
		},
		SubjectResolution: contractsv1.ContextFabricSubjectResolution{
			Candidates: []contractsv1.ContextFabricSubjectCandidate{}, Committed: []contractsv1.ContextFabricSubjectRef{subject},
		},
		DirectJudgment: "Ask Dev is not release-ready.", CurrentState: "Acceptance is incomplete.",
		StrongestPressures: []string{},
		// A driver CITING the kept reference. The projection builds its
		// evidence index as content is admitted, so a reference no
		// retained item cites is never projected at all -- without this
		// the positive control below would be asserting on an empty
		// projection, which would make the negative assertion worthless.
		Drivers: []contractsv1.ContextFabricDriverJudgment{{
			DriverID: "driver_degraded_01", Standing: contractsv1.ContextFabricDriverPrincipal,
			Category:         string(contractsv1.ContextFabricDriverCategoryRelationship),
			Title:            "Acceptance work is the binding constraint",
			Summary:          "Open acceptance work is what keeps the project short of ready.",
			AffectedSubjects: []contractsv1.ContextFabricSubjectRef{subject},
			Derivation:       contractsv1.ContextFabricDerivationGraphAssociated,
			EpistemicStatus:  contractsv1.ContextFabricEpistemicInferred, Confidence: 0.7,
			EvidenceRefIDs: []string{kept},
		}},
		RemainingWork: []contractsv1.ContextFabricFinding{}, ReadinessGaps: []contractsv1.ContextFabricFinding{},
		Paths: []contractsv1.ContextFabricRelationshipPath{}, Conflicts: []contractsv1.ContextFabricFinding{},
		Limitations: []string{}, EvidenceRefIDs: []string{kept},
		ClaimedFacts: []contractsv1.ContextFabricClaimedFact{},
		Coverage: contractsv1.ContextFabricCoverage{
			Sources: []contractsv1.ContextFabricSourceObservation{}, DegradedReasons: []string{},
		},
		Versions: contractsv1.ContextFabricVersionSet{
			ServiceVersion: "test-v1", ContractVersion: contractsv1.ContextFabricInvestigationResultSchema,
			Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
			InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
			CanonicalServiceVersion: "ops-v1", ModelIdentity: "test/model-v1",
		},
		DeterministicAnswer: "Ask Dev is not release-ready because acceptance work remains open.",
		Warnings:            []string{},
		EvidenceRefLabels:   map[string]string{},
	}
	for ref := range contractsv1.ContextFabricEvidenceRefClosure(result) {
		label, _ := contractsv1.ContextFabricEvidenceRefLabel(ref)
		result.EvidenceRefLabels[ref] = label
	}
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	if err := contractsv1.ValidateStoredResult(result); err != nil {
		t.Fatalf("fixture does not validate: %v", err)
	}
	return result
}
