package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func degradedResultWithCorrection(t *testing.T) ContextFabricInvestigationResult {
	t.Helper()
	r := buildFromTable(t, func(x answerBound) func(*ContextFabricInvestigationResult) { return x.Max })
	r.Status = ContextFabricInvestigationDegraded
	r.Completeness.TerminalStatus = ContextFabricInvestigationDegraded
	r.Completeness.TerminalReason = expectedTerminalReason(r)
	return r
}

func TestStatusCorrectionValidationClauses(t *testing.T) {
	t.Parallel()
	setBoth := func(r *ContextFabricInvestigationResult, c *ContextFabricStatusCorrection) {
		r.StatusCorrection = c
		r.Completeness.StatusCorrection = c
	}
	for _, tc := range []struct {
		name   string
		mutate func(r *ContextFabricInvestigationResult)
		want   string
	}{
		{"mirror_missing", func(r *ContextFabricInvestigationResult) {
			r.StatusCorrection = &ContextFabricStatusCorrection{ModelStatus: ContextFabricInvestigationComplete, Direction: ContextFabricStatusCorrectionCompleteToDegraded}
		}, "must equal the result's status_correction"},
		{"mirror_differs", func(r *ContextFabricInvestigationResult) {
			r.StatusCorrection = &ContextFabricStatusCorrection{ModelStatus: ContextFabricInvestigationComplete, Direction: ContextFabricStatusCorrectionCompleteToDegraded}
			r.Completeness.StatusCorrection = &ContextFabricStatusCorrection{ModelStatus: ContextFabricInvestigationPartial, Direction: ContextFabricStatusCorrectionPartialToDegraded}
		}, "must equal the result's status_correction"},
		{"served_complete", func(r *ContextFabricInvestigationResult) {
			r.Status = ContextFabricInvestigationComplete
			r.Completeness.TerminalStatus = ContextFabricInvestigationComplete
			r.Completeness.TerminalReason = ""
			setBoth(r, &ContextFabricStatusCorrection{ModelStatus: ContextFabricInvestigationPartial, Direction: ContextFabricStatusCorrectionPartialToDegraded})
		}, "cannot accompany status"},
		{"direction_not_member", func(r *ContextFabricInvestigationResult) {
			setBoth(r, &ContextFabricStatusCorrection{ModelStatus: ContextFabricInvestigationPartial, Direction: "partial_to_complete"})
		}, "is not a vocabulary member"},
		{"model_status_off_direction", func(r *ContextFabricInvestigationResult) {
			setBoth(r, &ContextFabricStatusCorrection{ModelStatus: ContextFabricInvestigationPartial, Direction: ContextFabricStatusCorrectionCompleteToDegraded})
		}, "does not match direction"},
		{"model_equals_served", func(r *ContextFabricInvestigationResult) {
			setBoth(r, &ContextFabricStatusCorrection{ModelStatus: ContextFabricInvestigationDegraded, Direction: ContextFabricStatusCorrectionDegradedToPartial})
		}, "equals the served status"},
		{"deciding_outcome_not_member", func(r *ContextFabricInvestigationResult) {
			setBoth(r, &ContextFabricStatusCorrection{ModelStatus: ContextFabricInvestigationComplete, Direction: ContextFabricStatusCorrectionCompleteToDegraded, DecidingOutcome: "bogus"})
		}, "deciding_outcome"},
		{"deciding_requirement_unknown", func(r *ContextFabricInvestigationResult) {
			setBoth(r, &ContextFabricStatusCorrection{ModelStatus: ContextFabricInvestigationComplete, Direction: ContextFabricStatusCorrectionCompleteToDegraded, DecidingRequirement: "no/such/row"})
		}, "names no completeness outcome row"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := degradedResultWithCorrection(t)
			if err := r.Validate(); err != nil {
				t.Fatalf("the uncorrected control must validate first: %v", err)
			}
			tc.mutate(&r)
			err := r.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, tc.want)
			}
		})
	}
	t.Run("valid_record_accepted", func(t *testing.T) {
		t.Parallel()
		r := degradedResultWithCorrection(t)
		setBoth(&r, &ContextFabricStatusCorrection{ModelStatus: ContextFabricInvestigationComplete, Direction: ContextFabricStatusCorrectionCompleteToDegraded})
		if err := r.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want a valid corrected document", err)
		}
	})
}

func TestTheStatusCorrectedGoldenExampleValidates(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "examples", "v1", "context_fabric_investigation_result_status_corrected.v1.json"))
	if err != nil {
		t.Fatalf("golden example missing: %v", err)
	}
	var r ContextFabricInvestigationResult
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.StatusCorrection == nil || r.Completeness.StatusCorrection == nil {
		t.Fatal("the example must carry the correction on both surfaces")
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}
