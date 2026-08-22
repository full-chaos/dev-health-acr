package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// parityResult is the canonical result both surfaces answer from. It is
// deliberately rich enough that a projection has to make real choices:
// drivers across several standings including a withheld one, claimed facts,
// a cohort, and partial coverage.
func parityResult() contractsv1.ContextFabricInvestigationResult {
	project := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	teamA := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team_a", Label: "Team A"}
	teamB := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team_b", Label: "Team B"}
	amber := "amber"
	blockers := "3"

	parityDriver := func(id string, standing contractsv1.ContextFabricDriverStanding, category, title string, claims []string) contractsv1.ContextFabricDriverJudgment {
		return contractsv1.ContextFabricDriverJudgment{
			DriverID: id, Standing: standing, Category: category, Title: title,
			Summary:          title + " summary.",
			AffectedSubjects: []contractsv1.ContextFabricSubjectRef{project},
			EvidenceRefIDs:   []string{"evidence_" + id},
			ClaimedFactIDs:   claims,
			Derivation:       contractsv1.ContextFabricDerivationCanonicalStructured,
			EpistemicStatus:  contractsv1.ContextFabricEpistemicObserved,
			Confidence:       0.83,
			Current:          true,
		}
	}
	withheld := parityDriver("driver_withheld_01", contractsv1.ContextFabricDriverWithheld, "narrative", "Withheld", nil)
	withheld.Qualification = "Evidence was too thin to stand behind."

	return contractsv1.ContextFabricInvestigationResult{
		SchemaVersion: contractsv1.ContextFabricInvestigationResultSchema,
		ResultID:      "result_parity_0001", RequestID: "request_parity_001",
		GeneratedAt: time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC),
		Status:      contractsv1.ContextFabricInvestigationComplete,
		Question:    "Which teams need attention on Ask Dev, and why?",
		Interpretation: contractsv1.ContextFabricInterpretedQuestion{
			Shape: contractsv1.ContextFabricShapeDiscoveredCohort, RequestedJudgment: "attention_and_drivers",
			TimeContext:      contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
			FactRequirements: []contractsv1.ContextFabricFactRequirement{{Kind: contractsv1.ContextFabricFactStatus}},
		},
		SubjectResolution: contractsv1.ContextFabricSubjectResolution{
			Candidates: []contractsv1.ContextFabricSubjectCandidate{{
				ReceiptID: "receipt_parity_0001", Subject: project, State: contractsv1.ContextFabricResolutionCommitted,
				MatchReasons: []string{"exact label"}, Confidence: 0.98, EvidenceRefIDs: []string{},
			}},
			Committed: []contractsv1.ContextFabricSubjectRef{project},
		},
		DirectJudgment:     "Two teams need attention on Ask Dev.",
		CurrentState:       "Blockers and stalled reviews concentrate on Team A.",
		StrongestPressures: []string{"open blockers", "stalled reviews"},
		Drivers: []contractsv1.ContextFabricDriverJudgment{
			parityDriver("driver_symptom_001", contractsv1.ContextFabricDriverSymptom, "reviews", "Reviews stalled", []string{"claim_reviews_01"}),
			parityDriver("driver_principal_1", contractsv1.ContextFabricDriverPrincipal, "blockers", "Blockers remain", []string{"claim_blockers_1"}),
			withheld,
			parityDriver("driver_principal_2", contractsv1.ContextFabricDriverPrincipal, "status", "Status is amber", []string{"claim_status_001"}),
			parityDriver("driver_contrib_001", contractsv1.ContextFabricDriverContributing, "work", "Work outstanding", []string{"claim_work_00001"}),
		},
		RemainingWork: []contractsv1.ContextFabricFinding{},
		ReadinessGaps: []contractsv1.ContextFabricFinding{},
		Paths:         []contractsv1.ContextFabricRelationshipPath{},
		Conflicts:     []contractsv1.ContextFabricFinding{},
		Cohort: &contractsv1.ContextFabricCohort{
			Kind: contractsv1.ContextFabricSubjectTeam,
			Members: []contractsv1.ContextFabricCohortMember{
				{Subject: teamA, Rank: 1, InclusionReasons: []string{"highest blocker load"}, EvidenceRefIDs: []string{"evidence_cohort_a"}},
				{Subject: teamB, Rank: 2, InclusionReasons: []string{"rising review latency"}, EvidenceRefIDs: []string{"evidence_cohort_b"}},
			},
			Rationale: "Teams carrying the most open blockers.",
			Complete:  true,
		},
		Limitations:    []string{"deployments source unavailable"},
		EvidenceRefIDs: []string{"evidence_driver_principal_1"},
		ClaimedFacts: []contractsv1.ContextFabricClaimedFact{
			{ClaimID: "claim_blockers_1", Kind: contractsv1.ContextFabricFactBlockers, Subject: project, Field: "open_blockers", Value: contractsv1.ContextFabricScalarValue{String: &blockers}},
			{ClaimID: "claim_status_001", Kind: contractsv1.ContextFabricFactStatus, Subject: project, Field: "status", Value: contractsv1.ContextFabricScalarValue{String: &amber}},
			{ClaimID: "claim_reviews_01", Kind: contractsv1.ContextFabricFactReviews, Subject: project, Field: "stalled_reviews", Value: contractsv1.ContextFabricScalarValue{String: &blockers}},
			{ClaimID: "claim_work_00001", Kind: contractsv1.ContextFabricFactWork, Subject: project, Field: "open_items", Value: contractsv1.ContextFabricScalarValue{String: &blockers}},
		},
		Coverage: contractsv1.ContextFabricCoverage{
			Sources: []contractsv1.ContextFabricSourceObservation{
				{Source: "work_items", State: contractsv1.ContextFabricSourceAvailable},
				{Source: "deployments", State: contractsv1.ContextFabricSourceUnavailable, Reason: "source not configured"},
			},
			Partial:         true,
			DegradedReasons: []string{"deployments unavailable"},
		},
		Versions: contractsv1.ContextFabricVersionSet{
			ServiceVersion: "acr-v1", ContractVersion: contractsv1.ContextFabricInvestigationResultSchema, Backend: "graph",
			ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1",
			SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1",
		},
		DeterministicAnswer: "Two teams need attention because blockers and stalled reviews concentrate there.",
		Warnings:            []string{},
	}
}

// answerFixtureBootstrap serves the answer endpoints from an httptest
// server and advertises the answer tools, so the sidecar registers them.
func answerFixtureBootstrap(t *testing.T, result contractsv1.ContextFabricInvestigationResult, seen *contractsv1.ContextFabricInvestigationRequest) *Bootstrap {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/context-fabric/investigations":
			if seen != nil {
				if err := json.NewDecoder(r.Body).Decode(seen); err != nil {
					t.Errorf("decode investigation request: %v", err)
				}
			}
			writeJSONFixture(t, w, http.StatusOK, result)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/context-fabric/investigations/"+result.ResultID:
			writeJSONFixture(t, w, http.StatusOK, result)
		default:
			writeErrorFixture(t, w, http.StatusNotFound, "not_found", false)
		}
	}))
	t.Cleanup(server.Close)

	cfg := fixtureConfig(t, server)
	// Raised to the sidecar's own configuration ceiling so the transport
	// is not what fails when the wrapper closure proof drives a very large
	// payload. Note this ceiling is REAL: the canonical contract permits
	// results larger than any sidecar can be configured to read, so the
	// largest legal results are unreachable through MCP by construction.
	// That contract-versus-deployment mismatch is tracked as CHAOS-3795;
	// it predates this surface and is not resolved here.
	// The wrapper proof therefore uses the largest TRANSPORTABLE result;
	// the true contract-maximum closure is proven at the validator level
	// in internal/contextfabric/answerprojection, where no transport is
	// involved.
	cfg.MaxResponseBytes = 8 << 20
	client, err := sidecar.NewClient(cfg, fixedCredentialSource(fixtureToken(0xAB)))
	if err != nil {
		t.Fatal(err)
	}
	caps := validCapabilitiesFixture()
	caps.EnabledTools = append(caps.EnabledTools, toolInvestigateQuestion, toolInvestigationResult)
	return &Bootstrap{Config: cfg, Client: client, Capabilities: caps}
}

func callInvestigateQuestion(t *testing.T, boot *Bootstrap, input contractsv1.MCPInvestigateQuestionRequest) contractsv1.MCPInvestigateQuestionResponse {
	t.Helper()
	args, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := handleInvestigateQuestion(context.Background(), boot, &mcpsdk.CallToolRequest{
		Params: &mcpsdk.CallToolParamsRaw{Arguments: args},
	})
	if err != nil {
		t.Fatalf("handleInvestigateQuestion returned a protocol error: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool reported an error: %s", toolResultText(result))
	}
	var response contractsv1.MCPInvestigateQuestionResponse
	if err := json.Unmarshal(result.StructuredContent.(json.RawMessage), &response); err != nil {
		t.Fatalf("decode structured content: %v", err)
	}
	return response
}

// The API-versus-MCP differential parity check does NOT live here. It needs
// both REAL surfaces -- the hosted route and this tool -- wired against one
// another, so it lives in internal/api as
// TestAnswerSurfaceParityBetweenRealAPIAndRealMCP, where a real api.App can
// be constructed. A version of it that lived here could only ever compare
// this tool against a direct call to the projection helper, which proves
// the helper is deterministic rather than that the two surfaces agree
// (codex round-1 F2).

// TestInvestigateQuestionPreservesJudgmentFromTheCanonicalResult states the
// parity requirement in the issue's own terms, independently of the shared
// function: material facts and driver standing must be identical to the
// canonical result, and consumer truncation must not change the judgment.
func TestInvestigateQuestionPreservesJudgmentFromTheCanonicalResult(t *testing.T) {
	result := parityResult()
	boot := answerFixtureBootstrap(t, result, nil)

	response := callInvestigateQuestion(t, boot, contractsv1.MCPInvestigateQuestionRequest{Question: result.Question})
	projection := response.Structured

	if projection.DirectJudgment != result.DirectJudgment {
		t.Errorf("direct_judgment changed: %q != %q", projection.DirectJudgment, result.DirectJudgment)
	}
	if projection.CurrentState != result.CurrentState {
		t.Errorf("current_state changed")
	}
	if !reflect.DeepEqual(projection.CommittedSubjects, result.SubjectResolution.Committed) {
		t.Errorf("committed subjects differ from the canonical resolution")
	}
	canonicalStanding := map[string]contractsv1.ContextFabricDriverStanding{}
	for _, driver := range result.Drivers {
		canonicalStanding[driver.DriverID] = driver.Standing
	}
	for _, driver := range projection.PrincipalDrivers {
		if driver.Standing != canonicalStanding[driver.DriverID] {
			t.Errorf("driver %q standing changed: %q != %q", driver.DriverID, driver.Standing, canonicalStanding[driver.DriverID])
		}
	}
	// Evidence references must be a subset of what the canonical result
	// admitted -- an agent must never be handed a reference the
	// investigation did not stand behind.
	canonicalEvidence := map[string]struct{}{}
	for _, driver := range result.Drivers {
		for _, id := range driver.EvidenceRefIDs {
			canonicalEvidence[id] = struct{}{}
		}
	}
	if result.Cohort != nil {
		for _, member := range result.Cohort.Members {
			for _, id := range member.EvidenceRefIDs {
				canonicalEvidence[id] = struct{}{}
			}
		}
	}
	for _, id := range projection.EvidenceRefIDs {
		if _, ok := canonicalEvidence[id]; !ok {
			t.Errorf("projection invented evidence reference %q", id)
		}
	}
	// Limitations must survive: a shortened answer that dropped its own
	// caveats would read as more confident than the investigation was.
	if !reflect.DeepEqual(projection.Limitations, result.Limitations) {
		t.Errorf("limitations differ: %v != %v", projection.Limitations, result.Limitations)
	}
}

// TestInvestigateQuestionSendsTheMCPSurfaceAndCurrentAxis proves the
// sidecar owns consumer identity and the time axis rather than the caller.
func TestInvestigateQuestionSendsTheMCPSurfaceAndCurrentAxis(t *testing.T) {
	var seen contractsv1.ContextFabricInvestigationRequest
	boot := answerFixtureBootstrap(t, parityResult(), &seen)

	callInvestigateQuestion(t, boot, contractsv1.MCPInvestigateQuestionRequest{Question: "Which teams need attention?"})

	if seen.Consumer.Surface != "mcp" {
		t.Errorf("consumer surface = %q, want \"mcp\"", seen.Consumer.Surface)
	}
	if seen.TimeContext.Axis != contractsv1.ContextFabricTemporalCurrent {
		t.Errorf("time axis = %q, want current", seen.TimeContext.Axis)
	}
	if seen.SchemaVersion != contractsv1.ContextFabricInvestigationRequestSchema {
		t.Errorf("schema version = %q", seen.SchemaVersion)
	}
	if seen.RequestID == "" {
		t.Errorf("sidecar did not stamp a request id")
	}
}

// TestInvestigateQuestionAppliesAgentDefaults proves an omitted budget
// yields the smaller agent-appropriate defaults rather than the hosted
// maxima.
func TestInvestigateQuestionAppliesAgentDefaults(t *testing.T) {
	var seen contractsv1.ContextFabricInvestigationRequest
	boot := answerFixtureBootstrap(t, parityResult(), &seen)

	callInvestigateQuestion(t, boot, contractsv1.MCPInvestigateQuestionRequest{Question: "Which teams need attention?"})

	if seen.Options.MaxDrivers != defaultMaxDrivers {
		t.Errorf("max_drivers = %d, want the agent default %d", seen.Options.MaxDrivers, defaultMaxDrivers)
	}
	if seen.Options.MaxCohortMembers != defaultMaxCohortMembers {
		t.Errorf("max_cohort_members = %d, want %d", seen.Options.MaxCohortMembers, defaultMaxCohortMembers)
	}
	// CHAOS-4117: pins the calibrated candidate limit (10 -> 20) at the
	// ONE place a real, budget-omitting MCP caller actually gets it from --
	// hostedOptions' defaultMaxSubjectCandidates. Neither this test file
	// nor any other in this package asserted this field before; a revert
	// of the constant alone would have passed every existing test here.
	//
	// Deliberately a LITERAL 20, not defaultMaxSubjectCandidates itself
	// (codex xhigh review, round 2): comparing the observed value against
	// the SAME constant hostedOptions reads from proves internal
	// consistency, never the calibrated value -- a future revert of the
	// constant back to 10 would change both sides together and this
	// assertion would stay green. 20 is CalibratedTopK
	// (falkorgraph.RetrievalPolicy, retrieval_policy.go); a change here
	// must be a deliberate, reviewed recalibration, not a silent drift.
	const wantCalibratedMaxSubjectCandidates = 20
	if seen.Options.MaxSubjectCandidates != wantCalibratedMaxSubjectCandidates {
		t.Errorf("max_subject_candidates = %d, want the calibrated default %d", seen.Options.MaxSubjectCandidates, wantCalibratedMaxSubjectCandidates)
	}
	if !seen.Options.AllowClarification {
		t.Errorf("clarification must be allowed unless the caller opts out")
	}
	if seen.Options.IncludeDebug {
		t.Errorf("debug output must never be requested from an agent surface")
	}
}

// TestInvestigateQuestionRespectsExplicitClarificationOptOut covers the
// tri-state: an agent that cannot relay a follow-up question may prefer a
// best-effort answer.
func TestInvestigateQuestionRespectsExplicitClarificationOptOut(t *testing.T) {
	var seen contractsv1.ContextFabricInvestigationRequest
	boot := answerFixtureBootstrap(t, parityResult(), &seen)

	optOut := false
	callInvestigateQuestion(t, boot, contractsv1.MCPInvestigateQuestionRequest{
		Question:           "Which teams need attention?",
		AllowClarification: &optOut,
	})
	if seen.Options.AllowClarification {
		t.Errorf("explicit allow_clarification=false was ignored")
	}
}

// TestInvestigateQuestionMapsStructureAndWindowRequestFields (CHAOS-3972 P3
// + W2) proves the tool's own request fields map straight through to the
// hosted contract's fields of the same name, exactly as
// investigate_question.go's own construction comment describes -- the
// receipt arrays into their own PriorKindReceipts/.../PriorWindowReceipts
// fields, ExpectedKinds/SubjectHandles verbatim, EvidenceWindow into
// TimeContext.EvidenceWindow (legal only on this tool's fixed current
// axis), and WindowConfirmationMode into Options.
func TestInvestigateQuestionMapsStructureAndWindowRequestFields(t *testing.T) {
	var seen contractsv1.ContextFabricInvestigationRequest
	boot := answerFixtureBootstrap(t, parityResult(), &seen)

	kindReceipts := []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: "result_prior_00000001", ReceiptID: "kindr_" + strings.Repeat("a", 24)}}
	anchorReceipts := []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: "result_prior_00000001", ReceiptID: "ancr_" + strings.Repeat("b", 24)}}
	handleReceipts := []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: "result_prior_00000001", ReceiptID: "handr_" + strings.Repeat("c", 24)}}
	windowReceipts := []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: "result_prior_00000001", ReceiptID: "winr_" + strings.Repeat("d", 24)}}
	expectedKinds := []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectPullRequest}
	subjectHandles := []contractsv1.ContextFabricRequestedHandle{{Kind: contractsv1.ContextFabricSubjectPullRequest, PatternID: "pull_request_number", Value: "532"}}
	windowStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	callInvestigateQuestion(t, boot, contractsv1.MCPInvestigateQuestionRequest{
		Question:               "Which teams need attention?",
		PriorKindReceipts:      kindReceipts,
		PriorAnchorReceipts:    anchorReceipts,
		PriorHandleReceipts:    handleReceipts,
		PriorWindowReceipts:    windowReceipts,
		ExpectedKinds:          expectedKinds,
		SubjectHandles:         subjectHandles,
		EvidenceWindow:         &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &windowStart, End: &windowEnd},
		WindowConfirmationMode: contractsv1.ContextFabricWindowConfirmationNudge,
	})

	if !reflect.DeepEqual(seen.PriorKindReceipts, kindReceipts) {
		t.Errorf("prior_kind_receipts = %+v, want %+v", seen.PriorKindReceipts, kindReceipts)
	}
	if !reflect.DeepEqual(seen.PriorAnchorReceipts, anchorReceipts) {
		t.Errorf("prior_anchor_receipts = %+v, want %+v", seen.PriorAnchorReceipts, anchorReceipts)
	}
	if !reflect.DeepEqual(seen.PriorHandleReceipts, handleReceipts) {
		t.Errorf("prior_handle_receipts = %+v, want %+v", seen.PriorHandleReceipts, handleReceipts)
	}
	if !reflect.DeepEqual(seen.PriorWindowReceipts, windowReceipts) {
		t.Errorf("prior_window_receipts = %+v, want %+v", seen.PriorWindowReceipts, windowReceipts)
	}
	if !reflect.DeepEqual(seen.ExpectedKinds, expectedKinds) {
		t.Errorf("expected_kinds = %+v, want %+v", seen.ExpectedKinds, expectedKinds)
	}
	if !reflect.DeepEqual(seen.SubjectHandles, subjectHandles) {
		t.Errorf("subject_handles = %+v, want %+v", seen.SubjectHandles, subjectHandles)
	}
	if seen.TimeContext.EvidenceWindow == nil || !seen.TimeContext.EvidenceWindow.Start.Equal(windowStart) || !seen.TimeContext.EvidenceWindow.End.Equal(windowEnd) {
		t.Errorf("time_context.evidence_window = %+v, want start=%v end=%v", seen.TimeContext.EvidenceWindow, windowStart, windowEnd)
	}
	if seen.Options.WindowConfirmationMode != contractsv1.ContextFabricWindowConfirmationNudge {
		t.Errorf("options.window_confirmation_mode = %q, want %q", seen.Options.WindowConfirmationMode, contractsv1.ContextFabricWindowConfirmationNudge)
	}
}

// TestInvestigateQuestionProjectsStructureAndWindowDisclosure (CHAOS-3972
// P3+W2) proves the response's projection carries structure_needs/
// confirmed_structure/effective_evidence_window/window_clarification
// verbatim from the canonical result, through the SAME answerprojection.Project
// function the hosted API uses -- the P3 disclosure surface this whole
// ticket exists to reach.
func TestInvestigateQuestionProjectsStructureAndWindowDisclosure(t *testing.T) {
	result := parityResult()
	result.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{
		Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
		KindOptions: []contractsv1.ContextFabricKindOption{{
			ReceiptID: "kindr_" + strings.Repeat("a", 24), OptionID: "opt_kind1", Label: "a pull request",
			Kind: contractsv1.ContextFabricSubjectPullRequest, OfferSource: contractsv1.ContextFabricStructureOfferEngine,
		}},
	}
	result.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{{
		Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedValue: "532",
		Source: contractsv1.ContextFabricStructureSourceExplicitUnattributed, Provenance: contractsv1.ContextFabricStructureInferredDefault,
		Disposition: contractsv1.ContextFabricStructureDispositionApplied,
	}}
	result.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{
		RelativeID: contractsv1.ContextFabricRelativeWindowTrailing90D, Provenance: contractsv1.ContextFabricWindowInferredDefault,
	}
	windowOptionStart, windowOptionEnd := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC), time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	result.WindowClarification = &contractsv1.ContextFabricWindowClarification{
		Options: []contractsv1.ContextFabricWindowOption{{
			ReceiptID: "winr_" + strings.Repeat("d", 24), OptionID: "opt_window1", Label: "the last 90 days",
			RelativeID: contractsv1.ContextFabricRelativeWindowTrailing90D, Start: &windowOptionStart, End: &windowOptionEnd,
		}},
	}

	var seen contractsv1.ContextFabricInvestigationRequest
	boot := answerFixtureBootstrap(t, result, &seen)
	response := callInvestigateQuestion(t, boot, contractsv1.MCPInvestigateQuestionRequest{Question: "What is PR 532?"})

	if response.Structured.StructureNeeds == nil || len(response.Structured.StructureNeeds.KindOptions) != 1 {
		t.Fatalf("structured.structure_needs did not carry the result's kind options: %+v", response.Structured.StructureNeeds)
	}
	if len(response.Structured.ConfirmedStructure) != 1 || response.Structured.ConfirmedStructure[0].AppliedValue != "532" {
		t.Fatalf("structured.confirmed_structure did not carry the result's entry: %+v", response.Structured.ConfirmedStructure)
	}
	if response.Structured.EffectiveEvidenceWindow == nil || response.Structured.EffectiveEvidenceWindow.RelativeID != contractsv1.ContextFabricRelativeWindowTrailing90D {
		t.Fatalf("structured.effective_evidence_window did not carry the result's window: %+v", response.Structured.EffectiveEvidenceWindow)
	}
	if response.Structured.WindowClarification == nil || len(response.Structured.WindowClarification.Options) != 1 {
		t.Fatalf("structured.window_clarification did not carry the result's options: %+v", response.Structured.WindowClarification)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("response failed contract validation: %v", err)
	}
}

// TestIncludeFullResultDropsWholeResultWhenOverBudget is the Directive 1
// probe: the byte budget bounds the TOTAL content, and an over-budget full
// result is dropped whole with the drop declared, never truncated into a
// partial document.
func TestIncludeFullResultDropsWholeResultWhenOverBudget(t *testing.T) {
	result := parityResult()
	boot := answerFixtureBootstrap(t, result, nil)

	// A budget far too small for the canonical result to fit beside the
	// projection, but still a legal contract value.
	tight := contractsv1.MCPInvestigationBudget{MaxSerializedBytes: 8192}
	response := callInvestigateQuestion(t, boot, contractsv1.MCPInvestigateQuestionRequest{
		Question:          result.Question,
		IncludeFullResult: true,
		Budget:            &tight,
	})

	if response.FullResult != nil {
		t.Fatalf("full result was included despite exceeding the byte budget")
	}
	if !response.Structured.ProjectionBudget.FullResultOmitted {
		t.Errorf("the dropped full result was not declared")
	}
	if !response.Structured.ProjectionBudget.Truncated {
		t.Errorf("truncated must be set when the full result was dropped")
	}
	// The projection itself stays complete and valid -- that is the point
	// of failing this way.
	if err := response.Structured.Validate(); err != nil {
		t.Errorf("projection is invalid after dropping the full result: %v", err)
	}
	if response.Structured.DirectJudgment != result.DirectJudgment {
		t.Errorf("dropping the full result changed the answer")
	}
}

// TestIncludeFullResultAttachesWhenItFits covers the other half: with room
// in the budget the caller gets both views, and they describe the same
// investigation.
func TestIncludeFullResultAttachesWhenItFits(t *testing.T) {
	result := parityResult()
	boot := answerFixtureBootstrap(t, result, nil)

	generous := contractsv1.MCPInvestigationBudget{MaxSerializedBytes: 1048576}
	response := callInvestigateQuestion(t, boot, contractsv1.MCPInvestigateQuestionRequest{
		Question:          result.Question,
		IncludeFullResult: true,
		Budget:            &generous,
	})

	if response.FullResult == nil {
		t.Fatalf("full result was omitted despite fitting the byte budget")
	}
	if response.FullResult.ResultID != response.Structured.ResultID {
		t.Errorf("full result and projection describe different investigations")
	}
	if response.Structured.ProjectionBudget.FullResultOmitted {
		t.Errorf("full result is present but declared omitted")
	}
	if err := response.Validate(); err != nil {
		t.Errorf("response failed contract validation: %v", err)
	}
}

// TestInvestigationResultReturnsTheCanonicalResultWhole proves the
// deep-inspection tool narrows nothing.
func TestInvestigationResultReturnsTheCanonicalResultWhole(t *testing.T) {
	result := parityResult()
	boot := answerFixtureBootstrap(t, result, nil)

	args, err := json.Marshal(contractsv1.MCPInvestigationResultRequest{ResultID: result.ResultID})
	if err != nil {
		t.Fatal(err)
	}
	toolResult, err := handleInvestigationResult(context.Background(), boot, &mcpsdk.CallToolRequest{
		Params: &mcpsdk.CallToolParamsRaw{Arguments: args},
	})
	if err != nil {
		t.Fatalf("protocol error: %v", err)
	}
	if toolResult.IsError {
		t.Fatalf("tool reported an error: %s", toolResultText(toolResult))
	}
	var response contractsv1.MCPInvestigationResultResponse
	if err := json.Unmarshal(toolResult.StructuredContent.(json.RawMessage), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Structured.Drivers) != len(result.Drivers) {
		t.Errorf("drivers = %d, want the full %d: this tool must not narrow", len(response.Structured.Drivers), len(result.Drivers))
	}
	if len(response.Structured.ClaimedFacts) != len(result.ClaimedFacts) {
		t.Errorf("claimed facts were narrowed")
	}
	if response.Structured.ResultID != result.ResultID {
		t.Errorf("wrong result returned")
	}
}

// TestInvestigationResultRejectsMalformedID keeps a bad handle from ever
// reaching the network.
func TestInvestigationResultRejectsMalformedID(t *testing.T) {
	boot := answerFixtureBootstrap(t, parityResult(), nil)

	args, err := json.Marshal(contractsv1.MCPInvestigationResultRequest{ResultID: "short"})
	if err != nil {
		t.Fatal(err)
	}
	toolResult, err := handleInvestigationResult(context.Background(), boot, &mcpsdk.CallToolRequest{
		Params: &mcpsdk.CallToolParamsRaw{Arguments: args},
	})
	if err != nil {
		t.Fatalf("protocol error: %v", err)
	}
	if !toolResult.IsError {
		t.Errorf("a malformed result_id was accepted")
	}
}

// TestAnswerToolsAppearOnlyWhenHostedAdvertisesThem pins the
// advertise-gated registration. Context Fabric is an optional hosted
// capability, so a deployment without it must not offer an agent tools
// that every call would fail.
func TestAnswerToolsAppearOnlyWhenHostedAdvertisesThem(t *testing.T) {
	withAnswers := answerFixtureBootstrap(t, parityResult(), nil)
	client, closeFn := connectedClient(t, withAnswers)
	defer closeFn()
	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	if !names[toolInvestigateQuestion] || !names[toolInvestigationResult] {
		t.Errorf("answer tools missing when hosted advertises them: %v", names)
	}

	fx := newFixtureServer(t)
	withoutAnswers := newFixtureBootstrap(t, fx)
	plainClient, plainClose := connectedClient(t, withoutAnswers)
	defer plainClose()
	plainListed, err := plainClient.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range plainListed.Tools {
		if tool.Name == toolInvestigateQuestion || tool.Name == toolInvestigationResult {
			t.Errorf("answer tool %q was offered without hosted support", tool.Name)
		}
	}
}

// TestStructuredPayloadsCarryTheUntrustedDeclaration is the codex round-1
// F3 regression.
//
// RenderedMarkdown was always marked untrusted, but Structured and
// FullResult carried model- and source-derived text with no machine-readable
// signal at all -- so a consumer reading the structured payload, which is
// the whole point of a structured payload, got nothing. The declaration
// must be in the PAYLOAD, not only in the documentation.
func TestStructuredPayloadsCarryTheUntrustedDeclaration(t *testing.T) {
	result := parityResult()
	boot := answerFixtureBootstrap(t, result, nil)

	generous := contractsv1.MCPInvestigationBudget{MaxSerializedBytes: 1048576}
	answer := callInvestigateQuestion(t, boot, contractsv1.MCPInvestigateQuestionRequest{
		Question:          result.Question,
		IncludeFullResult: true,
		Budget:            &generous,
	})

	if !answer.UntrustedContent.Untrusted {
		t.Error("investigate_question structured payload did not declare itself untrusted")
	}
	if answer.UntrustedContent.Notice == "" {
		t.Error("untrusted declaration carries no notice")
	}
	if !reflect.DeepEqual(answer.UntrustedContent.Fields, contractsv1.MCPInvestigateQuestionUntrustedFields) {
		t.Errorf("declared fields = %v, want the contract list", answer.UntrustedContent.Fields)
	}

	// The declaration must cover the fields that are actually populated.
	// A list that omitted a live field would be worse than no list: a
	// consumer would read it as an exhaustive safe/unsafe partition.
	declared := map[string]bool{}
	for _, field := range answer.UntrustedContent.Fields {
		declared[field] = true
	}
	if answer.Structured.DirectJudgment != "" && !declared["structured.direct_judgment"] {
		t.Error("direct_judgment is populated but not declared untrusted")
	}
	if len(answer.Structured.PrincipalDrivers) > 0 && !declared["structured.principal_drivers[].summary"] {
		t.Error("driver summaries are populated but not declared untrusted")
	}
	if answer.FullResult != nil && !declared["full_result"] {
		t.Error("full_result is present but not declared untrusted")
	}

	// The deep-inspection tool carries the same signal for the canonical
	// document, which contains strictly more untrusted text.
	args, err := json.Marshal(contractsv1.MCPInvestigationResultRequest{ResultID: result.ResultID})
	if err != nil {
		t.Fatal(err)
	}
	toolResult, err := handleInvestigationResult(context.Background(), boot, &mcpsdk.CallToolRequest{
		Params: &mcpsdk.CallToolParamsRaw{Arguments: args},
	})
	if err != nil || toolResult.IsError {
		t.Fatalf("investigation_result failed: %v", err)
	}
	var full contractsv1.MCPInvestigationResultResponse
	if err := json.Unmarshal(toolResult.StructuredContent.(json.RawMessage), &full); err != nil {
		t.Fatal(err)
	}
	if !full.UntrustedContent.Untrusted || len(full.UntrustedContent.Fields) == 0 {
		t.Error("investigation_result structured payload did not declare itself untrusted")
	}
}

// TestUntrustedDeclarationCannotBeWeakened proves the declaration is
// validated exactly, not merely for presence: a response that shortened the
// field list would let a consumer treat model-derived text as safe.
func TestUntrustedDeclarationCannotBeWeakened(t *testing.T) {
	result := parityResult()
	boot := answerFixtureBootstrap(t, result, nil)
	answer := callInvestigateQuestion(t, boot, contractsv1.MCPInvestigateQuestionRequest{Question: result.Question})

	weakened := answer
	weakened.UntrustedContent.Fields = answer.UntrustedContent.Fields[:2]
	if err := weakened.Validate(); err == nil {
		t.Error("a shortened untrusted field list was accepted")
	}

	denied := answer
	denied.UntrustedContent.Untrusted = false
	if err := denied.Validate(); err == nil {
		t.Error("a payload denying it is untrusted was accepted")
	}
}

// TestMCPWrappersAreClosedOverCanonicalMaxima is the codex round-3 P2-4
// regression.
//
// The closure proof in internal/contextfabric/answerprojection exercises the
// projection VALIDATOR. That is necessary but not sufficient: what a client
// actually receives is the MCP wrapper, serialized, and a wrapper can fail
// where the inner value passes -- an envelope bound, a required member left
// nil, or emitted JSON that no longer matches the published tool schema.
//
// This drives a canonical result at its maximum legal size through BOTH real
// tool handlers and validates the EMITTED JSON against the tool's own
// schema, which is the document a client validates against.
func TestMCPWrappersAreClosedOverCanonicalMaxima(t *testing.T) {
	result := canonicalMaximumInvestigationResult(t)
	if err := result.Validate(); err != nil {
		t.Fatalf("the canonical-maximum fixture is not a valid result, so it proves nothing: %v", err)
	}
	boot := answerFixtureBootstrap(t, result, nil)

	t.Run("investigate_question", func(t *testing.T) {
		response := callInvestigateQuestion(t, boot, contractsv1.MCPInvestigateQuestionRequest{Question: result.Question})
		if err := response.Validate(); err != nil {
			t.Fatalf("wrapper rejected a maximum-size answer: %v", err)
		}
		assertMatchesToolSchema(t, response, investigateQuestionResponseSchemaFile)
	})

	t.Run("investigation_result", func(t *testing.T) {
		args, err := json.Marshal(contractsv1.MCPInvestigationResultRequest{ResultID: result.ResultID})
		if err != nil {
			t.Fatal(err)
		}
		toolResult, err := handleInvestigationResult(context.Background(), boot, &mcpsdk.CallToolRequest{
			Params: &mcpsdk.CallToolParamsRaw{Arguments: args},
		})
		if err != nil {
			t.Fatalf("protocol error: %v", err)
		}
		if toolResult.IsError {
			t.Fatalf("tool reported an error on a maximum-size result: %s", toolResultText(toolResult))
		}
		var response contractsv1.MCPInvestigationResultResponse
		if err := json.Unmarshal(toolResult.StructuredContent.(json.RawMessage), &response); err != nil {
			t.Fatal(err)
		}
		if err := response.Validate(); err != nil {
			t.Fatalf("wrapper rejected a maximum-size result: %v", err)
		}
		assertMatchesToolSchema(t, response, investigationResultResponseSchemaFile)
	})
}

// assertMatchesToolSchema validates an emitted response against the tool
// schema the sidecar publishes, which is what a client checks it against.
// Go-side validation agreeing is not the same as the wire document being
// schema-valid.
func assertMatchesToolSchema(t *testing.T, response any, schemaFile string) {
	t.Helper()
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	name := strings.TrimPrefix(schemaFile, "schemas/")
	if err := contractcheck.ValidateSerialized("", name, encoded); err != nil {
		t.Fatalf("emitted JSON violates the published tool schema %s: %v", name, err)
	}
}

// canonicalMaximumInvestigationResult builds a valid canonical result that
// sits at the contract maximum for the fields the answer surface carries,
// so the wrapper closure proof exercises the largest legal payload rather
// than a comfortable one.
func canonicalMaximumInvestigationResult(t *testing.T) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	result := parityResult()
	project := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}

	result.Question = strings.Repeat("q", 8000)
	result.DirectJudgment = strings.Repeat("j", 4000)
	result.CurrentState = strings.Repeat("c", 4000)
	result.DeterministicAnswer = strings.Repeat("d", 12000)
	result.StrongestPressures = maximalStrings(50, "pressure", 2000)
	result.Limitations = maximalStrings(100, "limitation", 2000)
	result.Warnings = maximalStrings(100, "warning", 2000)

	value := "amber"
	result.Drivers = nil
	result.ClaimedFacts = nil
	for i := 0; i < 50; i++ {
		claimID := "claim_max_" + strconv.Itoa(1000+i)
		result.ClaimedFacts = append(result.ClaimedFacts, contractsv1.ContextFabricClaimedFact{
			ClaimID: claimID, Kind: contractsv1.ContextFabricFactStatus, Subject: project,
			Field: strings.Repeat("f", 128), Value: contractsv1.ContextFabricScalarValue{String: &value},
		})
		result.Drivers = append(result.Drivers, contractsv1.ContextFabricDriverJudgment{
			DriverID: "driver_max_" + strconv.Itoa(1000+i),
			Standing: contractsv1.ContextFabricDriverPrincipal, Category: "status",
			Title: strings.Repeat("t", 512), Summary: strings.Repeat("s", 4000),
			Qualification:    strings.Repeat("u", 2000),
			AffectedSubjects: []contractsv1.ContextFabricSubjectRef{project},
			EvidenceRefIDs:   []string{"evidence_max_" + strconv.Itoa(1000+i)},
			ClaimedFactIDs:   []string{claimID},
			Derivation:       contractsv1.ContextFabricDerivationCanonicalStructured,
			EpistemicStatus:  contractsv1.ContextFabricEpistemicObserved,
			Confidence:       1, Current: true,
		})
	}

	// 60 rather than the contract's 250: 250 members at the maximum
	// inclusion-reason size alone exceeds the sidecar's 8 MiB ceiling, so
	// a larger cohort would test the transport limit rather than the
	// wrapper.
	members := make([]contractsv1.ContextFabricCohortMember, 0, 60)
	for i := 0; i < 60; i++ {
		members = append(members, contractsv1.ContextFabricCohortMember{
			Subject:          contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team_max_" + strconv.Itoa(i), Label: "Team " + strconv.Itoa(i)},
			Rank:             i + 1,
			InclusionReasons: maximalStrings(32, "reason-"+strconv.Itoa(i), 1000),
			EvidenceRefIDs:   []string{"evidence_cohort_" + strconv.Itoa(1000+i)},
		})
	}
	result.Cohort = &contractsv1.ContextFabricCohort{
		Kind: contractsv1.ContextFabricSubjectTeam, Members: members,
		Rationale: strings.Repeat("r", 4000), Complete: true,
	}

	sources := make([]contractsv1.ContextFabricSourceObservation, 0, 100)
	for i := 0; i < 100; i++ {
		sources = append(sources, contractsv1.ContextFabricSourceObservation{
			Source: "source_" + strconv.Itoa(1000+i),
			State:  contractsv1.ContextFabricSourceUnavailable,
			Reason: strings.Repeat("e", 2000),
		})
	}
	result.Coverage = contractsv1.ContextFabricCoverage{
		Sources: sources, Partial: true, DegradedReasons: maximalStrings(100, "degraded", 2000),
	}
	return result
}

func maximalStrings(count int, prefix string, length int) []string {
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		head := prefix + "-" + strconv.Itoa(i) + "-"
		if len(head) >= length {
			out = append(out, head[:length])
			continue
		}
		out = append(out, head+strings.Repeat("x", length-len(head)))
	}
	return out
}

// TestMatchReasonsAreReachableThroughTheFullResult pins the FACT half of a
// render exemption (CHAOS-3746 round 8).
//
// The answer view omits candidate match reasons, justified by "reachable
// through the full result". That reason asserts a code fact, and the last
// exemption whose reason asserted a code fact turned out to be false --
// drivers had stopped carrying subjects, so "subjects appear through the
// drivers" was wrong for two rounds. A reason that makes a checkable claim
// gets the claim checked.
//
// Only the fact is pinned. Whether that reachability JUSTIFIES omitting
// them from the bounded view stays a review judgment: pinning a conclusion
// would dress judgment as proof.
func TestMatchReasonsAreReachableThroughTheFullResult(t *testing.T) {
	// A DISTINCTIVE reason, not merely a non-empty one (codex round-9 F5).
	// Asserting "some candidate carries some reason" passed even if the
	// surfaced text were a placeholder, a different candidate's reason, or
	// any other substitute -- so it never actually proved that THIS reason
	// is reachable, which is the claim the exemption makes.
	const canonicalReason = "chaos3746-round9-match-reason-sentinel: matched on the former team name"

	result := parityResult()
	result.Status = contractsv1.ContextFabricInvestigationClarificationRequired
	result.SubjectResolution.ClarificationPrompt = "Which team did you mean?"
	result.SubjectResolution.Candidates[0].MatchReasons = []string{canonicalReason}
	wantReceiptID := result.SubjectResolution.Candidates[0].ReceiptID
	boot := answerFixtureBootstrap(t, result, nil)

	args, err := json.Marshal(contractsv1.MCPInvestigationResultRequest{ResultID: result.ResultID})
	if err != nil {
		t.Fatal(err)
	}
	toolResult, err := handleInvestigationResult(context.Background(), boot, &mcpsdk.CallToolRequest{
		Params: &mcpsdk.CallToolParamsRaw{Arguments: args},
	})
	if err != nil || toolResult.IsError {
		t.Fatalf("investigation_result failed: %v", err)
	}
	var response contractsv1.MCPInvestigationResultResponse
	if err := json.Unmarshal(toolResult.StructuredContent.(json.RawMessage), &response); err != nil {
		t.Fatal(err)
	}
	// The exemption's claim is that THIS candidate's own reason text arrives
	// verbatim, so that is what gets checked: the right candidate, carrying
	// the exact string that was stored. Anything weaker (any candidate, any
	// non-empty reason) also passes when the text is replaced or when
	// another candidate's reasons are what surfaced.
	for _, candidate := range response.Structured.SubjectResolution.Candidates {
		if candidate.ReceiptID != wantReceiptID {
			continue
		}
		for _, reason := range candidate.MatchReasons {
			if reason == canonicalReason {
				return // reachable verbatim, as the exemption claims
			}
		}
		t.Fatalf("candidate %q surfaced match reasons %q, which do not include the stored reason %q; the render exemption's stated reason is false",
			wantReceiptID, candidate.MatchReasons, canonicalReason)
	}
	t.Fatalf("candidate %q did not surface at all through the full result, so its match reasons are not reachable", wantReceiptID)
}
