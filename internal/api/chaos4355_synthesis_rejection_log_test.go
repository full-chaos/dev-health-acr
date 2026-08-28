package api

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestContextFabricInvestigationFailuresLogViolatedBoundAndClaimIndex is the
// (d) proof: before this fix, violated_bound was ONLY in the HTTP response
// body (writeContextFabricRejectionError) -- the CHAOS-4355 diagnosis
// session (19:10 08-27) had to re-derive it from source because the live
// 422's own server-side logs carried nothing beyond the generic
// failure_classification="synthesis_rejected" every rejection shares. This
// pins that logContextFabricFailure now also carries violated_bound (always,
// once ClassifySynthesisRejection/ClassifyInterpretationRejection attaches a
// *ModelBoundViolation) and claim_index (only when >= 0, i.e. only for a
// claim-scoped bound) -- and that a business-rule rejection with no
// ModelBoundViolation carries neither key at all, not an empty/zero one.
func TestContextFabricInvestigationFailuresLogViolatedBoundAndClaimIndex(t *testing.T) {
	rowsViolation := contextfabric.NewModelBoundViolation("synthesis.claimed_fact.rows.model_authored",
		fmt.Errorf("%w: claimed fact sets rows", contextfabric.ErrSynthesisRejected))
	rowsViolation.ClaimIndex = 2

	cases := []struct {
		name           string
		err            error
		wantBound      any
		wantClaimIndex any
	}{
		{
			name: "a driver bound violation logs the bound with no claim index",
			err: contextfabric.NewModelBoundViolation("synthesis.driver.title.max_length",
				fmt.Errorf("%w: driver title too long", contextfabric.ErrSynthesisRejected)),
			wantBound: "synthesis.driver.title.max_length", wantClaimIndex: nil,
		},
		{
			name:           "a claim-scoped rows-authorship bound logs the bound AND the claim index",
			err:            rowsViolation,
			wantBound:      "synthesis.claimed_fact.rows.model_authored",
			wantClaimIndex: float64(2), // JSON numbers decode as float64
		},
		{
			name: "a business-rule rejection with no ModelBoundViolation logs neither key",
			err:  contextfabric.ErrSynthesisRejected,
			// entry[...] on an absent key returns the untyped nil interface
			// value -- distinct from a present JSON null, which this must
			// never emit either (omitted entirely, not present-but-empty).
			wantBound: nil, wantClaimIndex: nil,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
				return contextfabric.InvestigationResult{}, testCase.err
			}))
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, investigationRequest(t, token))

			entry := decodeFailureLog(t, logs.String())
			if got, want := entry["violated_bound"], testCase.wantBound; got != want {
				t.Fatalf("violated_bound = %#v, want %#v (body=%s)", got, want, response.Body.String())
			}
			if got, want := entry["claim_index"], testCase.wantClaimIndex; got != want {
				t.Fatalf("claim_index = %#v, want %#v", got, want)
			}
		})
	}
}
