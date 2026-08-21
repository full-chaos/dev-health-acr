package sidecar

import (
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func validSidecarInvestigationResult() contractsv1.ContextFabricInvestigationResult {
	project := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	return contractsv1.ContextFabricInvestigationResult{
		SchemaVersion: contractsv1.ContextFabricInvestigationResultSchema,
		ResultID:      "result_12345678",
		RequestID:     "request_12345678",
		GeneratedAt:   time.Now().UTC(),
		Status:        contractsv1.ContextFabricInvestigationComplete,
		Question:      "What is the actual status of Ask Dev and what is driving it?",
		Interpretation: contractsv1.ContextFabricInterpretedQuestion{
			Shape: contractsv1.ContextFabricShapeOpen, RequestedJudgment: "status_and_drivers", TimeContext: contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
			FactRequirements: []contractsv1.ContextFabricFactRequirement{{Kind: contractsv1.ContextFabricFactStatus}},
		},
		SubjectResolution:   contractsv1.ContextFabricSubjectResolution{Candidates: []contractsv1.ContextFabricSubjectCandidate{}, Committed: []contractsv1.ContextFabricSubjectRef{project}},
		DirectJudgment:      "Ask Dev is not release-ready.",
		CurrentState:        "Required work remains.",
		StrongestPressures:  []string{},
		Drivers:             []contractsv1.ContextFabricDriverJudgment{},
		RemainingWork:       []contractsv1.ContextFabricFinding{},
		ReadinessGaps:       []contractsv1.ContextFabricFinding{},
		Paths:               []contractsv1.ContextFabricRelationshipPath{},
		Conflicts:           []contractsv1.ContextFabricFinding{},
		Limitations:         []string{},
		EvidenceRefIDs:      []string{},
		ClaimedFacts:        []contractsv1.ContextFabricClaimedFact{},
		Coverage:            contractsv1.ContextFabricCoverage{Sources: []contractsv1.ContextFabricSourceObservation{}, DegradedReasons: []string{}},
		Versions:            contractsv1.ContextFabricVersionSet{ServiceVersion: "acr-v1", ContractVersion: contractsv1.ContextFabricInvestigationResultSchema, Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1", ModelIdentity: "test/model-v1"},
		DeterministicAnswer: "Ask Dev is not release-ready because required work remains.",
		Warnings:            []string{},
	}
}

// TestValidateStoredInvestigationResult_AcceptsV2Result is CHAOS-4042 PR3's
// own regression proof for the sidecar half of the codex-confirmed
// validator gap: before this fix, validateStoredInvestigationResult called
// result.ValidateStored() directly, which hardcodes the v1 schema_version
// constant -- a v2-stamped result (once offer minting is ever enabled)
// would be rejected outright as transport defense-in-depth failure, even
// though the server legitimately served it.
func TestValidateStoredInvestigationResult_AcceptsV2Result(t *testing.T) {
	t.Parallel()

	t.Run("v1-stamped result still accepted", func(t *testing.T) {
		result := validSidecarInvestigationResult()
		if err := validateStoredInvestigationResult(result); err != nil {
			t.Fatalf("validateStoredInvestigationResult() error = %v, want nil for a valid v1 result", err)
		}
	})

	t.Run("v2-stamped result accepted, not rejected as a v1 schema mismatch", func(t *testing.T) {
		result := validSidecarInvestigationResult()
		result.SchemaVersion = contractsv1.ContextFabricInvestigationResultSchemaV2
		if err := validateStoredInvestigationResult(result); err != nil {
			t.Fatalf("validateStoredInvestigationResult() error = %v, want nil for a valid v2 result", err)
		}
	})
}
