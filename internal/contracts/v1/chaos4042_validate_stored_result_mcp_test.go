package v1_test

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func validMCPTestResult(schemaVersion string) contractsv1.ContextFabricInvestigationResult {
	project := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	return contractsv1.ContextFabricInvestigationResult{
		SchemaVersion: schemaVersion,
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
		Versions:            contractsv1.ContextFabricVersionSet{ServiceVersion: "acr-v1", ContractVersion: schemaVersion, Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1", ModelIdentity: "test/model-v1"},
		DeterministicAnswer: "Ask Dev is not release-ready because required work remains.",
		Warnings:            []string{},
		Completeness:        contractsv1.ContextFabricAnswerCompleteness{TerminalStatus: contractsv1.ContextFabricInvestigationComplete},
	}
}

func validRenderedMarkdown() contractsv1.MCPRenderedMarkdown {
	return contractsv1.MCPRenderedMarkdown{Markdown: "Ask Dev is not release-ready.", Untrusted: true}
}

// TestMCPInvestigateQuestionResponse_ValidateAcceptsV2FullResult is
// CHAOS-4042 PR3's own regression proof for the codex-confirmed MCP
// response validator gap: MCPInvestigateQuestionResponse.Validate() must
// accept a v2-stamped FullResult via the new ValidateStoredResult
// dispatcher, not reject it as a v1-only ValidateStored() call would.
func TestMCPInvestigateQuestionResponse_ValidateAcceptsV2FullResult(t *testing.T) {
	t.Parallel()

	result := validMCPTestResult(contractsv1.ContextFabricInvestigationResultSchemaV2)
	projection := answerprojection.Project(result, answerprojection.DefaultBudget)

	response := contractsv1.MCPInvestigateQuestionResponse{
		SchemaVersion:    contractsv1.MCPInvestigateQuestionResponseSchema,
		Structured:       projection,
		FullResult:       &result,
		RenderedMarkdown: validRenderedMarkdown(),
		UntrustedContent: contractsv1.MCPUntrustedContent{
			Untrusted: true, Notice: contractsv1.MCPUntrustedContentNotice, Fields: contractsv1.MCPInvestigateQuestionUntrustedFields,
		},
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("MCPInvestigateQuestionResponse.Validate() error = %v, want nil for a valid v2-stamped full_result", err)
	}

	// Regression pin: if this call site reverted to r.FullResult.ValidateStored()
	// directly, it would reject this exact fixture -- proving the test is not
	// vacuous.
	if err := result.ValidateStored(); err == nil {
		t.Fatal("result.ValidateStored() error = nil, want an error for a v2-stamped result -- if this ever starts passing, this test's premise is stale")
	}
}

// TestMCPInvestigationResultResponse_ValidateAcceptsV2Structured mirrors
// TestMCPInvestigateQuestionResponse_ValidateAcceptsV2FullResult for the
// investigation_result MCP tool's own response type.
func TestMCPInvestigationResultResponse_ValidateAcceptsV2Structured(t *testing.T) {
	t.Parallel()

	result := validMCPTestResult(contractsv1.ContextFabricInvestigationResultSchemaV2)
	response := contractsv1.MCPInvestigationResultResponse{
		SchemaVersion:    contractsv1.MCPInvestigationResultResponseSchema,
		Structured:       result,
		RenderedMarkdown: validRenderedMarkdown(),
		UntrustedContent: contractsv1.MCPUntrustedContent{
			Untrusted: true, Notice: contractsv1.MCPUntrustedContentNotice, Fields: contractsv1.MCPInvestigationResultUntrustedFields,
		},
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("MCPInvestigationResultResponse.Validate() error = %v, want nil for a valid v2-stamped structured result", err)
	}

	if err := result.ValidateStored(); err == nil {
		t.Fatal("result.ValidateStored() error = nil, want an error for a v2-stamped result -- if this ever starts passing, this test's premise is stale")
	}
}
