package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestContextFabricInvestigationFailuresLogPreSynthesisNarrowingBasis is the
// (d) proof for CHAOS-4726: 40/40 live synthesis_rejected 422s (org
// 70d529e0) carried a rejection_reason but no narrowing basis at all,
// because RecordPlanNarrowing's "context fabric plan narrowing" line only
// reaches the assembled_result stage on a result that actually assembles.
// This pins that logContextFabricFailure now also carries narrowing_basis,
// narrowing_last_stage and narrowing_last_basis whenever a
// SynthesisNarrowingSnapshot reaches it, and that an error carrying none
// logs none of the three keys at all -- not empty-string placeholders.
func TestContextFabricInvestigationFailuresLogPreSynthesisNarrowingBasis(t *testing.T) {
	cause := errors.New("synthesize investigation: rejected")

	cases := []struct {
		name          string
		err           error
		wantBasis     any
		wantLastStage any
		wantLastBasis any
	}{
		{
			name: "a rejection after stage 2 grouped narrowing logs the declared basis and the last step actually taken",
			err: contextfabric.NewSynthesisNarrowingSnapshot(
				contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
				contractsv1.ContextFabricPlanNarrowingSynthesisInput,
				contractsv1.ContextFabricNarrowingBasisOverlapAwareSetCover,
				cause,
			),
			wantBasis:     "canonical_id_lexical",
			wantLastStage: "synthesis_input",
			wantLastBasis: "overlap_aware_set_cover",
		},
		{
			name: "a rejection with nothing narrowed before synthesis logs the declared basis and empty last-step fields",
			err: contextfabric.NewSynthesisNarrowingSnapshot(
				contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
				"", "",
				cause,
			),
			wantBasis:     "canonical_id_lexical",
			wantLastStage: "",
			wantLastBasis: "",
		},
		{
			name:          "an error carrying no snapshot logs none of the three keys",
			err:           cause,
			wantBasis:     nil,
			wantLastStage: nil,
			wantLastBasis: nil,
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
			if got, want := entry["narrowing_basis"], testCase.wantBasis; got != want {
				t.Fatalf("narrowing_basis = %#v, want %#v (body=%s)", got, want, response.Body.String())
			}
			if got, want := entry["narrowing_last_stage"], testCase.wantLastStage; got != want {
				t.Fatalf("narrowing_last_stage = %#v, want %#v", got, want)
			}
			if got, want := entry["narrowing_last_basis"], testCase.wantLastBasis; got != want {
				t.Fatalf("narrowing_last_basis = %#v, want %#v", got, want)
			}
		})
	}
}
