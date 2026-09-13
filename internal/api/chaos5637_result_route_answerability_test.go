package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// THE READ SIDE of the CHAOS-5637 answerability invariant, driven through
// the real handler.
//
// The serving guard sits at the engine's finalizeServed, which this route
// does not go through: it reads a stored row, admits it under the
// deliberately lenient stored-read validator, and serves it -- and the MCP
// investigation_result tool forwards that same canonical response. So a row
// composed before the invariant existed went on being served as
// clarification_required with nothing on any offer channel, which is the
// exact 76-turn shape the ticket exists to end. Reproduced by adversarial
// review through this handler at 200 before the repair landed.
//
// The fixture is seeded through the STORE, not handed to the handler, and
// it is asserted to pass ValidateStored first -- so this is a legitimate
// legacy row, never a malformed-response artefact.
func seedLegacyUnanswerableClarification(t *testing.T, store *memoryinvestigation.Store, resultID string) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	result := legacyUnanswerableClarificationRow(t, resultID)
	if err := store.Save(context.Background(), storage.Principal{OrgID: callerOrgID}, result,
		contextfabric.SourceWatermarkSnapshot{}, nil,
		contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
		contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{},
		contextfabric.ReuseVersionAuthorities{}, 0, "",
		// A seeded row's turn ended before interpretation, so it carries the
		// closed absence rather than a reading -- the same shape every other
		// route fixture in this package seeds.
		contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)); err != nil {
		t.Fatalf("seed result: %v", err)
	}
	return result
}

// legacyUnanswerableClarificationRow is a stored clarification that offers
// nothing on any channel, valid under the stored-read validator.
func legacyUnanswerableClarificationRow(t *testing.T, resultID string) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	result := validContextFabricInvestigationResult()
	result.ResultID = resultID
	result.Status = contractsv1.ContextFabricInvestigationClarificationRequired
	result.SubjectResolution = contractsv1.ContextFabricSubjectResolution{
		Candidates:          []contractsv1.ContextFabricSubjectCandidate{},
		Committed:           []contractsv1.ContextFabricSubjectRef{},
		ClarificationPrompt: contextfabric.OfferPoolEmptiedClarificationPrompt,
	}
	result.StructureNeeds = nil
	result.WindowClarification = nil
	result.DirectJudgment = ""
	result.Drivers = []contractsv1.ContextFabricDriverJudgment{}
	result.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{}
	result.EvidenceRefIDs = []string{}
	result.Completeness.TerminalStatus = contractsv1.ContextFabricInvestigationClarificationRequired
	result.Completeness.TerminalReason = contractsv1.ContextFabricTerminalReasonClarificationDisclosed
	result.Completeness.ClaimedFactsCount = 0
	result.Completeness.RowsCount = 0
	if err := contractsv1.ValidateStoredResult(result); err != nil {
		t.Fatalf("fixture is not a valid legacy stored row, so this test would prove nothing about real rows: %v", err)
	}
	return result
}

func TestResultRouteNeverServesAnUnanswerableClarification(t *testing.T) {
	store := memoryinvestigation.NewStore()
	seeded := seedLegacyUnanswerableClarification(t, store, "result_legacy_unans1")
	app, token, logs := newContextFabricTestAppWithResultsAndLogs(t, nil, store)

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, seeded.ResultID))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- the row is real and was asked for by id; the repair must not deny it (body %s)",
			recorder.Code, recorder.Body.String())
	}
	var got contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Status == contractsv1.ContextFabricInvestigationClarificationRequired {
		t.Fatalf("the route served an unanswerable clarification: candidates=%d structure_needs=%v window_clarification=%v",
			len(got.SubjectResolution.Candidates), got.StructureNeeds != nil, got.WindowClarification != nil)
	}
	if got.Status != contractsv1.ContextFabricInvestigationNoMatch {
		t.Fatalf("status = %q, want %q", got.Status, contractsv1.ContextFabricInvestigationNoMatch)
	}
	if got.Completeness.TerminalStatus != contractsv1.ContextFabricInvestigationNoMatch {
		t.Fatalf("completeness.terminal_status = %q, want it recomputed from the repaired status",
			got.Completeness.TerminalStatus)
	}
	// The prompt survives: it is what distinguishes a withheld pool from an
	// empty graph, and the repair corrects the label, not the evidence.
	if got.SubjectResolution.ClarificationPrompt == "" {
		t.Error("the repair cleared the prompt on the served row")
	}
	// The row has no reading, so no basis can be taken: the served copy is
	// exactly the offer-less repair's, with no basis and no basis sentence.
	want := seeded
	want.Limitations = append([]string(nil), seeded.Limitations...)
	if !contextfabric.RepairLegacyUnanswerableClarification(&want) {
		t.Fatal("fixture defect: the offer-less repair does not apply to the seeded row")
	}
	if got.RefusalBasis != "" || got.DeterministicAnswer != want.DeterministicAnswer || strings.Join(got.Limitations, "|") != strings.Join(want.Limitations, "|") {
		t.Fatalf("served basis/answer/limitations = %q/%q/%#v, want no basis and the offer-less repair's %q/%#v", got.RefusalBasis, got.DeterministicAnswer, got.Limitations, want.DeterministicAnswer, want.Limitations)
	}
	for _, sentence := range []string{contractsv1.ContextFabricDeclaredKindUnmatchedLimitation, contractsv1.ContextFabricOrganizationScopeUnsupportedLimitation} {
		for _, limitation := range got.Limitations {
			if limitation == sentence {
				t.Fatalf("served limitations %#v carry a basis sentence on a row repaired with no basis", got.Limitations)
			}
		}
	}
	lines := storedAnswerabilityLines(t, logs, 0)
	if len(lines) != 1 {
		t.Fatalf("stored-answerability lines = %d, want 1 for the read; log: %s", len(lines), logs.String())
	}
	if line := lines[0]; line["determination"] != "unanswerable" || line["decided_by"] != "offer_less" || line["repaired"] != true ||
		line["semantic_state"] != "not_read" || line["stored_status"] != "clarification_required" || line["served_status"] != "no_match" {
		t.Fatalf("stored-answerability line = %v, want unanswerable/offer_less, repaired, clarification_required -> no_match", line)
	}
	if count := strings.Count(logs.String(), "context fabric legacy unanswerable clarification repaired"); count != 1 {
		t.Fatalf("legacy repair lines = %d, want 1: the offer-less arm's sunset count must keep its own line", count)
	}
}

// THE CONTROL. A stored clarification that DOES offer something must be
// served exactly as it is. Without this arm, a repair that rewrote every
// cached clarification into a terminal would stay green.
func TestResultRouteStillServesAnAnswerableClarification(t *testing.T) {
	store := memoryinvestigation.NewStore()
	result := validContextFabricInvestigationResult()
	result.ResultID = "result_legacy_answer1"
	result.Status = contractsv1.ContextFabricInvestigationClarificationRequired
	result.SubjectResolution = contractsv1.ContextFabricSubjectResolution{
		Candidates:          []contractsv1.ContextFabricSubjectCandidate{},
		Committed:           []contractsv1.ContextFabricSubjectRef{},
		ClarificationPrompt: contextfabric.OfferPoolEmptiedClarificationPrompt,
	}
	result.WindowClarification = &contractsv1.ContextFabricWindowClarification{
		Options: []contractsv1.ContextFabricWindowOption{{
			Label: "all time", RelativeID: contractsv1.ContextFabricRelativeWindowAllTime,
			ReceiptID: "winr_answerablecontrol01", OptionID: "wino_answerablecontrol01",
		}},
	}
	result.DirectJudgment = ""
	result.Drivers = []contractsv1.ContextFabricDriverJudgment{}
	result.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{}
	result.EvidenceRefIDs = []string{}
	result.Completeness.TerminalStatus = contractsv1.ContextFabricInvestigationClarificationRequired
	result.Completeness.TerminalReason = contractsv1.ContextFabricTerminalReasonClarificationDisclosed
	result.Completeness.ClaimedFactsCount = 0
	result.Completeness.RowsCount = 0
	if err := contractsv1.ValidateStoredResult(result); err != nil {
		t.Fatalf("fixture is not a valid stored row: %v", err)
	}
	if err := store.Save(context.Background(), storage.Principal{OrgID: callerOrgID}, result,
		contextfabric.SourceWatermarkSnapshot{}, nil,
		contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
		contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{},
		contextfabric.ReuseVersionAuthorities{}, 0, "",
		// A seeded row's turn ended before interpretation, so it carries the
		// closed absence rather than a reading -- the same shape every other
		// route fixture in this package seeds.
		contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)); err != nil {
		t.Fatalf("seed result: %v", err)
	}
	app, token := newContextFabricTestAppWithResults(t, nil, store)

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, result.ResultID))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var got contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Status != contractsv1.ContextFabricInvestigationClarificationRequired {
		t.Fatalf("status = %q, want the clarification served unchanged -- it carries a window offer the caller can redeem", got.Status)
	}
}
