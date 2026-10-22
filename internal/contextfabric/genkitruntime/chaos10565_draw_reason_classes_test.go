package genkitruntime

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// A rejected draw whose error is not a synthesis rejection logs its own error
// class from a closed set, never the word that means "a rule rejected this and
// the vocabulary has no entry".
func TestNonRejectionDrawErrorsLogTheirOwnClass(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"deadline", fmt.Errorf("draw: %w", context.DeadlineExceeded), "error_deadline_exceeded"},
		{"canceled", fmt.Errorf("draw: %w", context.Canceled), "error_canceled"},
		{"model_cancelled_sentinel", fmt.Errorf("%w", contextfabric.ErrModelCancelled), "error_canceled"},
		{"model_output", fmt.Errorf("%w: model refused the request", contextfabric.ErrModelOutput), "error_model_output"},
		{"anything_else", errors.New("boom"), "error_other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := synthesisDrawReason(tc.err, nil); got != tc.want {
				t.Fatalf("synthesisDrawReason() = %q, want %q", got, tc.want)
			}
		})
	}
	for _, stage := range []string{"empty", "not_json", "schema", "decode"} {
		local := &decodeSynthesisRejection{schemaStage: stage, cause: fmt.Errorf("%w: model output did not match the expected schema", contextfabric.ErrModelOutput)}
		if got := synthesisDrawReason(local.cause, local); got != string(contextfabric.RejectionReasonOutputSchemaMismatch) {
			t.Fatalf("schema stage %q = %q, want output_schema_mismatch", stage, got)
		}
	}
	if got := synthesisDrawReason(nil, nil); got != "" {
		t.Fatalf("synthesisDrawReason(nil) = %q, want the empty word a successful draw renders as none", got)
	}
}

// A SynthesisRejection keeps its own canonical reason; one that carries a
// reason outside the table stays unclassified (the vocabulary is closed).
func TestRejectionReasonsStayAsTheTableNamesThem(t *testing.T) {
	known := contextfabric.NewSynthesisRejection(contextfabric.RejectionReasonClaimFieldUnobserved, errors.New("x"))
	if got := synthesisDrawReason(known, nil); got != string(contextfabric.RejectionReasonClaimFieldUnobserved) {
		t.Fatalf("known rejection = %q", got)
	}
	rogue := contextfabric.NewSynthesisRejection("a reason nobody declared", errors.New("x"))
	if got := synthesisDrawReason(rogue, nil); got != string(contextfabric.RejectionReasonUnclassified) {
		t.Fatalf("undeclared reason = %q, want unclassified", got)
	}
}
