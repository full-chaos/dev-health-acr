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
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	result.Drivers = []DriverJudgment{{
		DriverID: "driver_12345678", Standing: DriverPrincipal, Category: "relationship",
		Title: "Release readiness", Summary: "Release readiness remains incomplete.",
		AffectedSubjects: []SubjectRef{project}, EvidenceRefIDs: []string{},
		Derivation: DerivationRuleInferred, EpistemicStatus: EpistemicInferred,
		Confidence: 0.9, Current: true,
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
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	return InvestigationResult{
		SchemaVersion: InvestigationResultSchemaV1,
		ResultID:      "result_12345678",
		RequestID:     "request_12345678",
		GeneratedAt:   time.Now().UTC(),
		Status:        InvestigationComplete,
		Question:      "What is the actual status of Ask Dev and what is driving it?",
		Interpretation: InterpretedQuestion{
			Shape: ShapeOpen, RequestedJudgment: "status_and_drivers",
			TimeContext:      TimeContext{Axis: TemporalCurrent},
			FactRequirements: []FactRequirement{{Kind: FactStatus}},
		},
		SubjectResolution:   SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		DirectJudgment:      "Ask Dev is not release-ready.",
		CurrentState:        "Release acceptance remains incomplete.",
		StrongestPressures:  []string{},
		Drivers:             []DriverJudgment{},
		RemainingWork:       []Finding{},
		ReadinessGaps:       []Finding{},
		Paths:               []RelationshipPath{},
		Conflicts:           []Finding{},
		Limitations:         []string{},
		EvidenceRefIDs:      []string{},
		ClaimedFacts:        []ClaimedFact{},
		Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		Versions:            VersionSet{ServiceVersion: "test-v1", ContractVersion: InvestigationResultSchemaV1, Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1"},
		DeterministicAnswer: "Ask Dev is not release-ready because required acceptance work remains open.",
		Warnings:            []string{},
	}
}

func validProjectionBatch() ProjectionBatch {
	now := time.Now().UTC()
	return ProjectionBatch{
		SchemaVersion: ProjectionBatchSchemaV1,
		BatchID:       "batch_12345678",
		OrgID:         "org_1",
		Source:        "dev-health-ops",
		SourceVersion: "source_v1",
		Cursor:        "cursor_1",
		NextCursor:    "cursor_2",
		GeneratedAt:   now,
		Entities: []EntityProjection{{
			Subject:        SubjectRef{Kind: SubjectProject, CanonicalID: "project_1", Label: "Ask Dev"},
			Authorization:  AuthorizationScope{ProjectIDs: []string{"project_1"}},
			EvidenceRefIDs: []string{"evidence_project_1"}, ObservedAt: now, SourceVersion: "source_v1",
		}},
		Relationships: []RelationshipProjection{},
		Contents:      []ContentProjection{},
		Episodes:      []EpisodeProjection{},
		Tombstones:    []ProjectionTombstone{},
	}
}
