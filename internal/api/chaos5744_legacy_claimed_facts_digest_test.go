package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// legacyByIDResultWithClaimedWorkFact is a valid, immutable stored result
// with NO outcome rows at all (the pre-outcome-layer legacy shape --
// DeriveContextFabricAnswerCompletenessState reads this as not_derived) that
// still carries one real claimed fact -- the shape that names the invariant
// the completeness-authority digest must hold: a document's claimed-fact
// count is real evidence, present whether or not the outcome-derivation
// authority can classify the document at all.
func legacyByIDResultWithClaimedWorkFact() contractsv1.ContextFabricInvestigationResult {
	result := validContextFabricInvestigationResult()
	result.ResultID = "result_byid_legacy_claim01"
	result.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{{
		ClaimID: "claim_work_1", Kind: contractsv1.ContextFabricFactWork,
		Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"},
		Field:   "work", Value: contractsv1.ContextFabricScalarValue{String: ptrString("in progress")},
	}}
	// Outcomes stays nil/empty -- the legacy shape this fixture exists to
	// name -- so Completeness.State derives not_derived, never a real
	// server state.
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	return result
}

// TestByIDRoute_LegacyResultStillPublishesItsClaimedFactsDigest pins that a
// legacy stored result with a real claimed work fact and no outcome rows
// still publishes claimed_facts_work=1 on the completeness-authority line --
// never a 0, which would read as "none claimed" for a document that
// genuinely has one.
func TestByIDRoute_LegacyResultStillPublishesItsClaimedFactsDigest(t *testing.T) {
	result := legacyByIDResultWithClaimedWorkFact()
	app, token, logs := newCompletenessAuthorityTestApp(t, legacyResultStore{result: result}, false)

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, result.ResultID))

	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	const line = "context fabric completeness authority"
	if !strings.Contains(logs.String(), line) {
		t.Fatalf("the by-id route must emit the completeness authority measurement, got: %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"basis":"unavailable"`) {
		t.Fatalf("the premise moved: this fixture must reach the basis=unavailable (no outcome rows) exit, got: %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"claimed_facts_work":1`) {
		t.Fatalf("claimed_facts_work must be 1 on a basis=unavailable line whose document carries one claimed work fact, got: %s", logs.String())
	}
}
