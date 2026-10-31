package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12286: a person is never shown ACR's own status or completeness word
// (chris 2026-10-31). The composer used to open the lead prose with "This
// investigation is complete." (or partial, or degraded) and close current_state
// with " Not complete: …"; answers stored with that wording are served with
// the plain wording on every read, and the stored document is not rewritten.

const (
	legacyCompleteSentence = "This investigation is complete."
	legacyPartialSentence  = "This investigation is partial: some canonical or graph coverage was unavailable."
	legacyDegradedSentence = "This investigation is degraded: coverage was limited."
)

// acrTemplatePhrases are ACR's own status and completeness wording. A
// provider's fact value ("status: Complete") is evidence, not one of these.
var acrTemplatePhrases = []string{"This investigation is", "Not complete:"}

func requireNoTemplatePhrase(t *testing.T, field, text string) {
	t.Helper()
	for _, phrase := range acrTemplatePhrases {
		if strings.Contains(text, phrase) {
			t.Fatalf("served %s = %q carries ACR's own status wording %q", field, text, phrase)
		}
	}
}

// storedLeadProseAnswer is a stored answer of status whose lead prose is head
// (both fields) and whose current_state is currentState.
func storedLeadProseAnswer(status contractsv1.ContextFabricInvestigationStatus, head, currentState string) contractsv1.ContextFabricInvestigationResult {
	result := validContextFabricInvestigationResult()
	result.ResultID = "result_status_sentence01"
	result.Status = status
	result.DirectJudgment = head
	result.DeterministicAnswer = head
	result.CurrentState = currentState
	if status != contractsv1.ContextFabricInvestigationComplete {
		result.Coverage.Partial = true
		result.Coverage.DegradedReasons = []string{"graph_unavailable"}
	}
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	return result
}

// readLeadProseByID serves stored through the real by-id route and returns
// the served document and the route's log lines.
func readLeadProseByID(t *testing.T, stored contractsv1.ContextFabricInvestigationResult) (contractsv1.ContextFabricInvestigationResult, string) {
	t.Helper()
	if err := stored.Validate(); err != nil {
		t.Fatalf("fixture: the stored answer is invalid: %v", err)
	}
	app, token, logs := newCompletenessAuthorityTestApp(t, legacyResultStore{result: stored}, false)
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, stored.ResultID))
	if recorder.Code != 200 {
		t.Fatalf("status = %d (body %s)", recorder.Code, recorder.Body.String())
	}
	var got contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("served result invalid: %v", err)
	}
	if got.Status != stored.Status {
		t.Fatalf("served status = %q, want the stored %q: this change moves prose only", got.Status, stored.Status)
	}
	return got, logs.String()
}

func TestByIDAStoredLegacyStatusSentenceIsServedInPlainWords(t *testing.T) {
	const counted = "Counted 1 work item."
	const observed = "Current observed values: open items: 3."
	cases := map[string]struct {
		status contractsv1.ContextFabricInvestigationStatus
		head   string
		want   string
	}{
		// A complete answer has no status sentence; with nothing else in
		// the head, the head is current_state's first sentence.
		"complete, the sentence alone": {contractsv1.ContextFabricInvestigationComplete, legacyCompleteSentence, observed},
		"complete, after the count":    {contractsv1.ContextFabricInvestigationComplete, counted + " " + legacyCompleteSentence, counted},
		"partial":                      {contractsv1.ContextFabricInvestigationPartial, counted + " " + legacyPartialSentence, counted + " Some canonical or graph coverage was unavailable."},
		"degraded":                     {contractsv1.ContextFabricInvestigationDegraded, legacyDegradedSentence + " " + counted, "Coverage was limited. " + counted},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, _ := readLeadProseByID(t, storedLeadProseAnswer(tc.status, tc.head, observed))
			if got.DirectJudgment != tc.want || got.DeterministicAnswer != tc.want {
				t.Fatalf("served lead prose = %q / %q, want %q", got.DirectJudgment, got.DeterministicAnswer, tc.want)
			}
			requireNoTemplatePhrase(t, "direct_judgment", got.DirectJudgment)
			requireNoTemplatePhrase(t, "deterministic_answer", got.DeterministicAnswer)
		})
	}
}

// The status does not change on this read, and the old wording still goes.
func TestByIDAnUnchangedStoredCompleteAnswerLosesItsStatusSentence(t *testing.T) {
	got, logs := readLeadProseByID(t, storedLeadProseAnswer(contractsv1.ContextFabricInvestigationComplete, legacyCompleteSentence, "Current observed values: open items: 3."))
	requireNoTemplatePhrase(t, "direct_judgment", got.DirectJudgment)
	if !strings.Contains(logs, `"lead_prose_served_as_stored":false`) {
		t.Fatalf("the completeness authority line must state lead_prose_served_as_stored=false: %s", logs)
	}
}

// A provider's fact value is evidence and is served as composed.
func TestByIDAProviderStatusValueInTheHeadFallbackIsKept(t *testing.T) {
	const observed = "Current observed values: status: Complete."
	got, _ := readLeadProseByID(t, storedLeadProseAnswer(contractsv1.ContextFabricInvestigationComplete, legacyCompleteSentence, observed))
	if got.DirectJudgment != observed {
		t.Fatalf("direct_judgment = %q, want current_state's first sentence as composed, %q", got.DirectJudgment, observed)
	}
}

// No head and no current_state: the stored head is served as stored, and the
// Info line counts it.
func TestByIDAHeadWithNothingToFallBackToIsServedAsStored(t *testing.T) {
	got, logs := readLeadProseByID(t, storedLeadProseAnswer(contractsv1.ContextFabricInvestigationComplete, legacyCompleteSentence, ""))
	if got.DirectJudgment != legacyCompleteSentence || got.DeterministicAnswer != legacyCompleteSentence {
		t.Fatalf("served lead prose = %q / %q, want the stored head as stored", got.DirectJudgment, got.DeterministicAnswer)
	}
	if !strings.Contains(logs, `"lead_prose_served_as_stored":true`) {
		t.Fatalf("the completeness authority line must state lead_prose_served_as_stored=true: %s", logs)
	}
}

func TestByIDAStoredNotCompleteClauseIsServedInPlainWords(t *testing.T) {
	const observed = "Current observed values: open items: 3."
	stored := storedLeadProseAnswer(contractsv1.ContextFabricInvestigationComplete, "Counted 1 work item.", observed+" Not complete: readiness (partial: the source read was truncated).")
	got, _ := readLeadProseByID(t, stored)
	if want := observed + " Limited read: readiness (truncated: the source read was cut)."; got.CurrentState != want {
		t.Fatalf("current_state = %q, want %q", got.CurrentState, want)
	}
	requireNoTemplatePhrase(t, "current_state", got.CurrentState)
}

// Review r2: a stored complete head the server did not write, floored by id,
// is withheld and recomposed for the served status (the floor discloses the
// withholding); it is not kept with a status sentence appended.
func TestByIDAFlooredHeadTheServerDidNotWriteIsWithheld(t *testing.T) {
	stored := storedLeadProseAnswer(contractsv1.ContextFabricInvestigationComplete, "Ask Dev is on track.", "Current observed values: open items: 3.")
	stored.Limitations = []string{contractsv1.ContextFabricMemberStatusPoolCutLimitation("project", 50, 120)}
	stored.Completeness = contextfabric.ComputeAnswerCompleteness(stored)
	app, token, _ := newCompletenessAuthorityTestApp(t, legacyResultStore{result: stored}, false)
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, stored.ResultID))
	if recorder.Code != 200 {
		t.Fatalf("status = %d (body %s)", recorder.Code, recorder.Body.String())
	}
	var got contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("served result invalid: %v", err)
	}
	const want = "Some canonical or graph coverage was unavailable."
	if got.Status != contractsv1.ContextFabricInvestigationPartial || got.DirectJudgment != want || got.DeterministicAnswer != want {
		t.Fatalf("served %q with %q / %q, want partial with %q", got.Status, got.DirectJudgment, got.DeterministicAnswer, want)
	}
}

// Review r3: the same words in a provider's fact value are evidence. Only
// ACR's own closing clause is restated.
func TestByIDTheWordsInAProviderValueAreNotRestated(t *testing.T) {
	for _, currentState := range []string{
		"Current observed values: deployment note: Rollout began. Not complete: acceptance is pending.",
		"Current observed values: deployment note: Rollout began. Not complete: acceptance is pending. Not complete: readiness (partial: the source read was truncated) or not.",
		// The clause's shape, not at the end: not the closing clause.
		"Current observed values: note: x. Not complete: readiness (partial: the source read was truncated). Then more.",
	} {
		got, _ := readLeadProseByID(t, storedLeadProseAnswer(contractsv1.ContextFabricInvestigationComplete, "Counted 1 work item.", currentState))
		if got.CurrentState != currentState {
			t.Fatalf("current_state = %q, want the provider value as stored, %q", got.CurrentState, currentState)
		}
	}
	// ACR's clause after a provider value with the same words: only the clause is restated.
	for stored, want := range map[string]string{
		"Current observed values: note: Not complete: yet. Not complete: health (stale: the source read is out of date); readiness (partial: the source read was truncated).": "Current observed values: note: Not complete: yet. Limited read: health (stale: the source read is out of date); readiness (truncated: the source read was cut).",
		"Current observed values: note: Rollout began. Not complete: soon. Not complete: health (stale: the source read is out of date).":                                     "Current observed values: note: Rollout began. Not complete: soon. Limited read: health (stale: the source read is out of date).",
	} {
		if got, _ := readLeadProseByID(t, storedLeadProseAnswer(contractsv1.ContextFabricInvestigationComplete, "Counted 1 work item.", stored)); got.CurrentState != want {
			t.Fatalf("current_state = %q, want %q", got.CurrentState, want)
		}
	}
}
