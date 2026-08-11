package contextfabric

import (
	"strings"
	"testing"
	"time"
)

func TestInvestigationRequestValidateAcceptsOpenEndedQuestion(t *testing.T) {
	t.Parallel()

	request := validInvestigationRequest()
	request.Question = "Where does the organization appear to be under the most delivery pressure, and what is connecting those problems?"
	request.TimeContext = TimeContext{Axis: TemporalCurrent}

	if err := request.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestInvestigationRequestValidateDoesNotRequireRegisteredIntent(t *testing.T) {
	t.Parallel()

	request := validInvestigationRequest()
	request.Question = "It says most of the work is closed, so why can’t the thing we discussed last turn actually ship?"
	request.Conversation = []ConversationTurn{{
		TurnID: "turn_previous", Role: ConversationUser, Content: "Tell me about Ask Dev.", CreatedAt: time.Now().UTC(),
	}}

	if err := request.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestInvestigationRequestValidateRejectsInvalidTemporalShape(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	request := validInvestigationRequest()
	request.TimeContext = TimeContext{Axis: TemporalCurrent, AsOf: &now}

	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "time_context") {
		t.Fatalf("Validate() error = %v, want time_context error", err)
	}
}

func TestInvestigationResultValidateRequiresDirectAnswerForSupportedResult(t *testing.T) {
	t.Parallel()

	result := validInvestigationResult()
	result.DirectJudgment = ""

	if err := result.Validate(); err == nil || !strings.Contains(err.Error(), "direct judgment") {
		t.Fatalf("Validate() error = %v, want direct judgment error", err)
	}
}

func TestInvestigationResultValidateRequiresEvidenceClosedDrivers(t *testing.T) {
	t.Parallel()

	result := validInvestigationResult()
	result.Drivers = []DriverJudgment{{
		DriverID: "driver_1", Standing: DriverPrincipal, Title: "Release readiness", Summary: "Release readiness remains incomplete.",
	}}

	if err := result.Validate(); err == nil || !strings.Contains(err.Error(), "lacks evidence closure") {
		t.Fatalf("Validate() error = %v, want evidence closure error", err)
	}
}

func TestProjectionBatchValidateRequiresCompleteEnumerationForFullSnapshot(t *testing.T) {
	t.Parallel()

	batch := validProjectionBatch()
	batch.FullSnapshot = true
	batch.CompleteEnumeration = false

	if err := batch.Validate(); err == nil || !strings.Contains(err.Error(), "complete enumeration") {
		t.Fatalf("Validate() error = %v, want complete enumeration error", err)
	}
}

func validInvestigationRequest() InvestigationRequest {
	return InvestigationRequest{
		SchemaVersion: InvestigationRequestSchemaV1,
		RequestID:     "request_12345678",
		Question:      "What is the actual status of Ask Dev and what is driving it?",
		TimeContext:   TimeContext{Axis: TemporalCurrent},
		Options: InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20, AllowClarification: true,
		},
		Consumer: ConsumerInfo{Name: "context-fabric-workbench", Version: "0.1.0", Surface: "workbench"},
	}
}

func validInvestigationResult() InvestigationResult {
	return InvestigationResult{
		SchemaVersion:       InvestigationResultSchemaV1,
		ResultID:            "result_12345678",
		RequestID:           "request_12345678",
		GeneratedAt:         time.Now().UTC(),
		Status:              InvestigationComplete,
		Question:            "What is the actual status of Ask Dev and what is driving it?",
		DirectJudgment:      "Ask Dev is not release-ready.",
		DeterministicAnswer: "Ask Dev is not release-ready because required acceptance work remains open.",
	}
}

func validProjectionBatch() ProjectionBatch {
	return ProjectionBatch{
		SchemaVersion: ProjectionBatchSchemaV1,
		BatchID:       "batch_12345678",
		OrgID:         "org_1",
		Source:        "dev-health-ops",
		SourceVersion: "source_v1",
		GeneratedAt:   time.Now().UTC(),
		Entities: []EntityProjection{{
			Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_1", Label: "Ask Dev"},
			ObservedAt: time.Now().UTC(), SourceVersion: "source_v1",
		}},
	}
}
