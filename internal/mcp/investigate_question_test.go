package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

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
