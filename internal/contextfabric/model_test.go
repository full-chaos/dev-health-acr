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

// TestValidateResult_DispatchesOnSchemaVersion is CHAOS-4042's own proof
// that the storage-adapter dispatch helper (ValidateResult, used by every
// Save call site instead of a bare result.Validate()) genuinely picks the
// validator matching the result's OWN persisted schema_version, never a
// fallthrough to the wrong major or a silent no-op on an unrecognized one.
func TestValidateResult_DispatchesOnSchemaVersion(t *testing.T) {
	t.Parallel()

	t.Run("v1 result validates under v1", func(t *testing.T) {
		t.Parallel()
		result := validInvestigationResult()
		if err := ValidateResult(result); err != nil {
			t.Errorf("ValidateResult() error = %v, want nil for a genuine v1 result", err)
		}
	})
	t.Run("v2 result validates under v2, not v1", func(t *testing.T) {
		t.Parallel()
		result := validInvestigationResult()
		result.SchemaVersion = InvestigationResultSchemaV2
		if err := ValidateResult(result); err != nil {
			t.Errorf("ValidateResult() error = %v, want nil for a genuine v2 result", err)
		}
		// The SAME payload must be REJECTED by the plain v1 Validate() --
		// proves the dispatch is not accidentally routing everything
		// through the same (looser) check.
		if err := result.Validate(); err == nil {
			t.Error("result.Validate() accepted a v2 schema_version; the v1-only entrypoint must reject it")
		}
	})
	t.Run("v1 result rejected if inspected under v2 semantics directly", func(t *testing.T) {
		t.Parallel()
		result := validInvestigationResult()
		if err := result.ValidateV2(); err == nil {
			t.Error("result.ValidateV2() accepted a v1 schema_version; the v2-only entrypoint must reject it")
		}
	})
	t.Run("unrecognized schema_version fails closed", func(t *testing.T) {
		t.Parallel()
		result := validInvestigationResult()
		result.SchemaVersion = "context_fabric_investigation_result.v99"
		if err := ValidateResult(result); err == nil {
			t.Error("ValidateResult() accepted an unrecognized schema_version; must fail closed, never silently pass")
		}
	})
}

// TestValidateStoredResult_DispatchesOnSchemaVersion mirrors
// TestValidateResult_DispatchesOnSchemaVersion for the READ-BACK path
// (memoryinvestigation/pginvestigation Get and FindReusable) -- proves a
// genuinely persisted v2 row is NOT rejected the instant it is read back,
// which the pre-CHAOS-4042 unconditional result.ValidateStored() call
// would have done.
func TestValidateStoredResult_DispatchesOnSchemaVersion(t *testing.T) {
	t.Parallel()

	t.Run("v1 stored result validates under v1", func(t *testing.T) {
		t.Parallel()
		result := validInvestigationResult()
		if err := ValidateStoredResult(result); err != nil {
			t.Errorf("ValidateStoredResult() error = %v, want nil for a genuine v1 stored result", err)
		}
	})
	t.Run("v2 stored result validates under v2, not v1", func(t *testing.T) {
		t.Parallel()
		result := validInvestigationResult()
		result.SchemaVersion = InvestigationResultSchemaV2
		if err := ValidateStoredResult(result); err != nil {
			t.Errorf("ValidateStoredResult() error = %v, want nil for a genuine v2 stored result", err)
		}
		if err := result.ValidateStored(); err == nil {
			t.Error("result.ValidateStored() accepted a v2 schema_version; the v1-only stored entrypoint must reject it")
		}
	})
	t.Run("unrecognized schema_version fails closed", func(t *testing.T) {
		t.Parallel()
		result := validInvestigationResult()
		result.SchemaVersion = "context_fabric_investigation_result.v99"
		if err := ValidateStoredResult(result); err == nil {
			t.Error("ValidateStoredResult() accepted an unrecognized schema_version; must fail closed, never silently pass")
		}
	})
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
		Versions:            VersionSet{ServiceVersion: "test-v1", ContractVersion: InvestigationResultSchemaV1, Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1", ModelIdentity: "test/model-v1"},
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
